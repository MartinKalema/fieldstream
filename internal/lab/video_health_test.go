package lab

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func healthTestMedia(t *testing.T) (string, []byte) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("real video-health checks require FFmpeg")
	}
	name := filepath.Join(t.TempDir(), "good.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=12", "-t", "1", "-an", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", "-movflags", "+faststart", name)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate isolated test video: %v %s", err, output)
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return ffmpeg, data
}

func corruptHealthMedia(t *testing.T, original []byte) []byte {
	t.Helper()
	data := bytes.Clone(original)
	for offset := 0; offset+8 <= len(data); {
		size := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		if size < 8 || size > len(data)-offset {
			t.Fatal("unexpected generated MP4 box")
		}
		if string(data[offset+4:offset+8]) == "mdat" {
			clear(data[offset+8 : offset+size]) // Keep the MP4 index, destroy the compressed pictures.
			return data
		}
		offset += size
	}
	t.Fatal("generated MP4 did not contain a media-data box")
	return nil
}

func TestVideoHealthRealMedia(t *testing.T) {
	ffmpeg, good := healthTestMedia(t)
	cases := []struct {
		name, state string
		data        []byte
	}{
		{"complete", "decodable", good},
		{"damaged-pictures-with-valid-index", "decode_error", corruptHealthMedia(t, good)},
		{"truncated-after-index", "decode_error", good[:len(good)/2]},
		{"not-an-mp4", "decode_error", []byte("this file cannot contain a video frame")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, catalog := catalogFixture(t)
			s := writeArchiveSegment(t, p, catalog, tc.data)
			r := checkVideoFile(context.Background(), p, ffmpeg, "test FFmpeg", s, 5*time.Second)
			if r.State != tc.state || (tc.state == "decodable" && r.Frames != 12) {
				t.Fatalf("unexpected real decode result: %+v", r)
			}
			after, err := os.ReadFile(filepath.Join(p.Recordings, s.Path))
			if err != nil || !bytes.Equal(after, tc.data) {
				t.Fatal("checker modified or removed the original bytes")
			}
			t.Logf("%s: %s, %d decoded frames, %s", tc.name, r.State, r.Frames, r.Reason)
		})
	}
}

