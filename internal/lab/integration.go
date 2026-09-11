package lab

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const integrationRecoveryTarget = 15 * time.Second

const integrationFirstSource = "camera-01"
const integrationSecondSource = "camera-02"

var integrationSourceIDs = []string{integrationFirstSource, integrationSecondSource}

type integrationAssertion struct {
	Name      string         `json:"name"`
	Passed    bool           `json:"passed"`
	CheckedAt time.Time      `json:"checked_at"`
	Evidence  map[string]any `json:"evidence,omitempty"`
}

type integrationReport struct {
	StartedAt     time.Time              `json:"started_at"`
	FinishedAt    time.Time              `json:"finished_at"`
	Success       bool                   `json:"success"`
	Scope         string                 `json:"scope"`
	TestRoot      string                 `json:"test_root"`
	Limitations   []string               `json:"limitations"`
	Assertions    []integrationAssertion `json:"assertions"`
	Timings       map[string]float64     `json:"timings_seconds"`
	Error         string                 `json:"error,omitempty"`
	CleanupErrors []string               `json:"cleanup_errors"`
}

type integrationRun struct {
	paths    Paths
	settings Settings
	exe      string
	report   integrationReport
	// Remember only identities observed in this test root. Cleanup never stops
	// another project, even if the test's fixed ports were already occupied.
	owned          map[string]int
	startAttempted bool
}

// RunIntegration exercises real media in an isolated directory. It never calls
// Setup, downloads tools, or changes the user's recording or control files.
// Call this through the actual videolab executable, not a Go test executable:
// child commands intentionally exercise the same CLI that the user runs.
func RunIntegration(p Paths) (result error) {
	if p.Running() {
		return errors.New("stop the lab before running its isolated media test: ./lab stop")
	}
	settings, err := p.LoadSettings()
	if err != nil {
		return err
	}
	// Start() uses this lock too. Holding it prevents the original lab from
	// being started between our free-port check and test-owned startup.
	lock, err := fileLock(filepath.Join(p.Local, "startup.lock"), true)
	if err != nil {
		return errors.New("another lab start or media test is in progress; try again after it finishes")
	}
	defer lock.Close()
	if p.Running() {
		return errors.New("the lab started before the media test; stop it first")
	}
	for _, name := range []string{settings.FFmpeg, settings.FFprobe, p.BinaryPath()} {
		info, err := os.Stat(name)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return errors.New("a required video tool is missing or not executable; complete ./lab setup before testing")
		}
	}
	settings.Host = "127.0.0.1"
	if err := checkPorts(settings); err != nil {
		return errors.New("the test's video ports are already in use; stop their owner before testing")
	}
	exe, err := os.Executable()
	if err != nil {
		return errors.New("could not locate the running lab executable")
	}
	testPaths, testSettings, err := prepareIntegrationRoot(p, settings)
	if err != nil {
		return err
	}
	run := &integrationRun{
		paths: testPaths, settings: testSettings, exe: exe, owned: map[string]int{},
		report: integrationReport{
			StartedAt: time.Now().UTC(), Scope: "Two concurrent generated video sources: independent recording, forwarding failures, path permissions, and delivery profiles",
			TestRoot: testPaths.Root, Assertions: []integrationAssertion{}, Timings: map[string]float64{}, CleanupErrors: []string{},
			Limitations: []string{
				"No physical camera, browser playback, or physical internet connection is tested.",
				"Recovery is elapsed time from a relay command or process kill to a successful decoded-video probe, including probe overhead.",
				"This is not camera-to-screen (glass-to-glass) delay, frame freshness, or a latency percentile.",
				"Recording persistence is checked across orderly stop and restart, not sudden power loss.",
				"Orderly shutdown stops recorders before publishers. Unexpected source disappearance can damage a trailing frame; separate disconnect diagnostics retain that known limitation.",
				"No cloud service is contacted and no recording upload is tested.",
			},
		},
	}
	started := time.Now()
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(signalCtx, 6*time.Minute)
	defer cancel()
	defer func() {
		// Cancellation of a check must never cancel cleanup as well.
		run.cleanup()
		if result != nil {
			run.report.Error = result.Error()
		}
		if len(run.report.CleanupErrors) > 0 {
			result = errors.Join(result, errors.New("test-owned services did not all stop cleanly; inspect the test report"))
		}
		run.report.Success = result == nil
		run.report.FinishedAt = time.Now().UTC()
		run.report.Timings["total_test_and_cleanup"] = integrationSeconds(time.Since(started))
		for _, path := range []string{filepath.Join(run.paths.Reports, "integration.json"), filepath.Join(p.Reports, "latest-integration.json")} {
			if err := AtomicJSON(path, run.report); err != nil {
				result = errors.Join(result, errors.New("could not write the private integration report"))
			}
		}
		fmt.Printf("Report: %s\n", filepath.Join(p.Reports, "latest-integration.json"))
		fmt.Printf("Generated test recordings kept in: %s\n", run.paths.Recordings)
	}()
	fmt.Println("Testing two concurrent generated video sources in a private test folder. Your lab must remain stopped.")
	result = run.checks(ctx)
	return result
}

