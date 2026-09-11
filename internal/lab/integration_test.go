package lab

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestIntegrationProbeRequiresDecodedFrames(t *testing.T) {
	if _, err := parseIntegrationProbe([]byte(`{"streams":[{"codec_name":"h264","width":1280,"height":720,"r_frame_rate":"30/1"}]}`)); err == nil {
		t.Fatal("a stream header must not count as successful video decoding")
	}
	data := []byte(`{"streams":[{"codec_name":"h264","width":640,"height":360,"r_frame_rate":"20/1"}],"frames":[{"media_type":"video","width":640,"height":360,"best_effort_timestamp_time":"10.00"},{"media_type":"video","width":640,"height":360,"best_effort_timestamp_time":"10.05"},{"media_type":"video","width":640,"height":360,"best_effort_timestamp_time":"10.10"}]}`)
	media, err := parseIntegrationProbe(data)
	if err != nil {
		t.Fatal(err)
	}
	if media.DecodedFrames != 3 || media.FrameRate != 20 || media.ObservedRate != 20 || media.Width != 640 || media.Height != 360 {
		t.Fatalf("incorrect decoded media evidence: %+v", media)
	}
	wrongDimensions := strings.Replace(string(data), `"media_type":"video","width":640`, `"media_type":"video","width":1280`, 1)
	if _, err := parseIntegrationProbe([]byte(wrongDimensions)); err == nil {
		t.Fatal("a mismatched decoded frame must invalidate the advertised dimensions")
	}
}

