package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"fieldvideolab/internal/lab"
)

func TestOptionsRequireOneInputAndLoopback(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--recording", "one/file.mp4", "--report-dir", "saved"},
		{"--recording", "one/file.mp4", "--listen", "0.0.0.0:19082"},
		{"--recording", "one/file.mp4", "--listen", "example.com:19082"},
		{"--report-dir", "saved", "extra"},
	} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatalf("unsafe or ambiguous options accepted: %v", args)
		}
	}
	if _, err := parseOptions([]string{"--help"}, io.Discard); err != flag.ErrHelp {
		t.Fatalf("unexpected help result: %v", err)
	}
	if _, err := parseOptions([]string{"--recording", "one/file.mp4", "--listen", "127.0.0.1:0"}, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func catalogFixture(t *testing.T) (lab.Paths, *lab.Catalog, lab.Segment, []byte) {
	t.Helper()
	p := lab.NewPaths(t.TempDir())
	catalog, err := lab.OpenCatalog(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { catalog.Close() })
	data := []byte("test catalog bytes for copy boundary")
	digest := sha256.Sum256(data)
	s := lab.Segment{Path: "session-one/00000.mp4", Session: "session-one", SourceID: "camera-01", Bytes: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Duration: 2}
	if err := os.MkdirAll(filepath.Join(p.Recordings, s.Session), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Recordings, s.Path), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := catalog.UpsertSegment(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return p, catalog, s, data
}

func TestCatalogCopyPreservesOriginalAndHistory(t *testing.T) {
	p, catalog, s, data := catalogFixture(t)
	before, err := catalog.GetSegment(context.Background(), s.Path)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := copyCatalogRecording(context.Background(), p, s.Path, dir); err != nil {
		t.Fatal(err)
	}
	copy, err := os.ReadFile(filepath.Join(dir, "original.mp4"))
	if err != nil || !bytes.Equal(copy, data) {
		t.Fatal("comparison copy differs from cataloged source")
	}
	info, err := os.Stat(filepath.Join(dir, "original.mp4"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("comparison copy is not private")
	}
	after, err := catalog.GetSegment(context.Background(), s.Path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("comparison copy changed catalog state")
	}
	original, err := os.ReadFile(filepath.Join(p.Recordings, s.Path))
	if err != nil || !bytes.Equal(original, data) {
		t.Fatal("comparison copy changed the original")
	}
	if err := copyCatalogRecording(context.Background(), p, s.Path, dir); err == nil {
		t.Fatal("existing private comparison was overwritten")
	}
}

func TestCatalogSelectionDoesNotMigrateOlderSchema(t *testing.T) {
	p, _, s, _ := catalogFixture(t)
	// A writable OpenCatalog would try to migrate this version marker. The
	// diagnostic needs only the stable bytes/checksum columns and must not do so.
	db, err := sql.Open("sqlite", filepath.Join(p.Local, "recordings.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	if err := copyCatalogRecording(context.Background(), p, s.Path, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("read-only selection altered schema version: %d %v", version, err)
	}
}

func TestCatalogCopyRejectsChangedMissingOrEscapedInput(t *testing.T) {
	for _, mode := range []string{"changed", "missing", "symlink", "uncataloged", "traversal"} {
		t.Run(mode, func(t *testing.T) {
			p, _, s, data := catalogFixture(t)
			input := s.Path
			name := filepath.Join(p.Recordings, s.Path)
			switch mode {
			case "changed":
				changed := bytes.Clone(data)
				changed[0] ^= 1 // Same length must still fail the checksum.
				if err := os.WriteFile(name, changed, 0600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				outside := filepath.Join(t.TempDir(), "outside.mp4")
				if err := os.Rename(name, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, name); err != nil {
					t.Fatal(err)
				}
			case "uncataloged":
				input = "session-one/unfinished.mp4"
			case "traversal":
				input = "../outside.mp4"
			}
			dir := t.TempDir()
			if err := copyCatalogRecording(context.Background(), p, input, dir); err == nil {
				t.Fatal("invalid input accepted")
			}
			if _, err := os.Stat(filepath.Join(dir, "original.mp4")); !os.IsNotExist(err) {
				t.Fatal("failed copy left a misleading original.mp4")
			}
		})
	}
}

func TestComparisonServerRestrictsFilesAndSupportsRanges(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "playback.mp4"), []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("not a published artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	handler := comparisonHandler(dir, "127.0.0.1:19082")
	for _, path := range []string{"/secret.txt", "/../secret.txt", "/result.json", "/original.mp4", "/subdir/playback.mp4"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:19082"+path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("unlisted route accepted: %s status=%d", path, response.Code)
		}
	}
	for _, test := range []struct {
		method, host string
		status       int
	}{
		{http.MethodGet, "attacker.example", http.StatusForbidden},
		{http.MethodPost, "127.0.0.1:19082", http.StatusMethodNotAllowed},
	} {
		request := httptest.NewRequest(test.method, "http://127.0.0.1:19082/", nil)
		request.Host = test.host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("wrong guard result: %d", response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:19082/playback.mp4", nil)
	request.Header.Set("Range", "bytes=2-5")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPartialContent || response.Body.String() != "2345" || response.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Fatalf("range or privacy headers failed: %d %q", response.Code, response.Body.String())
	}
	outside := filepath.Join(t.TempDir(), "outside.mp4")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "small-20.mp4")); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:19082/small-20.mp4", nil))
	if response.Code != http.StatusNotFound {
		t.Fatal("server followed a media link outside the private run")
	}
}

func reportFixture(t *testing.T) (string, comparisonReport) {
	t.Helper()
	dir := t.TempDir()
	var originalHash string
	variants := []Variant{}
	for _, name := range []string{"original.mp4", "playback.mp4", "detail-20.mp4", "small-20.mp4"} {
		data := []byte("fixed fixture bytes")
		digest := sha256.Sum256(data)
		checksum := hex.EncodeToString(digest[:])
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
		if name == "original.mp4" || name == "playback.mp4" {
			originalHash = checksum
			continue
		}
		variants = append(variants, Variant{Name: name, File: name, SHA256: checksum, Width: 640, Height: 360,
			FPS: 20, Frames: 40, Bytes: int64(len(data)), EncodeSeconds: .1, CPUSeconds: 0, SSIM: 0, ComparedFrames: 40})
	}
	report := comparisonReport{CreatedAt: time.Now().UTC(), FFmpegVersion: "ffmpeg fixture",
		Input: inputMedia{File: "original.mp4", Codec: "h264", PixelFormat: "yuv420p", SHA256: originalHash, Width: 1280, Height: 720, Frames: 60, FPS: 30, Duration: 2, Bytes: 19}, Variants: variants}
	report.Playback = report.Input
	report.Playback.File = "playback.mp4"
	if err := lab.AtomicJSON(filepath.Join(dir, "result.json"), report); err != nil {
		t.Fatal(err)
	}
	return dir, report
}

func TestReportRequiresMetricsAndMatchingArtifacts(t *testing.T) {
	t.Run("measured-zero-is-valid", func(t *testing.T) {
		dir, _ := reportFixture(t)
		if _, err := readReport(dir); err != nil {
			t.Fatalf("explicitly measured zero was rejected: %v", err)
		}
	})
	for _, field := range []string{"sha256", "frames", "start_time"} {
		t.Run("missing-playback-"+field, func(t *testing.T) {
			dir, report := reportFixture(t)
			data, _ := json.Marshal(report)
			var object map[string]any
			if err := json.Unmarshal(data, &object); err != nil {
				t.Fatal(err)
			}
			delete(object["playback"].(map[string]any), field)
			if err := lab.AtomicJSON(filepath.Join(dir, "result.json"), object); err != nil {
				t.Fatal(err)
			}
			if _, err := readReport(dir); err == nil {
				t.Fatal("incomplete playback identity accepted")
			}
		})
	}
	for _, change := range []string{"frame-count", "start-time", "file"} {
		t.Run("invalid-playback-"+change, func(t *testing.T) {
			dir, report := reportFixture(t)
			switch change {
			case "frame-count":
				report.Playback.Frames--
			case "start-time":
				report.Playback.StartTime = 1.868
			case "file":
				report.Playback.File = "original.mp4"
			}
			if err := lab.AtomicJSON(filepath.Join(dir, "result.json"), report); err != nil {
				t.Fatal(err)
			}
			if _, err := readReport(dir); err == nil {
				t.Fatal("unmatched playback copy accepted")
			}
		})
	}
	for _, field := range []string{"ssim", "cpu_seconds", "encode_seconds", "compared_frames", "width"} {
		t.Run("missing-"+field, func(t *testing.T) {
			dir, report := reportFixture(t)
			data, _ := json.Marshal(report)
			var object map[string]any
			if err := json.Unmarshal(data, &object); err != nil {
				t.Fatal(err)
			}
			delete(object["variants"].([]any)[0].(map[string]any), field)
			if err := lab.AtomicJSON(filepath.Join(dir, "result.json"), object); err != nil {
				t.Fatal(err)
			}
			if _, err := readReport(dir); err == nil {
				t.Fatal("missing measurement turned into a displayed zero")
			}
		})
	}
	for _, name := range []string{"original.mp4", "playback.mp4", "detail-20.mp4", "small-20.mp4"} {
		t.Run("changed-"+name, func(t *testing.T) {
			dir, _ := reportFixture(t)
			if err := os.WriteFile(filepath.Join(dir, name), []byte("other fixture bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readReport(dir); err == nil {
				t.Fatal("old measurements accepted for replaced video")
			}
		})
	}
}