func prepareIntegrationRoot(p Paths, settings Settings) (Paths, Settings, error) {
	if err := os.MkdirAll(p.Reports, 0700); err != nil {
		return Paths{}, Settings{}, errors.New("could not create the test report directory")
	}
	root, err := os.MkdirTemp(p.Reports, "test-run-")
	if err != nil {
		return Paths{}, Settings{}, errors.New("could not create a private test directory")
	}
	testPaths := NewPaths(root)
	for _, dir := range []string{testPaths.Local, testPaths.Recordings, testPaths.Reports} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return Paths{}, Settings{}, errors.New("could not prepare the private test directory")
		}
	}
	// Clone the installed tool paths and version, but bind test input only to
	// loopback and replace credentials. A saved camera connection cannot join it.
	settings.Host = "127.0.0.1"
	for _, secret := range []*string{&settings.PublisherPassword, &settings.PublishPassphrase, &settings.RelayPassword, &settings.CentralPassphrase} {
		value, err := randomSecret()
		if err != nil {
			return Paths{}, Settings{}, errors.New("could not generate private test credentials")
		}
		*secret = value
	}
	settings.Sources = nil
	for index, id := range integrationSourceIDs {
		password, err := randomSecret()
		if err != nil {
			return Paths{}, Settings{}, errors.New("could not generate private source credentials")
		}
		phrase, err := randomSecret()
		if err != nil {
			return Paths{}, Settings{}, errors.New("could not generate private source encryption settings")
		}
		settings.Sources = append(settings.Sources, SourceConfig{ID: id, Label: fmt.Sprintf("Generated camera %d", index+1), PublisherUser: "test-" + id, PublisherPassword: password, PublishPassphrase: phrase})
	}
	// Legacy fields mirror the first test source for migration compatibility.
	settings.PublisherUser = settings.Sources[0].PublisherUser
	settings.PublisherPassword = settings.Sources[0].PublisherPassword
	settings.PublishPassphrase = settings.Sources[0].PublishPassphrase
	for name, value := range map[string]any{
		"settings.json": settings, "field.yml": serverConfig(settings, false),
		"central.yml": serverConfig(settings, true), "control.json": defaultControl(),
	} {
		if err := AtomicJSON(filepath.Join(testPaths.Local, name), value); err != nil {
			return Paths{}, Settings{}, errors.New("could not save private test settings")
		}
	}
	if err := os.Symlink(p.Tools, testPaths.Tools); err != nil {
		return Paths{}, Settings{}, errors.New("could not share the already installed video tools with the test")
	}
	return testPaths, settings, nil
}

func (r *integrationRun) check(name string, passed bool, evidence map[string]any) error {
	r.report.Assertions = append(r.report.Assertions, integrationAssertion{Name: name, Passed: passed, CheckedAt: time.Now().UTC(), Evidence: evidence})
	if !passed {
		fmt.Println("FAIL:", name)
		return errors.New(name)
	}
	fmt.Println("PASS:", name)
	return nil
}

func integrationSeconds(value time.Duration) float64 {
	return math.Round(value.Seconds()*1000) / 1000
}

// A child can be noisy or broken. Keep its captured output bounded and never
// propagate raw stderr, which may contain complete publishing credentials.
type integrationBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *integrationBuffer) Write(data []byte) (int, error) {
	n := len(data)
	remaining := b.limit - b.Len()
	if remaining < n {
		b.truncated = true
	}
	if remaining > 0 {
		if remaining > n {
			remaining = n
		}
		_, _ = b.Buffer.Write(data[:remaining])
	}
	return n, nil
}

func integrationCommand(ctx context.Context, timeout time.Duration, directory, label, executable string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = directory
	command.WaitDelay = time.Second
	stdout := &integrationBuffer{limit: 2 << 20}
	stderr := &integrationBuffer{limit: 8 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s stopped after cancellation or its time limit", label)
		}
		return nil, fmt.Errorf("%s failed; raw output is withheld because it can contain connection secrets", label)
	}
	if stdout.truncated {
		return nil, fmt.Errorf("%s returned more output than the test allows", label)
	}
	return stdout.Bytes(), nil
}

func (r *integrationRun) command(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	label := "lab command"
	if len(args) > 0 {
		label = "lab " + args[0]
		if args[0] == "--source" && len(args) > 2 {
			label = "lab " + args[2] + " (" + args[1] + ")"
		}
	}
	return integrationCommand(ctx, timeout, r.paths.Root, label, r.exe, append([]string{"--root", r.paths.Root}, args...)...)
}

func (r *integrationRun) sourceCommand(ctx context.Context, timeout time.Duration, sourceID string, args ...string) ([]byte, error) {
	return r.command(ctx, timeout, append([]string{"--source", sourceID}, args...)...)
}

func (r *integrationRun) status(ctx context.Context) (State, error) {
	var state State
	data, err := r.command(ctx, 5*time.Second, "status", "--json")
	if err != nil {
		return state, err
	}
	if json.Unmarshal(data, &state) != nil {
		return state, errors.New("test status was not valid JSON")
	}
	for name, worker := range state.Workers {
		if worker.Running && worker.PID > 1 {
			r.owned[name] = worker.PID
		}
	}
	for id, source := range state.Sources {
		for name, worker := range source.Workers {
			if worker.Running && worker.PID > 1 {
				r.owned[name+"/"+id] = worker.PID
			}
		}
	}
	return state, nil
}

func integrationSelectSource(state State, id string) State {
	stream, ok := state.Sources[id]
	if !ok {
		return State{Workers: map[string]WorkerState{}}
	}
	return State{Running: state.Running, Control: stream.Control, Source: stream.Source, Remote: stream.Remote,
		Recording: stream.Recording, Workers: stream.Workers, UpdatedAt: state.UpdatedAt}
}

func (r *integrationRun) sourceStatus(ctx context.Context, id string) (State, error) {
	state, err := r.status(ctx)
	if err != nil {
		return state, err
	}
	if _, ok := state.Sources[id]; !ok {
		return State{}, errors.New("the requested test source is missing from status")
	}
	return integrationSelectSource(state, id), nil
}

func integrationSleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *integrationRun) waitState(ctx context.Context, name string, timeout time.Duration, predicate func(State) bool, sourceID ...string) (State, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	var state State
	for ctx.Err() == nil {
		current, err := r.status(ctx)
		if err == nil {
			if len(sourceID) > 0 {
				current = integrationSelectSource(current, sourceID[0])
			}
			state = current
			if predicate(state) {
				return state, r.check(name, true, map[string]any{"observed_after_seconds": integrationSeconds(time.Since(start))})
			}
		}
		if integrationSleep(ctx, 400*time.Millisecond) != nil {
			break
		}
	}
	return state, r.check(name, false, map[string]any{"time_limit_seconds": timeout.Seconds(), "last_state": integrationStateEvidence(state)})
}

