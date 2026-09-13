package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"fieldvideolab/internal/gstreamer"
	"fieldvideolab/internal/lab"
)

func viewSettings(t *testing.T) lab.Paths {
	t.Helper()
	p := lab.NewPaths(t.TempDir())
	if err := os.Mkdir(p.Local, 0700); err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("a", 32)
	settings := lab.Settings{Host: "127.0.0.1", Version: "1.21.0", FFmpeg: "/unused/ffmpeg", FFprobe: "/unused/ffprobe",
		RelayUser: "relay", RelayPassword: secret, CentralPassphrase: secret,
		Sources: []lab.SourceConfig{
			{ID: "camera-02", Label: "First camera", PublisherUser: "camera-02", PublisherPassword: secret, PublishPassphrase: secret},
			{ID: "camera-01", Label: "Second camera", PublisherUser: "camera-01", PublisherPassword: secret, PublishPassphrase: secret},
		}}
	if err := writeViewJSON(filepath.Join(p.Local, "settings.json"), settings); err != nil {
		t.Fatal(err)
	}
	return p
}

func fakeViewExecutable(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-gst-launch")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

type viewRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip viewRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestViewSourceReadyUsesOnlySelectedReceiver(t *testing.T) {
	for _, test := range []struct {
		name, route, host, body string
		status                  int
		ready, fails            bool
	}{
		{"local ready", "local", "127.0.0.1:19997", `{"ready":true}`, 200, true, false},
		{"forwarded ready", "forwarded", "127.0.0.1:29997", `{"online":true}`, 200, true, false},
		{"available compatibility", "local", "127.0.0.1:19997", `{"available":true}`, 200, true, false},
		{"not ready", "local", "127.0.0.1:19997", `{"ready":false,"online":true}`, 200, false, false},
		{"absent publisher", "local", "127.0.0.1:19997", `{"error":"not found"}`, 404, false, false},
		{"denied", "local", "127.0.0.1:19997", `private error`, 401, false, true},
		{"invalid JSON", "local", "127.0.0.1:19997", `{`, 200, false, true},
		{"missing readiness", "local", "127.0.0.1:19997", `{}`, 200, false, true},
		{"oversized response", "local", "127.0.0.1:19997", `{"ready":true}` + strings.Repeat(" ", 64<<10), 200, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: viewRoundTripper(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodGet || request.URL.Scheme != "http" || request.URL.Host != test.host || request.URL.Path != "/v3/paths/get/camera-02" || request.URL.RawQuery != "" {
					t.Fatalf("unexpected status request: %s %s", request.Method, request.URL)
				}
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body)), Header: make(http.Header)}, nil
			})}
			ready, err := viewSourceReady(context.Background(), client, "camera-02", test.route)
			if ready != test.ready || (err != nil) != test.fails {
				t.Fatalf("readiness = %v, %v", ready, err)
			}
			if err != nil && strings.Contains(err.Error(), "private error") {
				t.Fatal("response body exposed in error")
			}
		})
	}
}

func TestWaitForViewSourceReadyAfterWaiting(t *testing.T) {
	var output bytes.Buffer
	calls := 0
	err := waitForViewSource(context.Background(), "camera-02", "forwarded", time.Second, time.Millisecond,
		func(ctx context.Context) (bool, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("readiness request lacks deadline")
			}
			calls++
			return calls == 3, nil
		}, &output)
	if err != nil || calls != 3 || strings.Count(output.String(), "Waiting up to") != 1 || !strings.Contains(output.String(), "camera-02 / forwarded") {
		t.Fatalf("wait result: %v, %d calls, %q", err, calls, output.String())
	}
}

