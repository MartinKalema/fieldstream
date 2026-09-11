package lab

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func recordingFixture(t *testing.T) (Paths, *Catalog, string) {
	t.Helper()
	p := NewPaths(t.TempDir())
	dir := filepath.Join(p.Recordings, "20260911-120000-000000000000000000000000")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cat, err := OpenCatalog(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	return p, cat, dir
}

func TestRecordingScannerRetriesFailedCatalogWrite(t *testing.T) {
	p, cat, dir := recordingFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "segment-000000.mp4"), []byte("finished-file-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "segments.csv"), []byte("segment-000000.mp4,0,5\n"), 0600); err != nil {
		t.Fatal(err)
	}
	known := map[string]Segment{}
	cat.Close()
	if _, err := scanRecordings(context.Background(), p, known, cat); err == nil {
		t.Fatal("closed catalog unexpectedly accepted a write")
	}
	if len(known) != 0 {
		t.Fatal("failed transaction was cached, which would prevent recovery")
	}
	reopened, err := OpenCatalog(p)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, err := scanRecordings(context.Background(), p, known, reopened)
	if err != nil {
		t.Fatal(err)
	}
	if state.Segments != 1 || state.Bytes != 19 {
		t.Fatalf("unexpected recovered files: %+v", state)
	}
	if err := os.Remove(filepath.Join(dir, "segment-000000.mp4")); err != nil {
		t.Fatal(err)
	}
	state, err = scanRecordings(context.Background(), p, known, reopened)
	if err != nil {
		t.Fatal(err)
	}
	if state.Segments != 0 || state.Bytes != 0 || len(known) != 0 {
		t.Fatalf("removed file still counted locally: %+v", state)
	}
	summary, err := reopened.Summary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.PendingMissing != 1 {
		t.Fatal("lost unarchived file was silently forgotten")
	}
}

func TestRecordingScannerRejectsUnfinishedAndOutsideFiles(t *testing.T) {
	p, cat, dir := recordingFixture(t)
	for _, name := range []string{"finished.mp4", "active.mp4", "invalid.mp4"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("recording"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(p.Root, "outside.mp4")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "linked.mp4")); err != nil {
		t.Fatal(err)
	}
	list := "finished.mp4,0,5\n../../outside.mp4,0,5\nlinked.mp4,0,5\ninvalid.mp4,NaN,Inf\nactive.mp4,5,10"
	if err := os.WriteFile(filepath.Join(dir, "segments.csv"), []byte(list), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := scanRecordings(context.Background(), p, map[string]Segment{}, cat)
	if err != nil {
		t.Fatal(err)
	}
	if state.Segments != 1 {
		t.Fatalf("accepted an unfinished or unsafe entry: %+v", state)
	}
	if err := os.WriteFile(filepath.Join(dir, "segments.csv"), []byte(list+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	state, err = scanRecordings(context.Background(), p, map[string]Segment{}, cat)
	if err != nil || state.Segments != 2 {
		t.Fatalf("completed row was not retried: %+v, %v", state, err)
	}
}

func TestRecordingQuotaCountsUnfinishedFiles(t *testing.T) {
	p, cat, dir := recordingFixture(t)
	f, err := os.Create(filepath.Join(dir, "active.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	// A sparse private test file models the quota without filling the disk.
	if err = f.Truncate(MaxRecordingBytes); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	state, err := scanRecordings(context.Background(), p, map[string]Segment{}, cat)
	if err != nil {
		t.Fatal(err)
	}
	if state.BlockedReason == nil || !strings.Contains(*state.BlockedReason, "2 GB") {
		t.Fatalf("unfinished file bypassed quota: %+v", state)
	}
	if state.Segments != 0 {
		t.Fatal("unfinished file entered completed catalog")
	}
}

func TestRollingLogStaysBoundedAndDrainsOnWriteFailure(t *testing.T) {
	name := filepath.Join(t.TempDir(), "media.log")
	l, err := newRollingLog(name, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := 0; i < 4; i++ {
		if _, err = l.Write([]byte(strings.Repeat("x", 80))); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{name, name + ".previous"} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > 100 {
			t.Fatalf("log grew beyond cap: %d", info.Size())
		}
	}
	l.file.Close() // Simulate a failed destination while a child keeps writing.
	n, err := l.Write([]byte("more"))
	if n != 4 || err != nil || l.errorText() == "" {
		t.Fatal("failed log would block child output or hide the failure")
	}
}
