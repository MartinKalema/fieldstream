package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fieldvideolab/internal/lab"
)

func TestGeneratedComparisonNeverCreatesRecordingCatalog(t *testing.T) {
	ffmpeg, ffprobe := qualityTools(t)
	p := lab.NewPaths(t.TempDir())
	if err := os.MkdirAll(p.Local, 0700); err != nil {
		t.Fatal(err)
	}
	settings := lab.Settings{Host: "127.0.0.1", Version: "1.21.0", FFmpeg: ffmpeg, FFprobe: ffprobe,
		Sources:   []lab.SourceConfig{{ID: "test-camera", Label: "Test camera", PublisherUser: "publisher", PublisherPassword: strings.Repeat("a", 32), PublishPassphrase: strings.Repeat("b", 32)}},
		RelayUser: "relay", RelayPassword: strings.Repeat("c", 32), CentralPassphrase: strings.Repeat("d", 32)}
	settingsPath := filepath.Join(p.Local, "settings.json")
	if err := lab.AtomicJSON(settingsPath, settings); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--root", p.Root, "--test-scene", "fast-motion", "--no-serve"}, &output); err != nil {
		t.Fatal(err)
	}
	directories, err := filepath.Glob(filepath.Join(p.Reports, "quality-run-*"))
	if err != nil || len(directories) != 1 {
		t.Fatalf("expected one completed private run: %v %v", directories, err)
	}
	report, err := readReport(directories[0])
	if err != nil || report.TestScene == nil || report.TestScene.ID != "fast-motion" || report.Input.Frames != 150 {
		t.Fatalf("generated source identity missing: %v", err)
	}
	for _, path := range []string{p.Recordings, filepath.Join(p.Local, "recordings.sqlite"), filepath.Join(p.Local, "r2.json")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("generated diagnostic touched recording/archive state: %s", filepath.Base(path))
		}
	}
	after, err := os.ReadFile(settingsPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("generated diagnostic changed settings")
	}
	output.Reset()
	if err := run(context.Background(), []string{"--report-dir", directories[0], "--no-serve"}, &output); err != nil || !strings.Contains(output.String(), "Generated test scene:") {
		t.Fatalf("reopening lost the generated-scene label: %v", err)
	}
}

func TestReportRejectsIncompleteOrInventedSceneProvenance(t *testing.T) {
	for _, change := range []string{"valid", "unknown-id", "missing-recipe", "changed-note", "wrong-frame-count"} {
		t.Run(change, func(t *testing.T) {
			dir, report := reportFixture(t)
			scene, _ := sceneDefinition("dim-noise")
			report.TestScene = &scene
			report.Input.Frames, report.Input.FPS, report.Input.Duration = 150, 30, 5
			report.Playback.Frames, report.Playback.FPS, report.Playback.Duration = 150, 30, 5
			switch change {
			case "unknown-id":
				scene.ID = "camera-real-low-light"
			case "missing-recipe":
				scene.Filter = ""
			case "changed-note":
				scene.Note = "Tested real camera low-light quality"
			case "wrong-frame-count":
				report.Input.Frames--
			}
			if err := lab.AtomicJSON(filepath.Join(dir, "result.json"), report); err != nil {
				t.Fatal(err)
			}
			_, err := readReport(dir)
			if (err == nil) != (change == "valid") {
				t.Fatalf("unexpected recipe validation: %v", err)
			}
		})
	}
	dir, _ := reportFixture(t)
	var data bytes.Buffer
	data.WriteString(`{"test_scene":null,`)
	encoded, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	data.Write(encoded[1:])
	if err := os.WriteFile(filepath.Join(dir, "result.json"), data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readReport(dir); err == nil {
		t.Fatal("explicit null provenance was silently treated as camera footage")
	}
}