func TestWaitForViewSourceAbsentUnavailableAndCanceled(t *testing.T) {
	var output bytes.Buffer
	err := waitForViewSource(context.Background(), "camera-02", "forwarded", 5*time.Millisecond, time.Millisecond,
		func(context.Context) (bool, error) { return false, nil }, &output)
	if err == nil || !strings.Contains(err.Error(), "./lab --source camera-02 view forwarded") || !strings.Contains(err.Error(), "forwarding is enabled") {
		t.Fatalf("missing source guidance: %v", err)
	}
	if strings.Count(output.String(), "Waiting up to") != 1 {
		t.Fatalf("repeated waiting output: %q", output.String())
	}
	calls := 0
	err = waitForViewSource(context.Background(), "camera-02", "local", time.Minute, time.Second,
		func(context.Context) (bool, error) {
			calls++
			return false, errors.New("private network details")
		}, io.Discard)
	if err == nil || calls != 1 || !strings.Contains(err.Error(), "./lab start") || strings.Contains(err.Error(), "private network details") {
		t.Fatalf("unavailable API did not fail promptly and safely: %v, %d calls", err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = waitForViewSource(ctx, "camera-02", "local", time.Minute, time.Second,
		func(context.Context) (bool, error) { cancel(); return false, nil }, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestViewDoesNotWriteReportsBeforeSourceIsReady(t *testing.T) {
	p := viewSettings(t)
	executable := fakeViewExecutable(t, "exit 7")
	cfg, _ := parseViewOptions([]string{"--gst-launch", executable}, io.Discard)
	waitErr := errors.New("source did not become ready")
	err := runViewWithSourceWait(context.Background(), p, "camera-01", cfg, io.Discard,
		func(_ context.Context, id, route string, _ io.Writer) error {
			if id != "camera-01" || route != "local" {
				t.Fatalf("wrong source selected: %s / %s", id, route)
			}
			return waitErr
		})
	if !errors.Is(err, waitErr) {
		t.Fatalf("readiness failure hidden: %v", err)
	}
	if _, err := os.Lstat(p.Reports); !os.IsNotExist(err) {
		t.Fatal("unready source created a report")
	}
}

func TestViewOptions(t *testing.T) {
	cfg, err := parseViewOptions(nil, io.Discard)
	if err != nil || cfg.Route != "local" || cfg.LatencyMS != 50 || cfg.Decoder != "software" || cfg.GSTLaunch != "" {
		t.Fatalf("unexpected defaults: %+v %v", cfg, err)
	}
	cfg, err = parseViewOptions([]string{"forwarded", "--latency-ms", "50", "--decoder", "hardware", "--dry-run"}, io.Discard)
	if err != nil || cfg.Route != "forwarded" || cfg.LatencyMS != 50 || cfg.Decoder != "hardware" || !cfg.DryRun {
		t.Fatalf("explicit options: %+v %v", cfg, err)
	}
	for _, args := range [][]string{
		{"remote"}, {"local", "extra"}, {"--route", "local"}, {"--duration", "5s"}, {"--sink", "headless"},
		{"--latency-ms", "0"}, {"--latency-ms", "20"}, {"--latency-ms", "80"}, {"--latency-ms", "101"},
		{"--decoder", "anything ! filesink"}, {"--latency-ms", "50", "forwarded"},
	} {
		if _, err := parseViewOptions(args, io.Discard); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
	if err := viewCommand(lab.NewPaths(t.TempDir()), "", []string{"--help"}, io.Discard, io.Discard); err != nil {
		t.Fatalf("help required setup: %v", err)
	}
}

func TestViewDryRunDefaultsToFirstRegisteredSourceWithoutRuntimeOrWrites(t *testing.T) {
	p := viewSettings(t)
	var output bytes.Buffer
	if err := viewCommand(p, "", []string{"--dry-run", "--gst-launch", "/not/installed/gst-launch-1.0"}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Config    liveViewConfig
		Arguments []string
	}
	if err := json.Unmarshal(output.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Config.Source != "camera-02" || plan.Config.Route != "local" || plan.Config.LatencyMS != 50 ||
		!slices.Contains(plan.Arguments, "location=rtsp://127.0.0.1:18554/camera-02") || !slices.Contains(plan.Arguments, "latency=50") {
		t.Fatalf("unexpected plan: %s", output.String())
	}
	if strings.Contains(output.String(), strings.Repeat("a", 32)) || strings.Contains(output.String(), "publisher_password") {
		t.Fatal("private settings leaked")
	}
	if _, err := os.Lstat(p.Reports); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote reports")
	}
	output.Reset()
	if err := viewCommand(p, "camera-01", []string{"forwarded", "--latency-ms", "50", "--dry-run"}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "location=rtsp://127.0.0.1:28554/camera-01") || !strings.Contains(output.String(), "latency=50") {
		t.Fatalf("selected route ignored: %s", output.String())
	}
	if err := viewCommand(p, "unregistered", []string{"--dry-run"}, io.Discard, io.Discard); err == nil {
		t.Fatal("unregistered source accepted")
	}
	if err := os.Chmod(filepath.Join(p.Local, "settings.json"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := viewCommand(p, "", []string{"--dry-run"}, io.Discard, io.Discard); err == nil {
		t.Fatal("nonprivate settings accepted")
	}
}

func TestViewMissingRuntimeDoesNotCreateReport(t *testing.T) {
	p := viewSettings(t)
	t.Setenv("PATH", t.TempDir())
	if err := viewCommand(p, "", nil, io.Discard, io.Discard); err == nil {
		t.Fatal("missing GStreamer accepted")
	}
	if _, err := os.Lstat(p.Reports); !os.IsNotExist(err) {
		t.Fatal("missing tool created a report")
	}
}

const windowCloseDiagnostic = "ERROR: from element /GstPipeline:pipeline0/GstGLImageSinkBin:glimagesinkbin0/GstGLImageSink:sink: Output window was closed"

func TestWindowCloseClassificationDoesNotHideOtherFailures(t *testing.T) {
	one, zero := 1, 0
	closed := gstreamer.Result{Result: "exited", ExitCode: &one, ProcessError: "exit status 1"}
	if outcome, err := classifyViewResult(closed, []byte(windowCloseDiagnostic+"\nAdditional debug info:\n"), true); outcome != "window_closed" || err != nil {
		t.Fatalf("native close rejected: %s %v", outcome, err)
	}
	for _, log := range []string{
		"", "Output window was closed", "ERROR: from element /GstPipeline:pipeline0/GstRTSPSrc:source: Output window was closed",
		windowCloseDiagnostic + "\nERROR: from element /GstPipeline:pipeline0/GstAvdecH264:decoder: decode failed",
		"ERROR: failed to connect\n" + windowCloseDiagnostic,
	} {
		if _, err := classifyViewResult(closed, []byte(log), true); err == nil {
			t.Errorf("unrelated failure hidden: %q", log)
		}
	}
	if _, err := classifyViewResult(closed, []byte(windowCloseDiagnostic), false); err == nil {
		t.Fatal("truncated log accepted as proof of native close")
	}
	closed.ProcessError = "unexpected wait failure"
	if _, err := classifyViewResult(closed, []byte(windowCloseDiagnostic), true); err == nil {
		t.Fatal("unexpected wait error hidden")
	}
	if outcome, err := classifyViewResult(gstreamer.Result{Result: "exited", ExitCode: &zero}, nil, true); err != nil || outcome != "closed" {
		t.Fatalf("ordinary exit: %s %v", outcome, err)
	}
	if outcome, err := classifyViewResult(gstreamer.Result{Result: "canceled"}, nil, true); err != nil || outcome != "stopped" {
		t.Fatalf("interruption: %s %v", outcome, err)
	}
	if _, err := classifyViewResult(gstreamer.Result{Result: "canceled", ForcedStop: true}, nil, true); err == nil {
		t.Fatal("forced stop hidden")
	}
	if _, err := classifyViewResult(gstreamer.Result{Result: "duration_elapsed"}, nil, true); err == nil {
		t.Fatal("unexpected duration timer accepted")
	}
}

func TestViewWritesPrivateConfigurationAndResultForFakeProcess(t *testing.T) {
	for _, test := range []struct {
		name, body, outcome string
		fails               bool
	}{
		{"ordinary close", "exit 0", "closed", false},
		{"native window close", "printf '%s\\n' '" + windowCloseDiagnostic + "'\nexit 1", "window_closed", false},
		{"unexpected error", "printf '%s\\n' 'ERROR: decoder failed'\nexit 1", "failed", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := viewSettings(t)
			executable := fakeViewExecutable(t, test.body)
			var output bytes.Buffer
			cfg, err := parseViewOptions([]string{"forwarded", "--gst-launch", executable}, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			err = runViewWithSourceWait(context.Background(), p, "camera-01", cfg, &output,
				func(context.Context, string, string, io.Writer) error { return nil })
			if (err != nil) != test.fails {
				t.Fatalf("unexpected result: %v", err)
			}
			directories, err := filepath.Glob(filepath.Join(p.Reports, "live-view-*"))
			if err != nil || len(directories) != 1 {
				t.Fatalf("report directory missing: %v %v", directories, err)
			}
			info, err := os.Stat(directories[0])
			if err != nil || info.Mode().Perm() != 0700 {
				t.Fatalf("directory permissions: %v %v", info, err)
			}
			for _, name := range []string{"config.json", "process.log", "run.json"} {
				path := filepath.Join(directories[0], name)
				info, err := os.Lstat(path)
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > liveViewLogLimit {
					t.Fatalf("unsafe report file %s: %v %v", name, info, err)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(data, []byte(strings.Repeat("a", 32))) {
					t.Fatalf("credentials in %s", name)
				}
				if name == "run.json" {
					var report liveViewReport
					if err := json.Unmarshal(data, &report); err != nil {
						t.Fatal(err)
					}
					if report.Outcome != test.outcome || report.Process.Result != "exited" || report.Process.ForcedStop || !report.LogComplete {
						t.Fatalf("unexpected process report: %+v", report)
					}
				}
			}
		})
	}
}

func TestViewReportDirectoryRejectsSymlinkAndJSONDoesNotOverwrite(t *testing.T) {
	p := viewSettings(t)
	if err := os.Symlink(t.TempDir(), p.Reports); err != nil {
		t.Fatal(err)
	}
	if _, err := liveViewDirectory(p); err == nil {
		t.Fatal("symlink report root accepted")
	}
	path := filepath.Join(t.TempDir(), "existing.json")
	if err := writeViewJSON(path, map[string]int{"original": 1}); err != nil {
		t.Fatal(err)
	}
	if err := writeViewJSON(path, map[string]int{"replacement": 2}); err == nil {
		t.Fatal("existing private report replaced")
	}
}

func TestCanceledViewDoesNotRunChild(t *testing.T) {
	p := viewSettings(t)
	executable := fakeViewExecutable(t, "exit 7")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg, _ := parseViewOptions([]string{"--gst-launch", executable}, io.Discard)
	if err := runView(ctx, p, "", cfg, io.Discard); err != nil {
		t.Fatalf("already canceled viewer: %v", err)
	}
	if _, err := os.Lstat(p.Reports); !os.IsNotExist(err) {
		t.Fatal("canceled startup created a report")
	}
}
