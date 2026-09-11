package lab

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/smithy-go"
)

func testR2Config() R2Config {
	return R2Config{Enabled: true, AccountID: strings.Repeat("a", 32), Bucket: "fixture-bucket", AccessKeyID: "fixture-access-key", SecretAccessKey: "fixture-secret-key-not-real", Prefix: "fixture", BytesPerSecond: 100_000_000}
}

func writeArchiveSegment(t *testing.T, p Paths, c *Catalog, data []byte) Segment {
	t.Helper()
	sum := sha256.Sum256(data)
	s := Segment{Path: "session-one/00000.mp4", Session: "session-one", SourceID: "camera-02", Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Duration: 5}
	if err := os.MkdirAll(filepath.Join(p.Recordings, s.Session), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Recordings, s.Path), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.UpsertSegment(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestArchiveDisabledAndPrivateTemplate(t *testing.T) {
	p, c := catalogFixture(t)
	config, err := LoadR2Config(p)
	if err != nil || config != nil {
		t.Fatalf("missing config: %+v %v", config, err)
	}
	name, err := p.WriteR2Template()
	if err != nil {
		t.Fatal(err)
	}
	config, err = LoadR2Config(p)
	if err != nil || config.Enabled {
		t.Fatalf("template enabled or invalid: %+v %v", config, err)
	}
	before, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.WriteR2Template(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(name)
	if !bytes.Equal(before, after) {
		t.Fatal("template overwrote private configuration")
	}
	a, err := NewArchiver(p, c, *config)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.Snapshot().Running || a.Snapshot().Enabled {
		t.Fatal("disabled archive worker became active")
	}
	if err = os.Chmod(name, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadR2Config(p); err == nil {
		t.Fatal("world-readable credentials accepted")
	}
}

func TestArchiveSDKRetryUsesSameObjectAndConfirmsChecksum(t *testing.T) {
	p, c := catalogFixture(t)
	data := bytes.Repeat([]byte("video-fixture"), 1000)
	s := writeArchiveSegment(t, p, c, data)
	var mu sync.Mutex
	puts, heads := 0, 0
	objects := map[string][]byte{}
	config := testR2Config()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential="+config.AccessKeyID+"/") {
			t.Error("request was not signed using the configured credentials")
		}
		if r.Header.Get("X-Amz-Security-Token") != "" {
			t.Error("unexpected ambient credential token")
		}
		switch r.Method {
		case http.MethodPut:
			puts++
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				http.Error(w, "read failed", 400)
				return
			}
			checksum := md5.Sum(body)
			if r.Header.Get("Content-MD5") != base64.StdEncoding.EncodeToString(checksum[:]) || !bytes.Equal(body, data) {
				t.Error("transferred payload or Content-MD5 mismatch")
			}
			if r.Header.Get("X-Amz-Meta-Sha256") != s.SHA256 || r.Header.Get("X-Amz-Meta-Session") != s.Session || r.Header.Get("X-Amz-Meta-Source-Id") != s.SourceID {
				t.Error("identity metadata missing")
			}
			if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
				t.Error("unsupported automatic checksum framing enabled")
			}
			objects[r.URL.Path] = body
			if puts == 1 {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(503)
				fmt.Fprint(w, "<Error><Code>ServiceUnavailable</Code><Message>response lost after storing fixture</Message></Error>")
				return
			}
			w.WriteHeader(200)
		case http.MethodHead:
			heads++
			if _, ok := objects[r.URL.Path]; !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			w.Header().Set("X-Amz-Meta-Sha256", s.SHA256)
			w.Header().Set("X-Amz-Meta-Session", s.Session)
			w.Header().Set("X-Amz-Meta-Source-Id", s.SourceID)
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected operation: %s", r.Method)
			w.WriteHeader(405)
		}
	}))
	defer server.Close()
	a, err := NewArchiver(p, c, config)
	if err != nil {
		t.Fatal(err)
	}
	a.client = newR2Client(config, server.URL, server.Client())
	ctx := context.Background()
	if worked, err := a.uploadNext(ctx); !worked || err == nil {
		t.Fatalf("expected first attempt failure: worked=%v err=%v", worked, err)
	}
	first, err := c.GetSegment(ctx, s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if first.UploadState != "pending" || first.Attempts != 1 {
		t.Fatalf("lost retry state: %+v", first)
	}
	if _, err = c.db.Exec(`UPDATE recordings SET next_attempt_at=0`); err != nil {
		t.Fatal(err)
	}
	if worked, err := a.uploadNext(ctx); !worked || err != nil {
		t.Fatalf("retry failed: worked=%v err=%v", worked, err)
	}
	second, err := c.GetSegment(ctx, s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if second.UploadState != "archived" || second.Attempts != 2 || second.ArchivedAt == nil || !strings.Contains(second.ObjectKey, s.SHA256) {
		t.Fatalf("unconfirmed archive: %+v", second)
	}
	mu.Lock()
	defer mu.Unlock()
	if puts != 2 || heads != 1 || len(objects) != 1 {
		t.Fatalf("unsafe retry: puts=%d heads=%d distinct objects=%d", puts, heads, len(objects))
	}
}

func TestArchiveRefusesChangedLocalFileBeforeNetwork(t *testing.T) {
	p, c := catalogFixture(t)
	s := writeArchiveSegment(t, p, c, []byte("original video bytes"))
	if err := os.WriteFile(filepath.Join(p.Recordings, s.Path), []byte("modified video bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(200) }))
	defer server.Close()
	a, err := NewArchiver(p, c, testR2Config())
	if err != nil {
		t.Fatal(err)
	}
	a.client = newR2Client(testR2Config(), server.URL, server.Client())
	if _, err = a.uploadNext(context.Background()); err == nil {
		t.Fatal("changed file archived")
	}
	if calls != 0 {
		t.Fatal("changed file was sent to server")
	}
	stored, err := c.GetSegment(context.Background(), s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.UploadState == "archived" || !strings.Contains(stored.LastError, "SHA-256 changed") {
		t.Fatalf("wrong integrity result: %+v", stored)
	}
}

func TestArchiveHeadMismatchNeverMarksArchived(t *testing.T) {
	p, c := catalogFixture(t)
	s := writeArchiveSegment(t, p, c, []byte("original video"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(200)
			return
		}
		w.Header().Set("Content-Length", "100")
		w.Header().Set("X-Amz-Meta-Sha256", "wrong")
		w.WriteHeader(200)
	}))
	defer server.Close()
	a, err := NewArchiver(p, c, testR2Config())
	if err != nil {
		t.Fatal(err)
	}
	a.client = newR2Client(testR2Config(), server.URL, server.Client())
	if _, err = a.uploadNext(context.Background()); err == nil {
		t.Fatal("mismatched confirmation accepted")
	}
	stored, err := c.GetSegment(context.Background(), s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.UploadState == "archived" {
		t.Fatal("mismatched object marked archived")
	}
}

func TestArchivePacingCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &pacedReadSeeker{ctx: ctx, source: bytes.NewReader(make([]byte, 32768)), bytesPerSecond: 16384}
	done := make(chan error, 1)
	go func() { _, err := reader.Read(make([]byte, 32768)); done <- err }()
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("unexpected cancellation: %v", err)
		}
		if time.Since(start) > time.Second {
			t.Fatal("slow cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("rate limiter ignored shutdown")
	}
}

func TestArchiveErrorsDoNotExposeRemoteMessagesOrCredentials(t *testing.T) {
	secret := "fixture-secret-value"
	err := &smithy.GenericAPIError{Code: "AccessDenied", Message: "request URL and secret " + secret}
	if strings.Contains(safeArchiveError(err), secret) {
		t.Fatal("secret escaped in API error")
	}
	p := NewPaths(t.TempDir())
	if err := os.MkdirAll(p.Local, 0700); err != nil {
		t.Fatal(err)
	}
	config := testR2Config()
	config.AccountID = secret
	data, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(p.Local, "r2.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadR2Config(p); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe config error: %v", err)
	}
}

func TestArchiveRejectsSymlinkOutsideRecordings(t *testing.T) {
	p, c := catalogFixture(t)
	s := writeArchiveSegment(t, p, c, []byte("private video"))
	outside := filepath.Join(p.Root, "outside.mp4")
	if err := os.Rename(filepath.Join(p.Recordings, s.Path), outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(p.Recordings, s.Path)); err != nil {
		t.Fatal(err)
	}
	a, err := NewArchiver(p, c, testR2Config())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
	defer server.Close()
	a.client = newR2Client(testR2Config(), server.URL, server.Client())
	// A root-relative open must fail before any network request.
	if _, err = a.uploadNext(context.Background()); err == nil {
		t.Fatal("outside-root symlink accepted")
	}
	if calls != 0 {
		t.Fatal("outside-root file reached the archive service")
	}
}

func TestArchiveRunCancellationReturnsPendingState(t *testing.T) {
	p, c := catalogFixture(t)
	s := writeArchiveSegment(t, p, c, make([]byte, 256<<10))
	requestStarted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case requestStarted <- struct{}{}:
		default:
		}
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(200)
	}))
	defer server.Close()
	config := testR2Config()
	config.BytesPerSecond = 16384
	a, err := NewArchiver(p, c, config)
	if err != nil {
		t.Fatal(err)
	}
	a.client = newR2Client(config, server.URL, server.Client())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("archive shutdown exceeded two seconds")
	}
	stored, err := c.GetSegment(context.Background(), s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.UploadState != "pending" || stored.Attempts != 1 || !strings.Contains(stored.LastError, "interrupted") {
		t.Fatalf("canceled attempt was not recoverable: %+v", stored)
	}
}

func TestArchiveTerminalFailureAppearsInSnapshot(t *testing.T) {
	p, c := catalogFixture(t)
	if err := c.bindArchiveDestination(context.Background(), "previous/destination"); err != nil {
		t.Fatal(err)
	}
	a, err := NewArchiver(p, c, testR2Config())
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Run(context.Background()); err == nil {
		t.Fatal("changed archive destination was accepted")
	}
	state := a.Snapshot()
	if state.Running || !strings.Contains(state.LastError, "destination changed") {
		t.Fatalf("terminal failure hidden: %+v", state)
	}
}
