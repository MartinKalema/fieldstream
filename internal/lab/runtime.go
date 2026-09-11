package lab

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const MaxRecordingBytes int64 = 2_000_000_000
const MinFreeBytes uint64 = 1_000_000_000

type Control struct {
	RelayEnabled bool   `json:"relay_enabled"`
	Profile      string `json:"profile"`
	DemoEnabled  bool   `json:"demo_enabled"`
	RelayWaitMS  int    `json:"relay_wait_ms"`
	RelayLink    string `json:"relay_link"`
}

func defaultControl() Control {
	return Control{RelayEnabled: true, Profile: "copy", RelayWaitMS: 300, RelayLink: "srt"}
}

func (c Control) validate() error {
	if c.Profile != "copy" && c.Profile != "small" {
		return errors.New("picture profile must be copy or small")
	}
	if c.RelayWaitMS != 120 && c.RelayWaitMS != 300 {
		return errors.New("forwarding recovery wait must be 120 or 300 milliseconds")
	}
	if c.RelayLink != "srt" && c.RelayLink != "local" {
		return errors.New("forwarding link must be local or srt")
	}
	return nil
}

func (c Control) relayKey() string {
	if c.RelayLink == "local" {
		return c.Profile + "/local"
	}
	return fmt.Sprintf("%s/srt/%dms", c.Profile, c.RelayWaitMS)
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

type ControlSet struct {
	Sources map[string]Control `json:"sources"`
}

func selectedSource(s Settings, requested ...string) (SourceConfig, error) {
	sources := GetSources(s)
	if len(sources) == 0 {
		return SourceConfig{}, errors.New("no video sources are configured")
	}
	if len(requested) == 0 || requested[0] == "" {
		return sources[0], nil
	}
	for _, source := range sources {
		if source.ID == requested[0] {
			return source, nil
		}
	}
	return SourceConfig{}, fmt.Errorf("unknown source %q; use ./lab source list", requested[0])
}

func (p Paths) loadControls(settings Settings) (ControlSet, error) {
	result := ControlSet{Sources: map[string]Control{}}
	sources := GetSources(settings)
	for _, source := range sources {
		result.Sources[source.ID] = defaultControl()
	}
	data, err := os.ReadFile(filepath.Join(p.Local, "control.json"))
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		return result, errors.New("control settings are invalid")
	}
	if sourceData, ok := raw["sources"]; ok {
		var saved map[string]Control
		if json.Unmarshal(sourceData, &saved) != nil || saved == nil {
			return result, errors.New("source control settings are invalid")
		}
		for _, source := range sources {
			if c, ok := saved[source.ID]; ok {
				result.Sources[source.ID] = c
			}
		}
	} else if len(sources) > 0 {
		legacy := defaultControl()
		if json.Unmarshal(data, &legacy) != nil {
			return result, errors.New("control settings are invalid")
		}
		result.Sources[sources[0].ID] = legacy
	}
	for id, c := range result.Sources {
		if c.RelayLink == "" {
			c.RelayLink = "srt" // Preserve existing transport on upgrade.
			result.Sources[id] = c
		}
		// Older saved controls did not contain a wait setting.
		if c.RelayWaitMS == 0 {
			c.RelayWaitMS = 300
			result.Sources[id] = c
		}
		if err := c.validate(); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (p Paths) Control() Control {
	s, err := p.LoadSettings()
	if err != nil {
		return defaultControl()
	}
	controls, err := p.loadControls(s)
	if err != nil {
		return defaultControl()
	}
	source, err := selectedSource(s)
	if err != nil {
		return defaultControl()
	}
	return controls.Sources[source.ID]
}

func fileLock(path string, nonblocking bool) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	mode := syscall.LOCK_EX
	if nonblocking {
		mode |= syscall.LOCK_NB
	}
	if err = syscall.Flock(int(f.Fd()), mode); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (p Paths) ChangeControl(change func(*Control)) error { return p.ChangeSourceControl("", change) }

func (p Paths) ChangeSourceControl(sourceID string, change func(*Control)) error {
	settings, err := p.LoadSettings()
	if err != nil {
		return err
	}
	source, err := selectedSource(settings, sourceID)
	if err != nil {
		return err
	}
	f, err := fileLock(filepath.Join(p.Local, "control.lock"), false)
	if err != nil {
		return err
	}
	defer f.Close()
	controls, err := p.loadControls(settings)
	if err != nil {
		return err
	}
	control := controls.Sources[source.ID]
	change(&control)
	if err := control.validate(); err != nil {
		return err
	}
	controls.Sources[source.ID] = control
	return AtomicJSON(filepath.Join(p.Local, "control.json"), controls)
}

func (p Paths) Running() bool {
	f, err := fileLock(filepath.Join(p.Local, "supervisor.lock"), true)
	if err == nil {
		f.Close()
		return false
	}
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

type SourceState struct {
	Observed         bool     `json:"observed"`
	ObservationError string   `json:"observation_error,omitempty"`
	Ready            bool     `json:"ready"`
	BytesReceived    uint64   `json:"bytes_received"`
	PublisherID      string   `json:"publisher_id,omitempty"`
	Tracks           []string `json:"tracks"`
	Readers          int      `json:"readers"`
}

var apiClient = &http.Client{Timeout: 700 * time.Millisecond}

func sourceState(ports Ports, requested ...string) SourceState {
	id := "camera-01"
	if len(requested) > 0 && requested[0] != "" {
		id = requested[0]
	}
	empty := SourceState{Tracks: []string{}, ObservationError: "Video status is unavailable."}
	response, err := apiClient.Get(fmt.Sprintf("http://127.0.0.1:%d/v3/paths/get/%s", ports.API, url.PathEscape(id)))
	if err != nil {
		return empty
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		empty.Observed = true
		empty.ObservationError = ""
		return empty
	}
	if response.StatusCode != http.StatusOK {
		return empty
	}
	var d struct {
		Ready         *bool  `json:"ready"`
		Online        *bool  `json:"online"`
		Available     *bool  `json:"available"`
		BytesReceived uint64 `json:"bytesReceived"`
		Source        *struct {
			ID string `json:"id"`
		} `json:"source"`
		Tracks  []string          `json:"tracks"`
		Readers []json.RawMessage `json:"readers"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&d) != nil {
		return empty
	}
	ready := false
	if d.Ready != nil {
		ready = *d.Ready
	} else if d.Online != nil {
		ready = *d.Online
	} else if d.Available != nil {
		ready = *d.Available
	} else {
		return empty
	}
	if d.Tracks == nil {
		d.Tracks = []string{}
	}
	publisherID := ""
	if d.Source != nil {
		publisherID = d.Source.ID
	}
	return SourceState{Ready: ready, Observed: true, BytesReceived: d.BytesReceived, PublisherID: publisherID, Tracks: d.Tracks, Readers: len(d.Readers)}
}

// Failure to observe the input is not evidence that media itself has stopped.
func mediaWanted(source SourceState, previouslyReady bool) bool {
	if !source.Observed {
		return previouslyReady
	}
	return source.Ready
}

type WorkerState struct {
	PID      int    `json:"pid"`
	Running  bool   `json:"running"`
	Starts   int    `json:"starts"`
	LastExit string `json:"last_exit,omitempty"`
	LogError string `json:"log_error,omitempty"`
}
type RecordingState struct {
	Running       bool     `json:"running"`
	Segments      int      `json:"segments"`
	Bytes         int64    `json:"bytes"`
	BlockedReason *string  `json:"blocked_reason"`
	Warnings      []string `json:"warnings,omitempty"`
}
type State struct {
	Running bool `json:"running"`
	Control
	Source          SourceState            `json:"source"`
	Remote          SourceState            `json:"remote"`
	Workers         map[string]WorkerState `json:"workers"`
	Recording       RecordingState         `json:"recording"`
	Archive         ArchiveState           `json:"archive"`
	SupervisorPID   int                    `json:"supervisor_pid"`
	SupervisorToken string                 `json:"supervisor_token"`
	UpdatedAt       time.Time              `json:"updated_at"`
	MeasurementNote string                 `json:"measurement_note"`
	HealthNote      string                 `json:"health_note,omitempty"`
	Sources         map[string]StreamState `json:"sources"`
}

type StreamState struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Control
	Source    SourceState            `json:"source"`
	Remote    SourceState            `json:"remote"`
	Recording RecordingState         `json:"recording"`
	Workers   map[string]WorkerState `json:"workers"`
}

func (p Paths) Status() State {
	running := p.Running()
	state := State{Workers: map[string]WorkerState{}, Sources: map[string]StreamState{}}
	_ = readJSON(filepath.Join(p.Local, "state.json"), &state)
	if state.Sources == nil {
		state.Sources = map[string]StreamState{}
	}
	if state.Workers == nil {
		state.Workers = map[string]WorkerState{}
	}
	state.Running = running
	settings, settingsErr := p.LoadSettings()
	if settingsErr != nil {
		state.HealthNote = settingsErr.Error()
	} else {
		controls, err := p.loadControls(settings)
		if err != nil {
			state.HealthNote = err.Error()
		}
		observations := map[string]streamObservation{}
		if running {
			observations = observeSources(settings)
		}
		for _, source := range GetSources(settings) {
			stream := state.Sources[source.ID]
			stream.ID, stream.Label = source.ID, source.Label
			if err == nil {
				stream.Control = controls.Sources[source.ID]
			}
			if running {
				stream.Source, stream.Remote = observations[source.ID].Source, observations[source.ID].Remote
			} else {
				stream.Source, stream.Remote = SourceState{Tracks: []string{}}, SourceState{Tracks: []string{}}
				stream.Recording.Running = false
				for name, worker := range stream.Workers {
					worker.Running = false
					worker.PID = 0
					stream.Workers[name] = worker
				}
			}
			state.Sources[source.ID] = stream
		}
		if primary, e := selectedSource(settings); e == nil {
			stream := state.Sources[primary.ID]
			state.Control, state.Source, state.Remote = stream.Control, stream.Source, stream.Remote
		}
	}
	if running {
		if state.UpdatedAt.IsZero() || time.Since(state.UpdatedAt) > 10*time.Second {
			state.HealthNote = "The controller's saved status is old; recording and worker details may be stale."
		}
	} else {
		state.Recording.Running = false
		state.Archive.Running, state.Archive.Uploading = false, false
		for name, worker := range state.Workers {
			worker.Running = false
			worker.PID = 0
			state.Workers[name] = worker
		}
		if settingsErr == nil && mediaServicesPresent(settings) {
			state.HealthNote = "The controller is stopped, but lab ports are still occupied. Some services may remain from an interrupted run."
		}
	}
	state.MeasurementNote = "Stream activity is not a measurement of camera-to-screen delay."
	return state
}

type worker struct {
	name     string
	cmd      *exec.Cmd
	done     chan error
	key      string
	next     time.Time
	starts   int
	lastExit string
	log      *rollingLog
	progress *relayProgress
}

func (w *worker) poll() {
	if w.cmd == nil {
		return
	}
	select {
	case err := <-w.done:
		w.lastExit = "finished"
		if err != nil {
			w.lastExit = err.Error()
		}
		w.cmd = nil
		w.progress = nil
		w.next = time.Now().Add(2 * time.Second)
	default:
	}
}

func (w *worker) stop() {
	w.poll()
	if w.cmd == nil {
		return
	}
	_ = w.cmd.Process.Signal(os.Interrupt)
	select {
	case <-w.done:
	case <-time.After(3 * time.Second):
		_ = w.cmd.Process.Kill()
		<-w.done
	}
	w.cmd = nil
	w.progress = nil
	w.key = ""
	w.next = time.Time{}
}

func (w *worker) ensure(p Paths, wanted bool, key string, build func() ([]string, error)) error {
	w.poll()
	if w.cmd != nil && (!wanted || w.key != key) {
		w.stop()
	}
	if !wanted || w.cmd != nil || time.Now().Before(w.next) {
		return nil
	}
	args, err := build()
	if err != nil {
		w.next = time.Now().Add(3 * time.Second)
		return err
	}
	log := mediaLog(filepath.Join(p.Local, "logs", strings.ReplaceAll(w.name, "/", "-")+".log"))
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = p.Root
	cmd.Stdout = log
	cmd.Stderr = log
	var progress *relayProgress
	if strings.HasPrefix(w.name, "relay/") && strings.HasSuffix(key, "/local") {
		progress = newRelayProgress(log, time.Now)
		cmd.Stdout = progress
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.WaitDelay = 2 * time.Second
	if err = cmd.Start(); err != nil {
		log.Close()
		w.next = time.Now().Add(3 * time.Second)
		return err
	}
	w.cmd = cmd
	w.log = log
	w.progress = progress
	w.done = make(chan error, 1)
	w.key = key
	w.starts++
	done := w.done
	go func() { err := cmd.Wait(); log.Close(); done <- err }()
	return nil
}

func (w *worker) state() WorkerState {
	w.poll()
	s := WorkerState{Starts: w.starts, LastExit: w.lastExit}
	if w.log != nil {
		s.LogError = w.log.errorText()
	}
	if w.cmd != nil {
		s.PID = w.cmd.Process.Pid
		s.Running = true
	}
	return s
}

type Segment struct {
	SourceID string  `json:"source_id"`
	Path     string  `json:"path"`
	Session  string  `json:"session"`
	Bytes    int64   `json:"bytes"`
	SHA256   string  `json:"sha256"`
	Duration float64 `json:"duration_seconds"`
}

type recordingMonitor struct {
	mu           sync.RWMutex
	state        RecordingState
	archive      ArchiveState
	done         chan struct{}
	sourceStates map[string]RecordingState
}

func (m *recordingMonitor) snapshot() RecordingState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

func freeBytes(path string) (uint64, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return 0, err
	}
	return fs.Bavail * uint64(fs.Bsize), nil
}

func srtURL(s Settings, central bool, requested ...string) string {
	return srtURLWithWait(s, central, 300, requested...)
}

func srtURLWithWait(s Settings, central bool, waitMS int, requested ...string) string {
	source, err := selectedSource(s, requested...)
	if err != nil {
		return ""
	}
	host, port, user, password, phrase := s.Host, Field.SRT, source.PublisherUser, source.PublisherPassword, source.PublishPassphrase
	if central {
		host, port, user, password, phrase = "127.0.0.1", Central.SRT, s.RelayUser, s.RelayPassword, s.CentralPassphrase
	}
	query := url.Values{"streamid": {fmt.Sprintf("publish:%s:%s:%s", source.ID, user, password)}, "passphrase": {phrase}, "pbkeylen": {"16"}, "pkt_size": {"1316"}, "latency": {strconv.Itoa(waitMS * 1000)}, "connect_timeout": {"2000"}, "timeout": {"3000000"}}
	return fmt.Sprintf("srt://%s:%d?%s", host, port, query.Encode())
}

func ffmpegBase(s Settings) []string {
	return []string{s.FFmpeg, "-hide_banner", "-loglevel", "warning", "-nostdin"}
}

func inputArgs(requested ...string) []string {
	id := "camera-01"
	if len(requested) > 0 && requested[0] != "" {
		id = requested[0]
	}
	return []string{"-rtsp_transport", "tcp", "-timeout", "3000000", "-i", fmt.Sprintf("rtsp://127.0.0.1:%d/%s", Field.RTSP, id), "-map", "0:v:0", "-an"}
}

func demoCommand(s Settings, requested ...string) []string {
	return append(ffmpegBase(s), "-re", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30", "-an", "-c:v", "libx264", "-threads", "2", "-preset", "ultrafast", "-tune", "zerolatency", "-profile:v", "baseline", "-pix_fmt", "yuv420p", "-bf", "0", "-g", "30", "-b:v", "2000k", "-maxrate", "2000k", "-bufsize", "1000k", "-f", "mpegts", srtURL(s, false, requested...))
}

func relayCommand(s Settings, control Control, requested ...string) []string {
	args := append(ffmpegBase(s), inputArgs(requested...)...)
	if control.Profile == "small" {
		args = append(args, "-vf", "scale=640:360:force_original_aspect_ratio=decrease:force_divisible_by=2,pad=640:360:(ow-iw)/2:(oh-ih)/2,fps=20", "-c:v", "libx264", "-threads", "2", "-preset", "veryfast", "-tune", "zerolatency", "-profile:v", "baseline", "-pix_fmt", "yuv420p", "-bf", "0", "-g", "20", "-b:v", "650k", "-maxrate", "650k", "-bufsize", "325k")
	} else {
		args = append(args, "-c:v", "copy")
	}
	if control.RelayLink == "local" {
		source, err := selectedSource(s, requested...)
		if err != nil {
			return nil
		}
		// This mode is restricted to the same machine. RTSP is not encrypted;
		// the receiver listener and publishing account are loopback-only.
		target := url.URL{Scheme: "rtsp", User: url.UserPassword(s.RelayUser, s.RelayPassword), Host: fmt.Sprintf("127.0.0.1:%d", Central.RTSP), Path: "/" + source.ID}
		return append(args, "-progress", "pipe:1", "-stats_period", "1", "-f", "rtsp", "-rtsp_transport", "tcp", "-timeout", "3000000", target.String())
	}
	return append(args, "-f", "mpegts", srtURLWithWait(s, true, control.RelayWaitMS, requested...))
}

func identifier() string {
	var data [12]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data[:])
}

func recordCommand(p Paths, s Settings, requested ...string) ([]string, error) {
	source, err := selectedSource(s, requested...)
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(p.Recordings, source.ID+"-"+time.Now().Format("20060102-150405")+"-"+identifier())
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	if err := AtomicJSON(filepath.Join(directory, ".source.json"), struct {
		SourceID string `json:"source_id"`
	}{source.ID}); err != nil {
		return nil, err
	}
	args := append(ffmpegBase(s), inputArgs(source.ID)...)
	return append(args, "-c:v", "copy", "-f", "segment", "-segment_time", "5", "-segment_format", "mp4", "-reset_timestamps", "1", "-segment_format_options", "movflags=+faststart", "-segment_list_type", "csv", "-segment_list", filepath.Join(directory, "segments.csv"), filepath.Join(directory, "segment-%06d.mp4")), nil
}

func (p Paths) Supervise(token string) error {
	settings, err := p.LoadSettings()
	if err != nil {
		return err
	}
	lock, err := fileLock(filepath.Join(p.Local, "supervisor.lock"), true)
	if err != nil {
		return err
	}
	defer lock.Close()
	syscall.Umask(0077)
	if err = os.MkdirAll(filepath.Join(p.Local, "logs"), 0700); err != nil {
		return err
	}
	log := mediaLog(filepath.Join(p.Local, "logs", "supervisor.log"))
	defer log.Close()
	identity := struct {
		PID   int    `json:"pid"`
		Token string `json:"token"`
	}{os.Getpid(), token}
	if err = AtomicJSON(filepath.Join(p.Local, "supervisor.json"), identity); err != nil {
		return err
	}
	sources := GetSources(settings)
	controls, err := p.loadControls(settings)
	if err != nil {
		return err
	}
	workers := map[string]*worker{"field": {name: "field"}, "central": {name: "central"}}
	for _, source := range sources {
		for _, role := range []string{"demo", "relay", "recorder"} {
			name := role + "/" + source.ID
			workers[name] = &worker{name: name}
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	monitorCtx, stopMonitor := context.WithCancel(context.Background())
	checking := "Checking recording storage."
	monitor := &recordingMonitor{state: RecordingState{BlockedReason: &checking}, sourceStates: map[string]RecordingState{}, done: make(chan struct{})}
	go monitor.run(monitorCtx, p)
	defer func() {
		// Stop each source's output workers together, before stopping receivers.
		var stopping sync.WaitGroup
		for _, source := range sources {
			for _, role := range []string{"relay", "recorder"} {
				w := workers[role+"/"+source.ID]
				stopping.Add(1)
				go func() { defer stopping.Done(); w.stop() }()
			}
		}
		stopping.Wait()
		for _, source := range sources {
			workers["demo/"+source.ID].stop()
		}
		workers["central"].stop()
		workers["field"].stop()
		stopMonitor()
		<-monitor.done
		final := State{Recording: monitor.snapshot(), Archive: monitor.snapshotArchive(), Workers: map[string]WorkerState{}, Sources: map[string]StreamState{}, UpdatedAt: time.Now()}
		final.Archive.Running, final.Archive.Uploading = false, false
		for _, source := range sources {
			final.Sources[source.ID] = StreamState{ID: source.ID, Label: source.Label, Control: controls.Sources[source.ID], Recording: monitor.snapshotSource(source.ID), Workers: map[string]WorkerState{}}
		}
		if catalog, e := OpenCatalog(p); e == nil {
			finalCtx, finish := context.WithTimeout(context.Background(), 5*time.Second)
			known := map[string]Segment{}
			for offset := 0; ; offset += 500 {
				entries, e := catalog.ListSegments(finalCtx, 500, offset)
				if e != nil {
					break
				}
				for _, entry := range entries {
					if !entry.Missing {
						if _, e := os.Stat(filepath.Join(p.Recordings, entry.Path)); e == nil {
							known[entry.Path] = entry.Segment
						}
					}
				}
				if len(entries) < 500 {
					break
				}
			}
			if rec, e := scanRecordings(finalCtx, p, known, catalog); e == nil {
				final.Recording = rec
			}
			if summary, e := catalog.Summary(finalCtx); e == nil {
				final.Archive.Summary = summary
			}
			if summaries, e := catalog.SummariesBySource(finalCtx); e == nil {
				for id, summary := range summaries {
					stream := final.Sources[id]
					stream.Recording = RecordingState{Segments: summary.LocalSegments, Bytes: summary.LocalBytes}
					final.Sources[id] = stream
				}
			}
			finish()
			catalog.Close()
		}
		final.Recording.Running = false
		if len(sources) > 0 {
			final.Control = controls.Sources[sources[0].ID]
		}
		_ = AtomicJSON(filepath.Join(p.Local, "state.json"), final)
	}()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	sourceExpected := map[string]bool{}
	for {
		healthNote := ""
		if updated, e := p.loadControls(settings); e == nil {
			controls = updated
		} else {
			healthNote = e.Error() + "; keeping the last valid controls."
		}
		for _, name := range []string{"field", "central"} {
			n := name
			if e := workers[n].ensure(p, true, n, func() ([]string, error) { return []string{p.BinaryPath(), filepath.Join(p.Local, n+".yml")}, nil }); e != nil {
				fmt.Fprintln(log, n+":", e)
			}
		}
		for _, source := range sources {
			id := source.ID
			if e := workers["demo/"+id].ensure(p, controls.Sources[id].DemoEnabled, id, func() ([]string, error) { return demoCommand(settings, id), nil }); e != nil {
				fmt.Fprintln(log, id, "test source:", e)
			}
		}
		observations := observeSources(settings)
		state := State{Running: true, Recording: monitor.snapshot(), Archive: monitor.snapshotArchive(), Workers: map[string]WorkerState{}, Sources: map[string]StreamState{}, SupervisorPID: os.Getpid(), SupervisorToken: token, UpdatedAt: time.Now(), HealthNote: healthNote}
		for _, source := range sources {
			id := source.ID
			control := controls.Sources[id]
			observation := observations[id]
			wantMedia := mediaWanted(observation.Source, sourceExpected[id])
			sourceExpected[id] = wantMedia
			relayTag := control.relayKey()
			relay := workers["relay/"+id]
			relay.poll()
			if wantMedia && control.RelayEnabled && control.RelayLink == "local" && relay.cmd != nil && relay.key == relayTag && relay.progress != nil {
				if reason := relay.progress.reason(observation.Source, time.Now()); reason != "" {
					relay.stop()
					relay.lastExit = reason
					fmt.Fprintln(log, id, reason)
				}
			}
			if e := relay.ensure(p, wantMedia && control.RelayEnabled, relayTag, func() ([]string, error) { return relayCommand(settings, control, id), nil }); e != nil {
				fmt.Fprintln(log, id, "forwarding:", e)
			}
			rec := monitor.snapshotSource(id)
			if e := workers["recorder/"+id].ensure(p, wantMedia && rec.BlockedReason == nil, id, func() ([]string, error) { return recordCommand(p, settings, id) }); e != nil {
				reason := "Recording could not start: " + e.Error()
				rec.BlockedReason = &reason
			}
			stream := StreamState{ID: id, Label: source.Label, Control: control, Source: observation.Source, Remote: observation.Remote, Recording: rec, Workers: map[string]WorkerState{}}
			for _, role := range []string{"demo", "relay", "recorder"} {
				stream.Workers[role] = workers[role+"/"+id].state()
			}
			stream.Recording.Running = stream.Workers["recorder"].Running
			if stream.Recording.Running {
				state.Recording.Running = true
			}
			state.Sources[id] = stream
		}
		for name, w := range workers {
			state.Workers[name] = w.state()
		}
		if len(sources) > 0 {
			primary := state.Sources[sources[0].ID]
			state.Control, state.Source, state.Remote = primary.Control, primary.Source, primary.Remote
			for role, w := range primary.Workers {
				state.Workers[role] = w
			}
		}
		if e := AtomicJSON(filepath.Join(p.Local, "state.json"), state); e != nil {
			fmt.Fprintln(log, "Could not save status:", e)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func checkPorts(s Settings) error {
	for _, item := range []struct {
		ports Ports
		host  string
	}{{Field, s.Host}, {Central, "127.0.0.1"}} {
		for _, port := range []int{item.ports.API, item.ports.Metrics, item.ports.Web, item.ports.RTSP} {
			l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				return fmt.Errorf("port %d is already used; stop its owner before starting this lab: %w", port, err)
			}
			l.Close()
		}
		for _, addr := range []string{fmt.Sprintf("%s:%d", item.host, item.ports.SRT), fmt.Sprintf("127.0.0.1:%d", item.ports.ICE)} {
			l, err := net.ListenPacket("udp", addr)
			if err != nil {
				return fmt.Errorf("cannot use %s; a service may own it, or the Mac's address changed: %w", addr, err)
			}
			l.Close()
		}
	}
	return nil
}

func mediaServicesPresent(_ Settings) bool {
	for _, ports := range []Ports{Field, Central} {
		for _, port := range []int{ports.RTSP, ports.Web, ports.API} {
			connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
			if err == nil {
				connection.Close()
				return true
			}
		}
	}
	return false
}

func (p Paths) WithStoppedConfiguration(change func() error) error {
	if err := os.MkdirAll(p.Local, 0700); err != nil {
		return err
	}
	lock, err := fileLock(filepath.Join(p.Local, "startup.lock"), false)
	if err != nil {
		return err
	}
	defer lock.Close()
	if p.Running() {
		return errors.New("stop the lab before changing source connections: ./lab stop")
	}
	return change()
}

func apiHealthy(ports Ports) bool {
	response, err := apiClient.Get(fmt.Sprintf("http://127.0.0.1:%d/v3/paths/list", ports.API))
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == 200
}

func (p Paths) Start() error {
	s, err := p.LoadSettings()
	if err != nil {
		return err
	}
	startLock, err := fileLock(filepath.Join(p.Local, "startup.lock"), false)
	if err != nil {
		return err
	}
	defer startLock.Close()
	if p.Running() {
		if apiHealthy(Field) && apiHealthy(Central) {
			return nil
		}
		return errors.New("the controller is running, but a video service is unavailable; inspect ./lab status and ./lab logs")
	}
	if err = checkPorts(s); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Join(p.Local, "logs"), 0700); err != nil {
		return err
	}
	controls, err := p.loadControls(s)
	if err != nil {
		return err
	}
	for id, control := range controls.Sources {
		control.DemoEnabled = false
		controls.Sources[id] = control
	}
	if err = AtomicJSON(filepath.Join(p.Local, "control.json"), controls); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	token := identifier()
	log, err := os.OpenFile(filepath.Join(p.Local, "logs", "startup.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "--root", p.Root, "_supervise", token)
	cmd.Dir = p.Root
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		log.Close()
		return err
	}
	log.Close()
	for i := 0; i < 50; i++ {
		time.Sleep(200 * time.Millisecond)
		var state State
		_ = readJSON(filepath.Join(p.Local, "state.json"), &state)
		if p.Running() && state.SupervisorToken == token && apiHealthy(Field) && apiHealthy(Central) {
			// The supervisor lives independently after this short CLI exits.
			_ = cmd.Process.Release()
			return nil
		}
	}
	// This is still our child process, so cleanup does not rely on a saved PID.
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	return errors.New("video services did not become ready; the startup attempt was stopped; run ./lab logs")
}

func (p Paths) Stop() error {
	startLock, err := fileLock(filepath.Join(p.Local, "startup.lock"), false)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer startLock.Close()
	if !p.Running() {
		if settings, e := p.LoadSettings(); e == nil && mediaServicesPresent(settings) {
			return errors.New("the controller is stopped, but lab ports remain occupied; refusing to report all services stopped or kill an unknown process")
		}
		return nil
	}
	var identity struct {
		PID   int    `json:"pid"`
		Token string `json:"token"`
	}
	if err := readJSON(filepath.Join(p.Local, "supervisor.json"), &identity); err != nil {
		return err
	}
	if identity.PID <= 1 || identity.Token == "" {
		return errors.New("supervisor identity is missing; refusing to stop an unknown process")
	}
	command, err := exec.Command("ps", "-p", strconv.Itoa(identity.PID), "-o", "args=").Output()
	if err != nil || !strings.Contains(string(command), "_supervise "+identity.Token) || !strings.Contains(string(command), p.Root) {
		return errors.New("supervisor identity changed; refusing to stop an unrelated process")
	}
	process, err := os.FindProcess(identity.PID)
	if err != nil {
		return err
	}
	if err = process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	for i := 0; i < 150; i++ {
		if !p.Running() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("services are still closing; inspect ./lab logs")
}

func (p Paths) SafeLogs() (string, error) {
	s, err := p.LoadSettings()
	if err != nil {
		return "", err
	}
	var out strings.Builder
	names := []string{"startup", "supervisor", "field", "central"}
	for _, source := range GetSources(s) {
		for _, role := range []string{"relay", "recorder", "demo"} {
			names = append(names, role+"-"+source.ID)
		}
	}
	for _, name := range names {
		f, e := os.Open(filepath.Join(p.Local, "logs", name+".log"))
		if e != nil {
			continue
		}
		info, e := f.Stat()
		if e != nil {
			f.Close()
			continue
		}
		start := info.Size() - 6000
		if start < 0 {
			start = 0
		}
		_, _ = f.Seek(start, io.SeekStart)
		data, _ := io.ReadAll(f)
		f.Close()
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) > 12 {
			lines = lines[len(lines)-12:]
		}
		fmt.Fprintf(&out, "%s:\n%s\n\n", name, strings.Join(lines, "\n"))
	}
	result := out.String()
	secrets := []string{s.PublisherPassword, s.PublishPassphrase, s.RelayPassword, s.CentralPassphrase}
	for _, source := range GetSources(s) {
		secrets = append(secrets, source.PublisherPassword, source.PublishPassphrase)
	}
	for _, secret := range secrets {
		if secret != "" {
			result = strings.ReplaceAll(result, secret, "[hidden]")
		}
	}
	return result, nil
}
