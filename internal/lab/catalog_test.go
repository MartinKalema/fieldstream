package lab

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func catalogFixture(t *testing.T) (Paths, *Catalog) {
	t.Helper()
	p := NewPaths(t.TempDir())
	c, err := OpenCatalog(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return p, c
}

func sampleSegment(name string) Segment {
	data := []byte("finished recording " + name)
	digest := sha256.Sum256(data)
	return Segment{Path: "session-one/" + name + ".mp4", Session: "session-one", Bytes: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Duration: 5}
}

func TestCatalogDuplicateDoesNotResetArchiveAndRejectsChangedContent(t *testing.T) {
	_, c := catalogFixture(t)
	ctx := context.Background()
	segment := sampleSegment("first")
	if err := c.UpsertSegment(ctx, segment); err != nil {
		t.Fatal(err)
	}
	claimed, err := c.claimUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.uploadSucceeded(ctx, claimed.Path, "stable-object-key"); err != nil {
		t.Fatal(err)
	}
	if err = c.UpsertSegment(ctx, segment); err != nil {
		t.Fatal(err)
	}
	changed := segment
	changed.Bytes++
	if err = c.UpsertSegment(ctx, changed); err == nil {
		t.Fatal("changed file was accepted")
	}
	stored, err := c.GetSegment(ctx, segment.Path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.UploadState != "archived" || stored.ObjectKey != "stable-object-key" || stored.Attempts != 1 || stored.Bytes != segment.Bytes {
		t.Fatalf("history changed: %+v", stored)
	}
	summary, err := c.Summary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Segments != 1 || summary.Archived != 1 || summary.Pending != 0 {
		t.Fatalf("wrong summary: %+v", summary)
	}
}

func TestCatalogConcurrentClaimsAreUnique(t *testing.T) {
	_, c := catalogFixture(t)
	ctx := context.Background()
	const count = 24
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			if err := c.UpsertSegment(ctx, sampleSegment(fmt.Sprintf("%03d", index))); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	results := make(chan string, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := c.claimUpload(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			results <- s.Path
		}()
	}
	wg.Wait()
	close(results)
	seen := map[string]bool{}
	for value := range results {
		if seen[value] {
			t.Errorf("duplicate claim: %s", value)
		}
		seen[value] = true
	}
	if len(seen) != count {
		t.Fatalf("claimed %d of %d", len(seen), count)
	}
}

func TestCatalogWALSurvivesProcessExit(t *testing.T) {
	if root := os.Getenv("FIELD_VIDEO_CATALOG_CRASH_HELPER"); root != "" {
		c, err := OpenCatalog(NewPaths(root))
		if err != nil {
			os.Exit(11)
		}
		if c.UpsertSegment(context.Background(), sampleSegment("committed")) != nil {
			os.Exit(12)
		}
		if _, err = c.claimUpload(context.Background()); err != nil {
			os.Exit(13)
		}
		// Exit without running Close or defers: the committed WAL and interrupted
		// upload state must be recoverable by a new process.
		os.Exit(0)
	}
	p := NewPaths(t.TempDir())
	command := exec.Command(os.Args[0], "-test.run=^TestCatalogWALSurvivesProcessExit$")
	command.Env = append(os.Environ(), "FIELD_VIDEO_CATALOG_CRASH_HELPER="+p.Root)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v %s", err, data)
	}
	c, err := OpenCatalog(p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	before, err := c.GetSegment(ctx, sampleSegment("committed").Path)
	if err != nil {
		t.Fatal(err)
	}
	if before.UploadState != "uploading" || before.Attempts != 1 {
		t.Fatalf("lost committed state: %+v", before)
	}
	if err = c.recoverUploads(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := c.claimUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Path != before.Path || after.SHA256 != before.SHA256 || after.Attempts != 2 {
		t.Fatalf("incorrect recovery: %+v", after)
	}
	var synchronous int
	var mode string
	if err = c.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != 2 {
		t.Fatalf("FULL durability unset: %d %v", synchronous, err)
	}
	if err = c.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("WAL unset: %s %v", mode, err)
	}
}

func TestCatalogMissingPreservesHistoryAndRestoration(t *testing.T) {
	p, c := catalogFixture(t)
	ctx := context.Background()
	s := sampleSegment("missing")
	if err := c.UpsertSegment(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := c.ReconcileMissing(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := c.GetSegment(ctx, s.Path)
	if err != nil || !stored.Missing {
		t.Fatalf("missing not recorded: %+v %v", stored, err)
	}
	if err = os.MkdirAll(filepath.Join(p.Recordings, s.Session), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(p.Recordings, s.Path), []byte("finished recording missing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = c.ReconcileMissing(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err = c.GetSegment(ctx, s.Path)
	if err != nil || stored.Missing {
		t.Fatalf("restoration not recorded: %+v %v", stored, err)
	}
}

func TestCatalogRejectsUnsafePathsAndDifferentArchiveDestination(t *testing.T) {
	_, c := catalogFixture(t)
	ctx := context.Background()
	for _, name := range []string{"../escape.mp4", "/tmp/escape.mp4", "session-one/../escape.mp4", "session-one/other/nested.mp4"} {
		s := sampleSegment("normal")
		s.Path = name
		if err := c.UpsertSegment(ctx, s); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	if err := c.bindArchiveDestination(ctx, "account/bucket/prefix"); err != nil {
		t.Fatal(err)
	}
	if err := c.bindArchiveDestination(ctx, "account/bucket/prefix"); err != nil {
		t.Fatal(err)
	}
	if err := c.bindArchiveDestination(ctx, "other-account/bucket/prefix"); err == nil {
		t.Fatal("archive history transferred to another destination")
	}
}

func TestCatalogRefusesFutureSchemaWithoutDowngrade(t *testing.T) {
	p, c := catalogFixture(t)
	if _, err := c.db.Exec("PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if newer, err := OpenCatalog(p); err == nil {
		newer.Close()
		t.Fatal("future catalog was accepted")
	}
	db, err := sql.Open("sqlite", filepath.Join(p.Local, "recordings.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 99 {
		t.Fatalf("future schema downgraded: %d %v", version, err)
	}
}

func TestCatalogMigratesVersionOneWithoutLosingArchiveHistory(t *testing.T) {
	p := NewPaths(t.TempDir())
	if err := os.MkdirAll(p.Local, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(p.Local, "recordings.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	// This is the shipped schema from the single-camera version, deliberately
	// created independently of the migration under test.
	_, err = db.Exec(`CREATE TABLE recordings (
		path TEXT PRIMARY KEY, session TEXT NOT NULL, bytes INTEGER NOT NULL,
		sha256 TEXT NOT NULL, duration REAL NOT NULL,
		upload_state TEXT NOT NULL DEFAULT 'pending', attempts INTEGER NOT NULL DEFAULT 0,
		last_error TEXT NOT NULL DEFAULT '', object_key TEXT NOT NULL DEFAULT '', archived_at INTEGER,
		missing INTEGER NOT NULL DEFAULT 0, next_attempt_at INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL
	);
	CREATE TABLE catalog_metadata (name TEXT PRIMARY KEY,value TEXT NOT NULL);
	INSERT INTO catalog_metadata VALUES('archive_destination','existing/account/bucket/prefix');
	PRAGMA user_version=1;`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	archived := sampleSegment("archived")
	pending := sampleSegment("pending")
	_, err = db.Exec(`INSERT INTO recordings(path,session,bytes,sha256,duration,upload_state,attempts,object_key,archived_at,created_at)
		VALUES(?,?,?,?,?,'archived',4,'unchanged-object-key',1234567890000,1234567890000)`, archived.Path, archived.Session, archived.Bytes, archived.SHA256, archived.Duration)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO recordings(path,session,bytes,sha256,duration,created_at) VALUES(?,?,?,?,?,1234567890000)`, pending.Path, pending.Session, pending.Bytes, pending.SHA256, pending.Duration)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := OpenCatalog(p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	summary, err := c.Summary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Segments != 2 || summary.Archived != 1 || summary.Pending != 1 || summary.Bytes != archived.Bytes+pending.Bytes {
		t.Fatalf("migration changed counts: %+v", summary)
	}
	stored, err := c.GetSegment(ctx, archived.Path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SourceID != "camera-01" || stored.ObjectKey != "unchanged-object-key" || stored.Attempts != 4 || stored.ArchivedAt == nil || stored.ArchivedAt.UnixMilli() != 1234567890000 {
		t.Fatalf("migration lost identity or history: %+v", stored)
	}
	if err = c.bindArchiveDestination(ctx, "existing/account/bucket/prefix"); err != nil {
		t.Fatal(err)
	}
	if err = c.UpsertSegment(ctx, archived); err != nil {
		t.Fatalf("legacy caller no longer works: %v", err)
	}
	var version int
	if err = c.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatalf("version marker incorrect: %d %v", version, err)
	}
}

func TestCatalogSourceIdentityAndPerSourceSummaries(t *testing.T) {
	_, c := catalogFixture(t)
	ctx := context.Background()
	first := sampleSegment("first")
	second := sampleSegment("second")
	second.SourceID = "camera-02"
	for _, segment := range []Segment{first, second} {
		if err := c.UpsertSegment(ctx, segment); err != nil {
			t.Fatal(err)
		}
	}
	claimed, err := c.claimUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.SourceID != "camera-01" {
		t.Fatalf("empty legacy identity was not normalized: %+v", claimed)
	}
	if err = c.uploadSucceeded(ctx, claimed.Path, "first-object"); err != nil {
		t.Fatal(err)
	}
	if err = c.MarkMissing(ctx, second.Path, true); err != nil {
		t.Fatal(err)
	}
	changed := first
	changed.SourceID = "camera-02"
	if err = c.UpsertSegment(ctx, changed); err == nil {
		t.Fatal("existing recording was moved to a different source")
	}
	for _, sourceID := range []string{"CAMERA-03", "3camera", "camera/name", strings.Repeat("x", 33)} {
		bad := sampleSegment("bad")
		bad.SourceID = sourceID
		if err = c.UpsertSegment(ctx, bad); err == nil {
			t.Errorf("invalid source identity accepted: %q", sourceID)
		}
	}
	summaries, err := c.SummariesBySource(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 {
		t.Fatalf("unexpected sources: %+v", summaries)
	}
	if s := summaries["camera-01"]; s.Segments != 1 || s.Archived != 1 || s.LocalBytes != first.Bytes {
		t.Fatalf("first source totals: %+v", s)
	}
	if s := summaries["camera-02"]; s.Segments != 1 || s.PendingMissing != 1 || s.LocalSegments != 0 {
		t.Fatalf("second source totals: %+v", s)
	}
}

func TestCatalogUploadQueueDoesNotStarveSourcesOrDueRetries(t *testing.T) {
	_, c := catalogFixture(t)
	ctx := context.Background()
	first := sampleSegment("first")
	first.SourceID = "camera-02"
	first.Session = "z-session"
	first.Path = "z-session/000.mp4"
	second := sampleSegment("second")
	second.SourceID = "camera-01"
	second.Session = "a-session"
	second.Path = "a-session/000.mp4"
	for _, segment := range []Segment{first, second} {
		if err := c.UpsertSegment(ctx, segment); err != nil {
			t.Fatal(err)
		}
	}
	claimed, err := c.claimUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Path != first.Path {
		t.Fatalf("source name overrode queue arrival order: %+v", claimed)
	}
	if err = c.uploadFailed(ctx, first.Path, "retry fixture", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	retry, err := c.claimUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Path != first.Path || retry.Attempts != 2 {
		t.Fatalf("due retry was starved by a new upload: %+v", retry)
	}
	if err = c.uploadSucceeded(ctx, retry.Path, "first-object"); err != nil {
		t.Fatal(err)
	}
	next, err := c.claimUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if next.SourceID != second.SourceID {
		t.Fatalf("second source did not progress: %+v", next)
	}
}
