package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"fieldvideolab/internal/lab"
)

func TestOptionsBoundResourcesAndRejectURLs(t *testing.T) {
	for _, args := range [][]string{
		{"--source", "rtsp://example.com/camera"}, {"--source", "../camera-01"}, {"--source", "camera-01?password=secret"},
		{"--duration", "4s"}, {"--duration", "121s"}, {"--duration", "-1s"},
		{"--latency-ms", "-1"}, {"--latency-ms", "201"}, {"--route", "https://example.com"},
		{"--decoder", "anything ! filesink"}, {"--sink", "filesink"}, {"--root", ""}, {"extra"},
	} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatalf("accepted invalid options %q", args)
		}
	}
	cfg, err := parseOptions(nil, io.Discard)
	if err != nil || cfg.Duration != 30*time.Second || cfg.LatencyMS != 100 || cfg.Route != "local" || cfg.Decoder != "software" || cfg.Sink != "gl" {
		t.Fatalf("unexpected defaults: %+v, %v", cfg, err)
	}
}

func TestPipelineFixedSourceAndDecodedPictureQueue(t *testing.T) {
	cfg, _ := parseOptions(nil, io.Discard)
	sources := []lab.SourceConfig{{ID: "camera-01", PublisherPassword: "secret-never-in-argv"}}
	args, err := pipeline(cfg, sources)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"location=rtsp://127.0.0.1:18554/camera-01", "protocols=tcp", "latency=100", "tcp-timeout=5000000", "drop-on-latency=true", "max-threads=1", "sync=false"} {
		if !slices.Contains(args, want) {
			t.Fatalf("missing %q", want)
		}
	}
	queue := slices.Index(args, "queue")
	decoder := slices.Index(args, "avdec_h264")
	if decoder < 0 || queue <= decoder || slices.Index(args, "videoconvert") <= queue || slices.Index(args, "glimagesink") <= queue {
		t.Fatalf("queue must discard decoded pictures before conversion/display: %q", args)
	}
	if !slices.Equal(args[queue:queue+5], []string{"queue", "max-size-buffers=1", "max-size-bytes=0", "max-size-time=0", "leaky=downstream"}) {
		t.Fatal("decoded picture queue is not bounded")
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "secret") || strings.Contains(joined, "tcp-timestamp") || strings.Contains(joined, "autovideosink") {
		t.Fatal("unexpected credential, timestamp override or automatic sink")
	}
	explicit, err := parseOptions([]string{"--latency-ms", "20"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	explicitArgs, err := pipeline(explicit, sources)
	wantExplicit := slices.Clone(args)
	wantExplicit[slices.Index(wantExplicit, "latency=100")] = "latency=20"
	if err != nil || !slices.Equal(explicitArgs, wantExplicit) {
		t.Fatalf("explicit 20 ms should change only the waiting time: %q %v", explicitArgs, err)
	}
	cfg.Route, cfg.Decoder, cfg.Sink = "forwarded", "hardware", "headless"
	args, err = pipeline(cfg, sources)
	if err != nil || !slices.Contains(args, "location=rtsp://127.0.0.1:28554/camera-01") || !slices.Contains(args, "vtdec_hw") || !slices.Contains(args, "fakesink") || slices.Contains(args, "max-threads=1") {
		t.Fatalf("unexpected hardware/headless pipeline: %q %v", args, err)
	}
	cfg.Source = "camera-02"
	if _, err := pipeline(cfg, sources); err == nil {
		t.Fatal("unregistered source accepted")
	}
	cfg.Source = "camera-01/@example.com"
	sources = append(sources, lab.SourceConfig{ID: cfg.Source})
	if _, err := pipeline(cfg, sources); err == nil {
		t.Fatal("unsafe source accepted even though registered")
	}
}

func settingsFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".local"), 0700); err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("a", 32)
	settings := lab.Settings{Host: "127.0.0.1", Version: "1.21.0", FFmpeg: "/unused/ffmpeg", FFprobe: "/unused/ffprobe",
		RelayUser: "relay", RelayPassword: secret, CentralPassphrase: secret,
		Sources: []lab.SourceConfig{{ID: "camera-01", Label: "Camera 1", PublisherUser: "camera-01", PublisherPassword: secret, PublishPassphrase: secret}}}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".local", "settings.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDryRunUsesPrivateSettingsWithoutLaunchingOrWritingReports(t *testing.T) {
	root := settingsFixture(t)
	var output, errorOutput bytes.Buffer
	args := []string{"--root", root, "--dry-run", "--gst-launch", "/not-installed/gst-launch-1.0"}
	if code := run(context.Background(), args, &output, &errorOutput); code != 0 {
		t.Fatalf("dry run failed: %d %s", code, errorOutput.String())
	}
	var argv []string
	if err := json.Unmarshal(output.Bytes(), &argv); err != nil || !slices.Contains(argv, "location=rtsp://127.0.0.1:18554/camera-01") {
		t.Fatalf("invalid planned arguments: %s %v", output.String(), err)
	}
	if strings.Contains(output.String(), strings.Repeat("a", 32)) {
		t.Fatal("credentials leaked into planned arguments")
	}
	if _, err := os.Lstat(filepath.Join(root, "reports")); !os.IsNotExist(err) {
		t.Fatal("dry run created reports")
	}
	if err := os.Chmod(filepath.Join(root, ".local", "settings.json"), 0644); err != nil {
		t.Fatal(err)
	}
	if code := run(context.Background(), args, io.Discard, io.Discard); code == 0 {
		t.Fatal("accepted nonprivate settings")
	}
}

func TestDefaultDryRunWorksBeforeInstallationWithoutWritingFiles(t *testing.T) {
	root := settingsFixture(t)
	t.Setenv("PATH", t.TempDir())
	var output, errorOutput bytes.Buffer
	if code := run(context.Background(), []string{"--root", root, "--dry-run"}, &output, &errorOutput); code != 0 {
		t.Fatalf("dry run should remain available before installation: %d %s", code, errorOutput.String())
	}
	var argv []string
	if err := json.Unmarshal(output.Bytes(), &argv); err != nil || len(argv) == 0 || argv[0] != "gst-launch-1.0" || !slices.Contains(argv, "latency=100") {
		t.Fatalf("missing planned media arguments: %q %v", argv, err)
	}
	if !strings.Contains(errorOutput.String(), "executable not resolved") || !strings.Contains(errorOutput.String(), "placeholder") {
		t.Fatalf("dry run implied an installed executable: %s", errorOutput.String())
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != ".local" {
		t.Fatalf("dry run wrote unexpected project files: %v %v", entries, err)
	}
	if code := run(context.Background(), []string{"--root", root}, io.Discard, io.Discard); code == 0 {
		t.Fatal("an actual run accepted the unresolved executable")
	}
}

func TestReportDirectoryPrivateAndRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	directory, err := privateReportDir(root)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("report directory is not private: %v %v", info, err)
	}
	linkedRoot := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(linkedRoot, "reports")); err != nil {
		t.Fatal(err)
	}
	if _, err := privateReportDir(linkedRoot); err == nil {
		t.Fatal("accepted a symlinked reports directory")
	}
}