// Opt-in investigation for source-disconnect damage. It uses separate ports and
// generated video only; ordinary unit tests do not start any media services.
func TestIntegrationDisconnectDiagnostic(t *testing.T) {
	root := os.Getenv("FIELD_VIDEO_DIAGNOSTIC_ROOT")
	if root == "" {
		t.Skip("set FIELD_VIDEO_DIAGNOSTIC_ROOT to run the bounded media diagnostic")
	}
	p := NewPaths(root)
	settings, err := p.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"127.0.0.1:38554", "127.0.0.1:39997"} {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatalf("diagnostic port is occupied: %v", err)
		}
		listener.Close()
	}
	listener, err := net.ListenPacket("udp", "127.0.0.1:38890")
	if err != nil {
		t.Fatalf("diagnostic publisher port is occupied: %v", err)
	}
	listener.Close()
	trialPaths, settings, err := prepareIntegrationRoot(p, settings)
	if err != nil {
		t.Fatal(err)
	}
	config := serverConfig(settings, false)
	config["srtAddress"] = "127.0.0.1:38890"
	config["rtspAddress"] = "127.0.0.1:38554"
	config["apiAddress"] = "127.0.0.1:39997"
	config["metrics"], config["webrtc"] = false, false
	configPath := filepath.Join(trialPaths.Local, "disconnect-trial.yml")
	if err := AtomicJSON(configPath, config); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	type child struct {
		cmd      *exec.Cmd
		done     chan error
		finished bool
		forced   bool
	}
	var children []*child
	start := func(label string, args []string) *child {
		log, err := os.OpenFile(filepath.Join(trialPaths.Root, label+".log"), os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Stdout, cmd.Stderr, cmd.Dir = log, log, trialPaths.Root
		item := &child{cmd: cmd, done: make(chan error, 1)}
		if err := cmd.Start(); err != nil {
			log.Close()
			t.Fatal(err)
		}
		children = append(children, item)
		go func() { err := cmd.Wait(); log.Close(); item.done <- err }()
		return item
	}
	finish := func(item *child, signal os.Signal, limit time.Duration) {
		if item.finished {
			return
		}
		if signal != nil {
			_ = item.cmd.Process.Signal(signal)
		}
		select {
		case <-item.done:
		case <-time.After(limit):
			item.forced = true
			_ = item.cmd.Process.Kill()
			<-item.done
		}
		item.finished = true
	}
	defer func() {
		for i := len(children) - 1; i >= 0; i-- {
			finish(children[i], syscall.SIGTERM, time.Second)
		}
	}()
	start("server", []string{p.BinaryPath(), configPath})
	client := &http.Client{Timeout: time.Second}
	ready := false
	for attempt := 0; attempt < 20; attempt++ {
		response, err := client.Get("http://127.0.0.1:39997/v3/paths/list")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		if integrationSleep(ctx, 200*time.Millisecond) != nil {
			break
		}
	}
	if !ready {
		t.Fatal("diagnostic MediaMTX server did not start")
	}
	var results []map[string]any
	for _, mode := range []string{"interrupt", "kill", "interrupt-linger"} {
		for repeat := 0; repeat < 2; repeat++ {
			name := fmt.Sprintf("%s-%d", mode, repeat)
			args := demoCommand(settings, integrationFirstSource)
			address, err := url.Parse(args[len(args)-1])
			if err != nil {
				t.Fatal(err)
			}
			address.Host = "127.0.0.1:38890"
			if mode == "interrupt-linger" {
				q := address.Query()
				q.Set("linger", "2")
				address.RawQuery = q.Encode()
			}
			args[len(args)-1] = address.String()
			publisher := start(name+"-source", args)
			if integrationSleep(ctx, time.Second) != nil {
				t.Fatal("diagnostic cancelled")
			}
			recorders := map[string]*child{}
			for _, variant := range []string{"drain", "discard-drain", "immediate-stop"} {
				file := filepath.Join(trialPaths.Root, name+"-"+variant+".mp4")
				args := []string{settings.FFmpeg, "-hide_banner", "-loglevel", "warning", "-nostdin"}
				if variant == "discard-drain" {
					args = append(args, "-fflags", "+discardcorrupt")
				}
				args = append(args, "-rtsp_transport", "tcp", "-timeout", "3000000", "-i", "rtsp://127.0.0.1:38554/camera-01", "-map", "0:v:0", "-an", "-c:v", "copy", "-movflags", "+faststart", file)
				recorders[variant] = start(name+"-"+variant, args)
			}
			if integrationSleep(ctx, 4200*time.Millisecond) != nil {
				t.Fatal("diagnostic cancelled")
			}
			stopSignal := os.Signal(os.Interrupt)
			if mode == "kill" {
				stopSignal = syscall.SIGKILL
			}
			finish(publisher, stopSignal, 4*time.Second)
			finish(recorders["immediate-stop"], os.Interrupt, 4*time.Second)
			for _, variant := range []string{"drain", "discard-drain", "immediate-stop"} {
				finish(recorders[variant], nil, 12*time.Second)
				file := filepath.Join(trialPaths.Root, name+"-"+variant+".mp4")
				_, decodeErr := integrationCommand(ctx, 8*time.Second, trialPaths.Root, "diagnostic full decode", settings.FFmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-xerror", "-i", file, "-map", "0:v:0", "-an", "-f", "null", "-")
				result := map[string]any{"mode": mode, "repeat": repeat, "recorder": variant, "full_decode_passed": decodeErr == nil, "recorder_forcibly_stopped": recorders[variant].forced, "file": filepath.Base(file)}
				results = append(results, result)
				t.Logf("%s / %s full decode passed: %t; forced stop: %t", name, variant, decodeErr == nil, recorders[variant].forced)
			}
			if integrationSleep(ctx, 300*time.Millisecond) != nil {
				t.Fatal("diagnostic cancelled")
			}
		}
	}
	if err := AtomicJSON(filepath.Join(trialPaths.Root, "disconnect-diagnostic.json"), results); err != nil {
		t.Fatal(err)
	}
	t.Logf("Diagnostic evidence kept in %s", trialPaths.Root)
}

func TestIntegrationRateRejectsInvalidValues(t *testing.T) {
	for _, input := range []string{"N/A", "0/0", "20/0", "20/NaN", "NaN", "Inf", "20/1/2"} {
		if got := integrationRate(input); got != 0 {
			t.Errorf("integrationRate(%q) = %v, want zero", input, got)
		}
	}
	if got := integrationRate("30000/1001"); got < 29.96 || got > 29.98 {
		t.Errorf("unexpected fractional rate: %v", got)
	}
}

func TestIntegrationFrameHashesParseOnlyDecodedData(t *testing.T) {
	data := []byte("#format: frame checksums\n#stream#, dts, pts, duration, size, hash\n0, 0, 0, 1, 12, 0123456789abcdef0123456789abcdef\n0, 1, 1, 1, 12, fedcba9876543210fedcba9876543210\n")
	hashes, err := integrationFrameHashes(data)
	if err != nil || len(hashes) != 2 || hashes[0] == hashes[1] {
		t.Fatalf("unexpected checksum evidence: %v, %v", hashes, err)
	}
	if _, err := integrationFrameHashes([]byte("0,0,0,1,12,not-a-hash")); err == nil {
		t.Fatal("malformed checksums must fail instead of pretending that video changed")
	}
}

func TestIntegrationCommandsHideSecretsAndRespectCancellation(t *testing.T) {
	_, err := integrationCommand(context.Background(), time.Second, t.TempDir(), "test child", "/bin/sh", "-c", "printf 'private-connection-password' >&2; exit 9")
	if err == nil || strings.Contains(err.Error(), "private-connection-password") {
		t.Fatalf("child errors must be reported without raw secret output: %v", err)
	}
	started := time.Now()
	_, err = integrationCommand(context.Background(), 100*time.Millisecond, t.TempDir(), "test wait", "/bin/sh", "-c", "exec sleep 5")
	if err == nil || time.Since(started) > 2*time.Second {
		t.Fatalf("the child command did not stop promptly at its time limit: %v", err)
	}
	buffer := &integrationBuffer{limit: 8}
	if n, err := buffer.Write([]byte("0123456789012345")); err != nil || n != 16 || buffer.Len() != 8 || !buffer.truncated {
		t.Fatalf("bounded output writer did not retain its limit: n=%d err=%v len=%d truncated=%v", n, err, buffer.Len(), buffer.truncated)
	}
}

func TestIntegrationRootIsPrivateAndIsolatesConcurrentSources(t *testing.T) {
	p := NewPaths(t.TempDir())
	if err := os.MkdirAll(p.Tools, 0700); err != nil {
		t.Fatal(err)
	}
	original := Settings{Host: "192.0.2.10", PublisherUser: "camera", PublisherPassword: strings.Repeat("1", 32), PublishPassphrase: strings.Repeat("2", 32),
		RelayUser: "relay", RelayPassword: strings.Repeat("3", 32), CentralPassphrase: strings.Repeat("4", 32), FFmpeg: "/example/ffmpeg", FFprobe: "/example/ffprobe", Version: mediaMTXVersion}
	original.Sources = GetSources(original)
	if err := AtomicJSON(filepath.Join(p.Local, "settings.json"), original); err != nil {
		t.Fatal(err)
	}
	testPaths, cloned, err := prepareIntegrationRoot(p, original)
	if err != nil {
		t.Fatal(err)
	}
	if cloned.Host != "127.0.0.1" || cloned.PublisherPassword == original.PublisherPassword || cloned.PublishPassphrase == original.PublishPassphrase || cloned.RelayPassword == original.RelayPassword || cloned.CentralPassphrase == original.CentralPassphrase {
		t.Fatal("test settings must have loopback input and their own credentials")
	}
	if cloned.FFmpeg != original.FFmpeg || cloned.FFprobe != original.FFprobe {
		t.Fatal("the test must reuse the installed media tools")
	}
	sources := GetSources(cloned)
	if len(sources) != 2 || sources[0].ID != integrationFirstSource || sources[1].ID != integrationSecondSource || sources[0].PublisherUser == sources[1].PublisherUser || sources[0].PublisherPassword == sources[1].PublisherPassword || sources[0].PublishPassphrase == sources[1].PublishPassphrase {
		t.Fatal("the test must create two independent camera identities")
	}
	stored, err := p.LoadSettings()
	if err != nil || !reflect.DeepEqual(stored, original) {
		t.Fatalf("the original settings changed: %v", err)
	}
	for _, path := range []string{testPaths.Root, testPaths.Local, testPaths.Recordings, testPaths.Reports} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("test directory is not private: %s (%v)", path, err)
		}
	}
	for _, filename := range []string{"settings.json", "field.yml", "central.yml"} {
		info, err := os.Stat(filepath.Join(testPaths.Local, filename))
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("test settings are not private: %s (%v)", filename, err)
		}
	}
	linked, err := os.Readlink(testPaths.Tools)
	if err != nil || linked != p.Tools {
		t.Fatalf("test tools are not shared by symlink: %v", err)
	}
	if _, err := os.Stat(filepath.Join(testPaths.Local, "r2.json")); !os.IsNotExist(err) {
		t.Fatal("test root must not inherit real cloud upload configuration")
	}
	var config map[string]any
	data, err := os.ReadFile(filepath.Join(testPaths.Local, "field.yml"))
	if err != nil || json.Unmarshal(data, &config) != nil || config["srtAddress"] != "127.0.0.1:18890" {
		t.Fatalf("the test camera listener is not restricted to loopback: %v", err)
	}
	paths, ok := config["paths"].(map[string]any)
	if !ok || len(paths) != 2 || paths[integrationFirstSource] == nil || paths[integrationSecondSource] == nil || paths["ipad"] != nil {
		t.Fatal("test listener paths must contain both device-neutral source IDs")
	}
}