func TestVideoHealthRefusesChangedAndEscapedFiles(t *testing.T) {
	for _, mode := range []string{"changed", "missing", "outside-symlink"} {
		t.Run(mode, func(t *testing.T) {
			p, catalog := catalogFixture(t)
			s := writeArchiveSegment(t, p, catalog, []byte("cataloged original"))
			name := filepath.Join(p.Recordings, s.Path)
			switch mode {
			case "changed":
				if err := os.WriteFile(name, []byte("different contents"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
			case "outside-symlink":
				outside := filepath.Join(t.TempDir(), "outside.mp4")
				if err := os.Rename(name, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, name); err != nil {
					t.Fatal(err)
				}
			}
			r := checkVideoFile(context.Background(), p, "/not-a-tool", "test", s, time.Second)
			if r.State != "check_failed" || r.Reason == "decoder_could_not_run" {
				t.Fatalf("unsafe input reached decoder or was mislabeled: %+v", r)
			}
		})
	}
}

func healthScript(t *testing.T, body string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "fake-ffmpeg")
	if err := os.WriteFile(name, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestVideoHealthTimeoutAndEmptyOutputCannotPass(t *testing.T) {
	for _, mode := range []string{"timeout", "empty", "missing-progress-end"} {
		t.Run(mode, func(t *testing.T) {
			p, catalog := catalogFixture(t)
			s := writeArchiveSegment(t, p, catalog, []byte("checksum-valid fixture"))
			body := "exit 0\n"
			budget := 3 * time.Second
			if mode == "timeout" {
				body = "exec sleep 30\n"
				budget = 100 * time.Millisecond
			} else if mode == "missing-progress-end" {
				body = "printf 'frame=1\\nprogress=continue\\n'\n"
			}
			started := time.Now()
			r := checkVideoFile(context.Background(), p, healthScript(t, body), "fixture", s, budget)
			if r.State == "decodable" {
				t.Fatalf("incomplete check claimed success: %+v", r)
			}
			if mode == "timeout" && (r.Reason != "check_timed_out_or_canceled" || time.Since(started) > 2*time.Second) {
				t.Fatalf("decoder timeout did not bound the check: %+v", r)
			}
			if mode == "empty" && (r.State != "decode_error" || r.Reason != "video_decode_failed") {
				t.Fatalf("zero frames did not produce a decode failure: %+v", r)
			}
			if mode == "missing-progress-end" && (r.State != "check_failed" || r.Reason != "decoder_did_not_confirm_completion") {
				t.Fatalf("partial progress did not produce an incomplete check: %+v", r)
			}
		})
	}
}

func TestVideoHealthSavedBatchesPreserveArchiveHistory(t *testing.T) {
	ffmpeg, data := healthTestMedia(t)
	p, catalog := catalogFixture(t)
	first := writeArchiveSegment(t, p, catalog, data)
	second := first
	second.SourceID, second.Session, second.Path = "camera-01", "session-two", "session-two/00000.mp4"
	if err := os.MkdirAll(filepath.Join(p.Recordings, second.Session), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Recordings, second.Path), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := catalog.UpsertSegment(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.claimUpload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := catalog.uploadSucceeded(context.Background(), first.Path, "existing-object"); err != nil {
		t.Fatal(err)
	}
	before, err := catalog.ListSegments(context.Background(), 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	for pass := 1; pass <= 3; pass++ {
		report, checked, err := p.CheckRecordingHealth(context.Background(), ffmpeg, VideoHealthOptions{Limit: 1})
		wantChecked, wantDecoded := 1, pass
		if pass == 3 {
			wantChecked, wantDecoded = 0, 2
		}
		if err != nil || checked != wantChecked || report.Summary.Decodable != wantDecoded || report.Summary.Unchecked != 2-wantDecoded {
			t.Fatalf("batch %d: checked=%d report=%+v err=%v", pass, checked, report, err)
		}
	}
	filtered, err := p.RecordingHealth(context.Background(), "camera-01")
	if err != nil || filtered.Summary.Total != 1 || len(filtered.Results) != 1 || filtered.Results[0].SourceID != "camera-01" {
		t.Fatalf("source filter lost recording identity: %+v %v", filtered, err)
	}
	rechecked, checked, err := p.CheckRecordingHealth(context.Background(), ffmpeg, VideoHealthOptions{Limit: 1, Path: second.Path, Recheck: true})
	if err != nil || checked != 1 || rechecked.Summary.Decodable != 2 {
		t.Fatalf("explicit recheck failed: %+v %d %v", rechecked, checked, err)
	}
	if _, _, err := p.CheckRecordingHealth(context.Background(), ffmpeg, VideoHealthOptions{Limit: 1, Path: "../outside.mp4"}); err == nil {
		t.Fatal("uncataloged path selector was accepted")
	}
	after, err := catalog.ListSegments(context.Background(), 100, 0)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("video-health checks changed catalog or upload state")
	}
	info, err := os.Stat(p.VideoHealthPath())
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("video-health report is not private")
	}
}

func TestVideoHealthCancelAndInvalidReport(t *testing.T) {
	p, catalog := catalogFixture(t)
	s := writeArchiveSegment(t, p, catalog, []byte("unchanged fixture"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, n, err := p.CheckRecordingHealth(ctx, "/unused", VideoHealthOptions{Limit: 1})
	if !errors.Is(err, context.Canceled) || n != 0 {
		t.Fatalf("canceled check should leave work untouched: %d %v", n, err)
	}
	if _, err := os.Stat(p.VideoHealthPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled check unexpectedly saved a report")
	}
	invalid := []byte(`{"version":99,"results":[]}`)
	if err := os.WriteFile(p.VideoHealthPath(), invalid, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.CheckRecordingHealth(context.Background(), "/unused", VideoHealthOptions{Limit: 1}); err == nil {
		t.Fatal("invalid report was silently overwritten")
	}
	after, _ := os.ReadFile(p.VideoHealthPath())
	if !bytes.Equal(after, invalid) {
		t.Fatal("invalid report was modified")
	}
	data, _ := os.ReadFile(filepath.Join(p.Recordings, s.Path))
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != s.SHA256 {
		t.Fatal("cancellation changed recording bytes")
	}
}

func TestVideoHealthExcludesUncatalogedFiles(t *testing.T) {
	p, _ := catalogFixture(t)
	if err := os.MkdirAll(p.Recordings, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Recordings, "unfinished.mp4"), []byte("unfinished"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := p.RecordingHealth(context.Background(), "")
	if err != nil || report.Summary.Total != 0 {
		t.Fatalf("uncataloged file entered health inventory: %+v %v", report, err)
	}
	lock, err := fileLock(filepath.Join(p.Local, "video-health.lock"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, _, err := p.CheckRecordingHealth(context.Background(), "/unused", VideoHealthOptions{Limit: 1}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("concurrent report writer was not rejected: %v", err)
	}
}