func integrationStateEvidence(s State) map[string]any {
	return map[string]any{
		"running": s.Running, "source_observed": s.Source.Observed, "source_ready": s.Source.Ready, "source_bytes": s.Source.BytesReceived,
		"remote_observed": s.Remote.Observed, "remote_ready": s.Remote.Ready, "recorder_running": s.Recording.Running,
		"recording_segments": s.Recording.Segments, "recording_bytes": s.Recording.Bytes,
		"profile": s.Profile, "test_source_enabled": s.DemoEnabled,
	}
}

type integrationMedia struct {
	Codec         string  `json:"codec"`
	Width         int     `json:"width"`
	Height        int     `json:"height"`
	FrameRate     float64 `json:"reported_frames_per_second"`
	ObservedRate  float64 `json:"decoded_timestamp_frames_per_second"`
	DecodedFrames int     `json:"decoded_frames"`
}

func integrationRate(value string) float64 {
	parts := strings.Split(value, "/")
	numerator, err := strconv.ParseFloat(parts[0], 64)
	if err != nil || math.IsNaN(numerator) || math.IsInf(numerator, 0) {
		return 0
	}
	if len(parts) == 1 {
		return numerator
	}
	if len(parts) != 2 {
		return 0
	}
	denominator, err := strconv.ParseFloat(parts[1], 64)
	if err != nil || denominator <= 0 || math.IsNaN(denominator) || math.IsInf(denominator, 0) {
		return 0
	}
	return numerator / denominator
}