func TestIntegrationPerSourceChecksCannotUseAggregateRecordingGrowth(t *testing.T) {
	state := State{Running: true, Recording: RecordingState{Segments: 100, Bytes: 10000}, Sources: map[string]StreamState{
		integrationFirstSource:  {ID: integrationFirstSource, Recording: RecordingState{Segments: 1, Bytes: 100}},
		integrationSecondSource: {ID: integrationSecondSource, Recording: RecordingState{Segments: 99, Bytes: 9900}},
	}}
	selected := integrationSelectSource(state, integrationFirstSource)
	if selected.Recording.Segments != 1 || selected.Recording.Bytes != 100 || !selected.Running {
		t.Fatal("per-source assertions must not be satisfied by another source's recording bytes")
	}
	if missing := integrationSelectSource(state, "missing"); missing.Running || missing.Recording.Segments != 0 {
		t.Fatal("missing source must not fall back to the aggregate status")
	}
	if got := integrationVideoURL(Field, integrationSecondSource); got != "rtsp://127.0.0.1:18554/camera-02" {
		t.Fatalf("unexpected source path: %s", got)
	}
}

func TestIntegrationWorkerContinuityRequiresEverySourceWorker(t *testing.T) {
	before := State{Workers: map[string]WorkerState{"demo": {PID: 10, Running: true, Starts: 1}, "relay": {PID: 11, Running: true, Starts: 1}, "recorder": {PID: 12, Running: true, Starts: 1}}}
	after := State{Workers: map[string]WorkerState{"demo": {PID: 10, Running: true, Starts: 1}, "relay": {PID: 11, Running: true, Starts: 1}, "recorder": {PID: 12, Running: true, Starts: 1}}}
	if !integrationSameWorkers(before, after) {
		t.Fatal("unchanged source workers should pass")
	}
	after.Workers["relay"] = WorkerState{PID: 13, Running: true, Starts: 2}
	if integrationSameWorkers(before, after) {
		t.Fatal("a restarted second-source relay must fail isolation evidence")
	}
}

func TestIntegrationCompletedClipsIgnoreActiveAndEscapedFiles(t *testing.T) {
	p := NewPaths(t.TempDir())
	session := filepath.Join(p.Recordings, "session")
	if err := os.MkdirAll(session, 0700); err != nil {
		t.Fatal(err)
	}
	finished := []byte("completed synthetic recording bytes")
	for name, data := range map[string][]byte{"finished.mp4": finished, "active.mp4": []byte("still being written")} {
		if err := os.WriteFile(filepath.Join(session, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(p.Root, "outside.mp4")
	if err := os.WriteFile(outside, []byte("outside test recording root"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(session, "escaped.mp4")); err != nil {
		t.Fatal(err)
	}
	list := "finished.mp4,0,5\nfinished.mp4,0,5\nescaped.mp4,0,5\n../../outside.mp4,0,5\nactive.mp4,NaN,5\n"
	if err := os.WriteFile(filepath.Join(session, "segments.csv"), []byte(list), 0600); err != nil {
		t.Fatal(err)
	}
	clips, err := integrationCompletedClips(p)
	if err != nil || len(clips) != 1 {
		t.Fatalf("only one completed, contained file should be selected: %v (%v)", clips, err)
	}
	digest := sha256.Sum256(finished)
	if clips[0].Path != filepath.Join("session", "finished.mp4") || clips[0].Bytes != int64(len(finished)) || clips[0].Duration != 5 || clips[0].SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("incorrect finalized-file evidence: %+v", clips[0])
	}
}