func parseIntegrationProbe(data []byte) (integrationMedia, error) {
	var payload struct {
		Streams []struct {
			Codec   string `json:"codec_name"`
			Width   int    `json:"width"`
			Height  int    `json:"height"`
			Rate    string `json:"r_frame_rate"`
			Average string `json:"avg_frame_rate"`
		} `json:"streams"`
		Frames []struct {
			Type      string `json:"media_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			Timestamp string `json:"best_effort_timestamp_time"`
		} `json:"frames"`
	}
	if json.Unmarshal(data, &payload) != nil || len(payload.Streams) != 1 {
		return integrationMedia{}, errors.New("video probe did not report exactly one selected video stream")
	}
	stream := payload.Streams[0]
	media := integrationMedia{Codec: stream.Codec, Width: stream.Width, Height: stream.Height, FrameRate: integrationRate(stream.Rate)}
	if media.FrameRate <= 0 {
		media.FrameRate = integrationRate(stream.Average)
	}
	var timestamps []float64
	for _, frame := range payload.Frames {
		if frame.Type != "video" {
			continue
		}
		if frame.Width != media.Width || frame.Height != media.Height || frame.Width <= 0 || frame.Height <= 0 {
			return integrationMedia{}, errors.New("decoded frame dimensions do not match the advertised video stream")
		}
		media.DecodedFrames++
		value, err := strconv.ParseFloat(frame.Timestamp, 64)
		if err == nil && !math.IsNaN(value) && !math.IsInf(value, 0) {
			timestamps = append(timestamps, value)
		}
	}
	if media.DecodedFrames < 3 {
		return integrationMedia{}, errors.New("the video probe decoded fewer than three frames")
	}
	var steps []float64
	for i := 1; i < len(timestamps); i++ {
		if step := timestamps[i] - timestamps[i-1]; step > 0 {
			steps = append(steps, step)
		}
	}
	if len(steps) >= 2 {
		sort.Float64s(steps)
		media.ObservedRate = math.Round((1/steps[len(steps)/2])*1000) / 1000
	}
	return media, nil
}

func (r *integrationRun) probe(ctx context.Context, target string) (integrationMedia, error) {
	args := []string{"-v", "error"}
	if strings.HasPrefix(target, "rtsp://") {
		args = append(args, "-rtsp_transport", "tcp", "-timeout", "3000000")
	}
	args = append(args, "-analyzeduration", "2000000", "-probesize", "2000000", "-select_streams", "v:0", "-read_intervals", "%+1", "-show_frames", "-show_entries", "stream=codec_name,width,height,r_frame_rate,avg_frame_rate:frame=media_type,width,height,best_effort_timestamp_time", "-of", "json", target)
	data, err := integrationCommand(ctx, 8*time.Second, r.paths.Root, "decoded-video probe", r.settings.FFprobe, args...)
	if err != nil {
		return integrationMedia{}, err
	}
	return parseIntegrationProbe(data)
}

func (r *integrationRun) waitVideo(ctx context.Context, name, target string, width, height int, fps float64, timeout time.Duration) (integrationMedia, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	var observed integrationMedia
	lastError := "No decoded video sample completed."
	for ctx.Err() == nil {
		media, err := r.probe(ctx, target)
		if err == nil {
			observed = media
			matches := media.Codec == "h264" && media.Width == width && media.Height == height
			if fps > 0 {
				matches = matches && math.Abs(media.FrameRate-fps) < 0.1 && math.Abs(media.ObservedRate-fps) < 0.1
			}
			if matches {
				return media, r.check(name, true, map[string]any{"media": media, "observed_after_seconds": integrationSeconds(time.Since(start))})
			}
			lastError = "Decoded video did not yet match the requested dimensions and frame rate."
		} else {
			lastError = err.Error()
		}
		if integrationSleep(ctx, 300*time.Millisecond) != nil {
			break
		}
	}
	return observed, r.check(name, false, map[string]any{"media": observed, "time_limit_seconds": timeout.Seconds(), "detail": lastError})
}

func integrationFrameHashes(data []byte) ([]string, error) {
	var hashes []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) != 6 {
			return nil, errors.New("decoded-frame checksum output had an unexpected format")
		}
		hash := strings.TrimSpace(fields[5])
		if decoded, err := hex.DecodeString(hash); err != nil || len(decoded) != 16 {
			return nil, errors.New("decoded-frame checksum output contained an invalid checksum")
		}
		hashes = append(hashes, hash)
	}
	return hashes, nil
}

func (r *integrationRun) motion(ctx context.Context, name, target string) error {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-rtsp_transport", "tcp", "-timeout", "3000000", "-i", target,
		"-map", "0:v:0", "-an", "-frames:v", "12", "-pix_fmt", "yuv420p", "-f", "framemd5", "-"}
	data, err := integrationCommand(ctx, 10*time.Second, r.paths.Root, "moving-picture decode", r.settings.FFmpeg, args...)
	if err != nil {
		return r.check(name, false, map[string]any{"detail": err.Error()})
	}
	hashes, err := integrationFrameHashes(data)
	if err != nil {
		return r.check(name, false, map[string]any{"detail": err.Error()})
	}
	unique := map[string]bool{}
	for _, hash := range hashes {
		unique[hash] = true
	}
	return r.check(name, len(hashes) >= 6 && len(unique) > 1, map[string]any{"decoded_frames": len(hashes), "different_frame_checksums": len(unique)})
}

func (r *integrationRun) checks(ctx context.Context) error {
	localURL := integrationVideoURL(Field, integrationFirstSource)
	remoteURL := integrationVideoURL(Central, integrationFirstSource)
	otherRemoteURL := integrationVideoURL(Central, integrationSecondSource)
	r.startAttempted = true
	if _, err := r.command(ctx, 20*time.Second, "start"); err != nil {
		return err
	}
	if err := r.rejectUnauthorizedPublisher(ctx); err != nil {
		return err
	}
	if err := r.rejectUnauthorizedRTSPPublisher(ctx); err != nil {
		return err
	}
	for _, id := range integrationSourceIDs {
		if _, err := r.sourceCommand(ctx, 5*time.Second, id, "demo"); err != nil {
			return err
		}
	}
	for _, id := range integrationSourceIDs {
		if _, err := r.waitState(ctx, id+": generated input, forwarded video, and recording become available", 35*time.Second,
			func(s State) bool {
				return s.Running && s.DemoEnabled && s.Source.Ready && s.Remote.Ready && s.Recording.Running && s.Recording.Segments > 0
			}, id); err != nil {
			return err
		}
		for _, view := range []struct {
			label string
			ports Ports
		}{{"source", Field}, {"forwarded", Central}} {
			target := integrationVideoURL(view.ports, id)
			if _, err := r.waitVideo(ctx, id+": "+view.label+" video decodes at 1280 × 720 and 30 fps", target, 1280, 720, 30, 20*time.Second); err != nil {
				return err
			}
			if err := r.motion(ctx, id+": decoded "+view.label+" pictures change over time", target); err != nil {
				return err
			}
		}
	}

	fmt.Println("Stopping only camera-01 forwarding and observing both sources for ten seconds.")
	otherBefore, err := r.sourceStatus(ctx, integrationSecondSource)
	if err != nil {
		return err
	}
	cutAt := time.Now()
	if _, err := r.sourceCommand(ctx, 5*time.Second, integrationFirstSource, "relay-off"); err != nil {
		return err
	}
	before, err := r.waitState(ctx, "camera-01: forwarded stream stops when its forwarding is disabled", 12*time.Second,
		func(s State) bool { return s.Remote.Observed && !s.Remote.Ready && !s.Workers["relay"].Running }, integrationFirstSource)
	if err != nil {
		return err
	}
	var samples []map[string]any
	observationStarted := time.Now()
	after := before
	otherAfter := otherBefore
	for time.Since(observationStarted) < 10*time.Second {
		all, statusErr := r.status(ctx)
		err = statusErr
		if err != nil {
			return err
		}
		after = integrationSelectSource(all, integrationFirstSource)
		otherAfter = integrationSelectSource(all, integrationSecondSource)
		sample := map[string]any{integrationFirstSource: integrationStateEvidence(after), integrationSecondSource: integrationStateEvidence(otherAfter)}
		sample["seconds_after_relay_off"] = integrationSeconds(time.Since(cutAt))
		samples = append(samples, sample)
		if !after.Source.Ready || !after.Recording.Running || after.Remote.Ready || !otherAfter.Source.Ready || !otherAfter.Remote.Ready || !otherAfter.Recording.Running {
			return r.check("Both local recordings and camera-02 delivery continue throughout camera-01's forwarding interruption", false, map[string]any{"samples": samples})
		}
		if err := integrationSleep(ctx, 500*time.Millisecond); err != nil {
			return errors.New("the forwarding interruption observation was cancelled")
		}
	}
	if err := r.check("Both local recordings and camera-02 delivery continue throughout camera-01's forwarding interruption", true, map[string]any{"samples": samples}); err != nil {
		return err
	}
	if err := r.check("camera-01: source bytes and its finalized recording data grow while forwarding is stopped",
		after.Source.BytesReceived > before.Source.BytesReceived && after.Recording.Segments > before.Recording.Segments && after.Recording.Bytes > before.Recording.Bytes,
		map[string]any{"before": integrationStateEvidence(before), "after": integrationStateEvidence(after)}); err != nil {
		return err
	}
	if err := r.check("camera-02: source bytes and its finalized recording data grow while camera-01 forwarding is stopped",
		otherAfter.Source.BytesReceived > otherBefore.Source.BytesReceived && otherAfter.Recording.Segments > otherBefore.Recording.Segments && otherAfter.Recording.Bytes > otherBefore.Recording.Bytes,
		map[string]any{"before": integrationStateEvidence(otherBefore), "after": integrationStateEvidence(otherAfter)}); err != nil {
		return err
	}
	if err := r.check("camera-01 relay-off does not restart camera-02 workers", integrationSameWorkers(otherBefore, otherAfter), nil); err != nil {
		return err
	}
	if _, err := r.waitVideo(ctx, "camera-01: source still decodes while its forwarding is stopped", localURL, 1280, 720, 30, 12*time.Second); err != nil {
		return err
	}
	if err := r.motion(ctx, "camera-02: forwarded pictures still move while camera-01 forwarding is stopped", otherRemoteURL); err != nil {
		return err
	}
	r.report.Timings["forwarding_interruption"] = integrationSeconds(time.Since(cutAt))

	recoveryAt := time.Now()
	recoveryCtx, cancelRecovery := context.WithTimeout(ctx, integrationRecoveryTarget)
	_, err = r.sourceCommand(recoveryCtx, 5*time.Second, integrationFirstSource, "relay-on")
	if err == nil {
		_, err = r.waitVideo(recoveryCtx, "camera-01: forwarded video decodes within 15 seconds of relay-on", remoteURL, 1280, 720, 30, integrationRecoveryTarget)
	}
	cancelRecovery()
	r.report.Timings["relay_on_to_decoded_probe"] = integrationSeconds(time.Since(recoveryAt))
	if err != nil {
		return err
	}
	if err := r.check("Measured relay-on recovery meets the provisional target", time.Since(recoveryAt) <= integrationRecoveryTarget,
		map[string]any{"measured_seconds": r.report.Timings["relay_on_to_decoded_probe"], "target_seconds": integrationRecoveryTarget.Seconds()}); err != nil {
		return err
	}

	if err := r.crashRelay(ctx, remoteURL); err != nil {
		return err
	}
	if err := r.relayWaitCheck(ctx, remoteURL); err != nil {
		return err
	}
	if err := r.relayLinkCheck(ctx, remoteURL); err != nil {
		return err
	}
	fmt.Println("Changing camera-01's delivery size while camera-02 keeps its own profile.")
	if _, err := r.sourceCommand(ctx, 5*time.Second, integrationFirstSource, "profile", "small"); err != nil {
		return err
	}
	if _, err := r.waitVideo(ctx, "camera-01: small profile decodes at 640 × 360 and 20 fps", remoteURL, 640, 360, 20, 25*time.Second); err != nil {
		return err
	}
	if err := r.waitRelayLink(ctx, "camera-01: small profile remains on the local RTSP/TCP relay", "local"); err != nil {
		return err
	}
	if _, err := r.waitVideo(ctx, "camera-01: local source stays at 1280 × 720 and 30 fps", localURL, 1280, 720, 30, 12*time.Second); err != nil {
		return err
	}
	if _, err := r.waitVideo(ctx, "camera-02: forwarded video keeps its independent 1280 × 720 profile", otherRemoteURL, 1280, 720, 30, 12*time.Second); err != nil {
		return err
	}
	if _, err := r.sourceCommand(ctx, 5*time.Second, integrationFirstSource, "profile", "copy"); err != nil {
		return err
	}
	if _, err := r.waitVideo(ctx, "camera-01: copy profile restores 1280 × 720 delivery", remoteURL, 1280, 720, 30, 25*time.Second); err != nil {
		return err
	}
	if err := r.waitRelayLink(ctx, "camera-01: restored copy profile remains on the local RTSP/TCP relay", "local"); err != nil {
		return err
	}
	return r.recordingPersistence(ctx)
}

func integrationVideoURL(ports Ports, id string) string {
	return fmt.Sprintf("rtsp://127.0.0.1:%d/%s", ports.RTSP, id)
}

func integrationSameWorkers(before, after State) bool {
	for _, name := range []string{"demo", "relay", "recorder"} {
		old, new := before.Workers[name], after.Workers[name]
		if !old.Running || !new.Running || old.PID <= 1 || old.PID != new.PID || old.Starts != new.Starts {
			return false
		}
	}
	return true
}

func (r *integrationRun) rejectUnauthorizedPublisher(ctx context.Context) error {
	// Test before the real test source connects. Otherwise an existing publisher
	// could cause a rejection even if the authorization rule were broken.
	for _, attempt := range []struct {
		target, credentialSource string
		wrongPassword            bool
	}{
		{integrationFirstSource, integrationFirstSource, true},
		{integrationSecondSource, integrationFirstSource, false},
		{integrationFirstSource, integrationSecondSource, false},
	} {
		badSettings := r.settings
		badSettings.Sources = GetSources(r.settings)
		var credentials SourceConfig
		for _, source := range badSettings.Sources {
			if source.ID == attempt.credentialSource {
				credentials = source
			}
		}
		for i := range badSettings.Sources {
			if badSettings.Sources[i].ID == attempt.target {
				// Keep the target's correct encryption passphrase. Only publisher
				// permission changes, so encryption failure cannot mask broken ACLs.
				badSettings.Sources[i].PublisherUser = credentials.PublisherUser
				badSettings.Sources[i].PublisherPassword = credentials.PublisherPassword
				if attempt.wrongPassword {
					badSettings.Sources[i].PublisherPassword = "invalid-" + credentials.PublisherPassword
				}
			}
		}
		args := demoCommand(badSettings, attempt.target)
		args = append(args[:len(args)-1], "-t", "1", args[len(args)-1])
		probeCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		_, attemptErr := integrationCommand(probeCtx, 6*time.Second, r.paths.Root, "unauthorized publisher check", args[0], args[1:]...)
		timedOut := probeCtx.Err() != nil
		cancel()
		state, statusErr := r.sourceStatus(ctx, attempt.target)
		name := attempt.credentialSource + " credentials cannot publish to " + attempt.target
		if attempt.wrongPassword {
			name = attempt.target + ": wrong publisher password is rejected"
		}
		if err := r.check(name, attemptErr != nil && !timedOut && statusErr == nil && state.Source.Observed && !state.Source.Ready,
			map[string]any{"attempt_failed": attemptErr != nil, "attempt_timed_out": timedOut, "source_ready_after_attempt": state.Source.Ready}); err != nil {
			return err
		}
	}
	return nil
}

func (r *integrationRun) crashRelay(ctx context.Context, remoteURL string) error {
	before, err := r.waitState(ctx, "camera-01: forwarder has a test-owned process before the crash", 6*time.Second,
		func(s State) bool { return s.Workers["relay"].Running && s.Workers["relay"].PID > 1 && s.Remote.Ready }, integrationFirstSource)
	if err != nil {
		return err
	}
	pid := before.Workers["relay"].PID
	otherBefore, err := r.sourceStatus(ctx, integrationSecondSource)
	if err != nil {
		return err
	}
	if !r.processOwned(ctx, "relay/"+integrationFirstSource, pid) {
		return errors.New("refusing to stop a forwarder whose test ownership could not be verified")
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return errors.New("could not access the test-owned forwarder")
	}
	fmt.Println("Killing only camera-01's test forwarder to check recovery and camera-02 independence.")
	killedAt := time.Now()
	if err := process.Kill(); err != nil {
		return errors.New("could not interrupt the test-owned forwarder")
	}
	recoveryCtx, cancel := context.WithTimeout(ctx, integrationRecoveryTarget)
	defer cancel()
	after, err := r.waitState(recoveryCtx, "Supervisor starts a new forwarder after its process is killed", integrationRecoveryTarget,
		func(s State) bool {
			w := s.Workers["relay"]
			return w.Running && w.PID != pid && w.Starts > before.Workers["relay"].Starts && s.Source.Ready && s.Recording.Running
		}, integrationFirstSource)
	if err != nil {
		return err
	}
	if _, err := r.waitVideo(recoveryCtx, "Forwarded video decodes within 15 seconds of the process crash", remoteURL, 1280, 720, 30, integrationRecoveryTarget); err != nil {
		return err
	}
	r.report.Timings["relay_kill_to_decoded_probe"] = integrationSeconds(time.Since(killedAt))
	if err := r.check("Measured forwarder crash recovery meets the provisional target", time.Since(killedAt) <= integrationRecoveryTarget,
		map[string]any{"measured_seconds": r.report.Timings["relay_kill_to_decoded_probe"], "target_seconds": integrationRecoveryTarget.Seconds()}); err != nil {
		return err
	}
	if err := r.check("camera-01: forwarder crash does not restart its source or recorder",
		after.Workers["demo"].PID == before.Workers["demo"].PID && after.Workers["recorder"].PID == before.Workers["recorder"].PID,
		map[string]any{"previous_relay_pid": pid, "new_relay_pid": after.Workers["relay"].PID, "relay_starts_before": before.Workers["relay"].Starts, "relay_starts_after": after.Workers["relay"].Starts}); err != nil {
		return err
	}
	otherAfter, err := r.sourceStatus(ctx, integrationSecondSource)
	if err != nil {
		return err
	}
	if err := r.check("camera-02: workers and recording continue through camera-01's forwarder crash",
		integrationSameWorkers(otherBefore, otherAfter) && otherAfter.Source.Ready && otherAfter.Remote.Ready && otherAfter.Source.BytesReceived > otherBefore.Source.BytesReceived && otherAfter.Recording.Bytes >= otherBefore.Recording.Bytes,
		map[string]any{"before": integrationStateEvidence(otherBefore), "after": integrationStateEvidence(otherAfter)}); err != nil {
		return err
	}
	return r.motion(ctx, "camera-02: delivered pictures still move after camera-01's forwarder crash", integrationVideoURL(Central, integrationSecondSource))
}

type integrationClip struct {
	Path     string  `json:"path"`
	Bytes    int64   `json:"bytes"`
	SHA256   string  `json:"sha256"`
	Duration float64 `json:"duration_seconds"`
}

// Only segment-list entries denote finalized clips. Do not select the newest
// .mp4 by its filename: the recorder may still be writing it.
func integrationCompletedClips(p Paths) ([]integrationClip, error) {
	lists, err := filepath.Glob(filepath.Join(p.Recordings, "*", "segments.csv"))
	if err != nil {
		return nil, errors.New("could not list test recording manifests")
	}
	root, err := filepath.EvalSymlinks(p.Recordings)
	if err != nil {
		return nil, errors.New("could not resolve the test recording directory")
	}
	known := map[string]integrationClip{}
	for _, list := range lists {
		file, err := os.Open(list)
		if err != nil {
			return nil, errors.New("could not read a completed-recording list")
		}
		reader := csv.NewReader(io.LimitReader(file, 2<<20))
		reader.FieldsPerRecord = -1
		for {
			row, err := reader.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				file.Close()
				return nil, errors.New("a completed-recording list is invalid")
			}
			if len(row) != 3 {
				continue
			}
			name := row[0]
			if !filepath.IsAbs(name) {
				name = filepath.Join(filepath.Dir(list), name)
			}
			name, err = filepath.EvalSymlinks(name)
			if err != nil {
				continue
			}
			relative, err := filepath.Rel(root, name)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.Ext(relative) != ".mp4" {
				continue
			}
			begin, firstErr := strconv.ParseFloat(row[1], 64)
			end, secondErr := strconv.ParseFloat(row[2], 64)
			if firstErr != nil || secondErr != nil || math.IsNaN(begin) || math.IsNaN(end) || math.IsInf(begin, 0) || math.IsInf(end, 0) || end <= begin {
				continue
			}
			media, err := os.Open(name)
			if err != nil {
				continue
			}
			info, err := media.Stat()
			if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxRecordingBytes {
				media.Close()
				continue
			}
			hash := sha256.New()
			size, err := io.Copy(hash, io.LimitReader(media, MaxRecordingBytes+1))
			media.Close()
			if err != nil || size != info.Size() {
				file.Close()
				return nil, errors.New("a completed test clip could not be read consistently")
			}
			known[relative] = integrationClip{Path: relative, Bytes: size, SHA256: hex.EncodeToString(hash.Sum(nil)), Duration: end - begin}
		}
		file.Close()
	}
	clips := make([]integrationClip, 0, len(known))
	for _, clip := range known {
		clips = append(clips, clip)
	}
	sort.Slice(clips, func(i, j int) bool { return clips[i].Path < clips[j].Path })
	return clips, nil
}

func (r *integrationRun) recordingPersistence(ctx context.Context) error {
	fmt.Println("Stopping the controller in order, then decoding both sources' recordings and checking restart persistence.")
	beforeStop, err := r.status(ctx)
	if err != nil {
		return err
	}
	// This is the orderly controller-shutdown contract: its recorder workers
	// stop while source publishers are still available. Arbitrary publisher
	// disappearance is a distinct diagnostic with retained failure evidence.
	if _, err := r.command(ctx, 30*time.Second, "stop"); err != nil {
		return err
	}
	state, err := r.waitState(ctx, "Controller closes both recording branches before stopping its sources", 10*time.Second,
		func(s State) bool {
			if s.Running || s.Recording.Running || len(s.Sources) < len(integrationSourceIDs) {
				return false
			}
			for _, id := range integrationSourceIDs {
				source := integrationSelectSource(s, id)
				if source.Recording.Running || source.Workers["demo"].Running || source.Workers["recorder"].Running {
					return false
				}
			}
			return true
		})
	if err != nil {
		return err
	}
	if err := r.check("Orderly shutdown retains both sources' completed recording totals",
		state.Recording.Segments >= beforeStop.Recording.Segments && state.Recording.Bytes >= beforeStop.Recording.Bytes,
		map[string]any{"before_stop": integrationStateEvidence(beforeStop), "after_stop": integrationStateEvidence(state)}); err != nil {
		return err
	}
	clips, err := integrationCompletedClips(r.paths)
	if err != nil {
		return err
	}
	if err := r.check("Recordings contain finalized test clips from concurrent capture", len(clips) >= 4, map[string]any{"completed_clips": len(clips)}); err != nil {
		return err
	}
	state, err = r.waitState(ctx, "Completed recording counts include both sources' finalized clips", 10*time.Second,
		func(s State) bool { return s.Recording.Segments >= len(clips) && !s.Recording.Running })
	if err != nil {
		return err
	}
	// Source identity comes from the durable catalog. A final partial clip can
	// validly be too short for the live-video probe's three-frame requirement.
	catalog, err := OpenCatalog(r.paths)
	if err != nil {
		return errors.New("could not open the test recording catalog for source identity checks")
	}
	catalogCtx, cancelCatalog := context.WithTimeout(ctx, 10*time.Second)
	bySource := map[string][]int{}
	for index, clip := range clips {
		segment, err := catalog.GetSegment(catalogCtx, clip.Path)
		if err != nil {
			catalog.Close()
			cancelCatalog()
			return errors.New("a finalized clip has no saved source identity")
		}
		if clip.Duration >= 1 {
			bySource[segment.SourceID] = append(bySource[segment.SourceID], index)
		}
	}
	catalog.Close()
	cancelCatalog()
	var candidates []int
	for _, id := range integrationSourceIDs {
		indices := bySource[id]
		if err := r.check(id+": SQLite identifies at least two finalized clips for this source", len(indices) >= 2, map[string]any{"eligible_clips": len(indices)}); err != nil {
			return err
		}
		candidates = append(candidates, indices[0], indices[len(indices)-1])
	}
	for _, index := range candidates {
		clip := clips[index]
		name := filepath.Join(r.paths.Recordings, clip.Path)
		media, err := r.probe(ctx, name)
		if err != nil {
			return r.check("Finalized test recording decodes", false, map[string]any{"clip": clip, "detail": err.Error()})
		}
		if err := r.check("Finalized test recording has decoded 1280 × 720 H.264 frames", media.Codec == "h264" && media.Width == 1280 && media.Height == 720,
			map[string]any{"clip": clip, "media": media}); err != nil {
			return err
		}
	}
	// Decode every completed normal-shutdown clip, including short final files.
	// A readable header or a checksum alone cannot detect damaged video frames.
	for _, clip := range clips {
		_, err := integrationCommand(ctx, 15*time.Second, r.paths.Root, "saved-clip full decode", r.settings.FFmpeg,
			"-hide_banner", "-loglevel", "error", "-nostdin", "-xerror", "-i", filepath.Join(r.paths.Recordings, clip.Path), "-map", "0:v:0", "-an", "-f", "null", "-")
		if err != nil {
			return r.check("Every completed normal-shutdown clip decodes fully", false, map[string]any{"clip": clip, "detail": err.Error()})
		}
	}
	if err := r.check("Every completed normal-shutdown clip decodes fully", true, map[string]any{"decoded_clips": len(clips)}); err != nil {
		return err
	}
	if err := r.catalogMatches(ctx, "SQLite contains both sources' completed clip identities after orderly shutdown", clips); err != nil {
		return err
	}
	stopped, err := r.status(ctx)
	if err != nil {
		return err
	}
	if err := r.check("Recorded metadata remains readable while test services are stopped",
		!stopped.Running && stopped.Recording.Segments >= state.Recording.Segments && stopped.Recording.Bytes >= state.Recording.Bytes,
		map[string]any{"before_stop": integrationStateEvidence(state), "after_stop": integrationStateEvidence(stopped)}); err != nil {
		return err
	}
	if err := r.catalogMatches(ctx, "A newly opened SQLite connection reads the same clips after shutdown", clips); err != nil {
		return err
	}
	if _, err := r.command(ctx, 20*time.Second, "start"); err != nil {
		return err
	}
	restarted, err := r.waitState(ctx, "Restart restores both sources' recording counts without live camera input", 10*time.Second,
		func(s State) bool {
			if !s.Running || len(s.Sources) < len(integrationSourceIDs) || s.Recording.Segments < state.Recording.Segments || s.Recording.Bytes < state.Recording.Bytes {
				return false
			}
			for _, id := range integrationSourceIDs {
				old, current := integrationSelectSource(state, id), integrationSelectSource(s, id)
				if current.Source.Ready || current.DemoEnabled || current.Recording.Segments < old.Recording.Segments || current.Recording.Bytes < old.Recording.Bytes {
					return false
				}
			}
			return true
		})
	if err != nil {
		return err
	}
	if err := r.catalogMatches(ctx, "SQLite clip checksums remain correct after supervisor restart", clips); err != nil {
		return err
	}
	after, err := integrationCompletedClips(r.paths)
	if err != nil {
		return err
	}
	unchanged := len(after) == len(clips)
	if unchanged {
		for i := range clips {
			if clips[i] != after[i] {
				unchanged = false
				break
			}
		}
	}
	return r.check("All finalized recording files retain their size and checksum across stop and restart", unchanged,
		map[string]any{"files": after, "restored_state": integrationStateEvidence(restarted)})
}

func (r *integrationRun) catalogMatches(ctx context.Context, name string, clips []integrationClip) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	catalog, err := OpenCatalog(r.paths)
	if err != nil {
		return r.check(name, false, map[string]any{"detail": "Could not open the test recording catalog."})
	}
	defer catalog.Close()
	summary, err := catalog.Summary(ctx)
	if err != nil {
		return r.check(name, false, map[string]any{"detail": "Could not read the test recording catalog summary."})
	}
	verified := 0
	verifiedBySource := map[string]int{}
	bytesBySource := map[string]int64{}
	for _, clip := range clips {
		segment, err := catalog.GetSegment(ctx, clip.Path)
		knownSource := false
		for _, id := range integrationSourceIDs {
			if segment.SourceID == id {
				knownSource = true
			}
		}
		if err != nil || !knownSource || segment.Path != clip.Path || segment.Bytes != clip.Bytes || segment.SHA256 != clip.SHA256 || math.Abs(segment.Duration-clip.Duration) > 0.001 {
			return r.check(name, false, map[string]any{"detail": "A completed file does not match its saved catalog entry.", "file": clip.Path, "verified_files": verified})
		}
		verified++
		verifiedBySource[segment.SourceID]++
		bytesBySource[segment.SourceID] += clip.Bytes
	}
	sourceSummaries, err := catalog.SummariesBySource(ctx)
	if err != nil {
		return r.check(name, false, map[string]any{"detail": "Could not read per-source recording totals."})
	}
	for _, id := range integrationSourceIDs {
		if verifiedBySource[id] < 1 || sourceSummaries[id].Segments != verifiedBySource[id] || sourceSummaries[id].Bytes != bytesBySource[id] {
			return r.check(name, false, map[string]any{"detail": "Per-source totals do not match that source's completed clips.", "source_id": id, "verified_files": verifiedBySource[id]})
		}
	}
	return r.check(name, summary.Segments == len(clips), map[string]any{"verified_files": verified, "verified_by_source": verifiedBySource, "catalog_segments": summary.Segments, "catalog_bytes": summary.Bytes})
}

func (r *integrationRun) processOwned(ctx context.Context, name string, pid int) bool {
	if pid <= 1 || pid == os.Getpid() {
		return false
	}
	data, err := integrationCommand(ctx, 2*time.Second, r.paths.Root, "test process identity check", "ps", "-p", strconv.Itoa(pid), "-o", "args=")
	if err != nil {
		return false
	}
	command := string(data)
	if decoded, err := url.QueryUnescape(command); err == nil {
		command = decoded
	}
	parts := strings.SplitN(name, "/", 2)
	role := parts[0]
	sourceID := integrationFirstSource
	if len(parts) == 2 {
		sourceID = parts[1]
	}
	var source SourceConfig
	for _, candidate := range GetSources(r.settings) {
		if candidate.ID == sourceID {
			source = candidate
		}
	}
	switch role {
	case "field", "central":
		return strings.Contains(command, filepath.Join(r.paths.Local, role+".yml"))
	case "demo":
		return source.PublishPassphrase != "" && strings.Contains(command, source.PublishPassphrase) && strings.Contains(command, "testsrc2=") && strings.Contains(command, "publish:"+sourceID+":")
	case "relay":
		if r.localRelayProcessOwned(command, sourceID) {
			return true
		}
		return r.settings.CentralPassphrase != "" && strings.Contains(command, r.settings.CentralPassphrase) && strings.Contains(command, "srt://127.0.0.1:") && strings.Contains(command, "publish:"+sourceID+":")
	case "recorder":
		return strings.Contains(command, r.paths.Recordings+string(os.PathSeparator)+sourceID+"-") && strings.Contains(command, "segment-")
	case "supervisor":
		var identity struct {
			PID   int    `json:"pid"`
			Token string `json:"token"`
		}
		if readJSON(filepath.Join(r.paths.Local, "supervisor.json"), &identity) != nil || identity.PID != pid || identity.Token == "" {
			return false
		}
		return strings.Contains(command, r.paths.Root) && strings.Contains(command, "_supervise "+identity.Token)
	}
	return false
}

func (r *integrationRun) cleanup() {
	if !r.startAttempted {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	_, _ = r.status(ctx)
	if _, err := r.command(ctx, 30*time.Second, "stop"); err != nil {
		r.report.CleanupErrors = append(r.report.CleanupErrors, "The normal stop command failed; checking only test-owned processes.")
	}
	var identity struct {
		PID int `json:"pid"`
	}
	if readJSON(filepath.Join(r.paths.Local, "supervisor.json"), &identity) == nil {
		r.owned["supervisor"] = identity.PID
	}
	// Stop the supervisor first only if normal shutdown failed. Identity checks
	// include a unique test root or secret, not merely a possibly reused PID.
	names := []string{"supervisor"}
	for _, source := range GetSources(r.settings) {
		for _, role := range []string{"demo", "relay", "recorder"} {
			names = append(names, role+"/"+source.ID)
		}
	}
	names = append(names, "central", "field")
	for _, name := range names {
		pid := r.owned[name]
		if !r.processOwned(ctx, name, pid) {
			continue
		}
		process, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		_ = process.Signal(syscall.SIGTERM)
		_ = integrationSleep(ctx, 500*time.Millisecond)
		if r.processOwned(ctx, name, pid) {
			_ = process.Kill()
			_ = integrationSleep(ctx, 200*time.Millisecond)
		}
		if r.processOwned(ctx, name, pid) {
			r.report.CleanupErrors = append(r.report.CleanupErrors, "A test-owned "+name+" process remains after cleanup.")
		}
	}
	if r.paths.Running() {
		r.report.CleanupErrors = append(r.report.CleanupErrors, "The isolated test supervisor still holds its running lock.")
	}
}
