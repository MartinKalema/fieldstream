package lab

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const recoveryHelperPrefix = "FIELD_VIDEO_ARCHIVE_RECOVERY_"

// These tests run the production uploader in a separate, forcibly killed
// process. The HTTP store survives that process, as a remote service would.
// They prove process-crash recovery, not physical disk/power-loss durability or
// Cloudflare availability. All credentials, files and endpoints are fixtures.
func TestArchiveRecoveryAfterProcessKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this test asserts Unix SIGKILL semantics")
	}
	for _, boundary := range []string{"partial-put", "stored-put-response-withheld", "head-accepted-before-catalog-save"} {
		t.Run(boundary, func(t *testing.T) {
			p, catalog := catalogFixture(t)
			data := bytes.Repeat([]byte("deterministic archived recording\n"), 1<<16)
			segment := writeArchiveSegment(t, p, catalog, data)
			if err := catalog.Close(); err != nil {
				t.Fatal(err)
			}
			store := newRecoveryStore(t, p, segment, data, boundary)
			first := startRecoveryChild(t, p, store.server.URL, "crash", boundary)
			select {
			case <-store.gate:
			case <-first.done:
				t.Fatalf("uploader exited before reaching crash boundary: %v %s", first.err, first.output.String())
			case <-time.After(10 * time.Second):
				t.Fatal("uploader did not reach the deterministic crash boundary")
			}
			first.kill(t)
			store.releaseWaiters()
			select {
			case <-store.firstPutDone:
			case <-time.After(3 * time.Second):
				t.Fatal("first PUT did not finish after uploader death")
			}
			if err := store.releaseCatalogLock(); err != nil {
				t.Fatal(err)
			}
			before := store.snapshot()
			wantHeads, wantObjects := 0, 1
			if boundary == "partial-put" {
				wantObjects = 0
				if before.firstBodyBytes <= 0 || before.firstBodyBytes >= segment.Bytes || !before.firstBodyFailed {
					t.Fatalf("test did not interrupt a partial transfer: read=%d expected=%d failed=%v", before.firstBodyBytes, segment.Bytes, before.firstBodyFailed)
				}
			} else if before.firstBodyBytes != segment.Bytes || before.firstBodyFailed {
				t.Fatal("complete-object crash boundary was not reached")
			}
			if boundary == "head-accepted-before-catalog-save" {
				wantHeads = 1
			}
			if before.puts != 1 || before.heads != wantHeads || len(before.objects) != wantObjects || len(before.failures) != 0 {
				t.Fatalf("unexpected pre-restart store: PUTs=%d HEADs=%d objects=%d failures=%v", before.puts, before.heads, len(before.objects), before.failures)
			}
			assertRecoveryCatalog(t, p, segment, false)
			assertRecoveryLocalFile(t, p, segment, data)

			// No direct recoverUploads call, retry-time reset or archive.lock removal:
			// the new process must discover and repair the durable uploading row.
			replacement := startRecoveryChild(t, p, store.server.URL, "recover", boundary)
			replacement.waitSuccess(t)
			assertRecoveryCatalog(t, p, segment, true)
			assertRecoveryLocalFile(t, p, segment, data)
			after := store.snapshot()
			if after.puts != 2 || after.heads != wantHeads+1 || len(after.objects) != 1 || len(after.failures) != 0 {
				t.Fatalf("unexpected recovered store: PUTs=%d HEADs=%d objects=%d failures=%v", after.puts, after.heads, len(after.objects), after.failures)
			}
			object, ok := after.objects[store.expectedPath]
			if !ok || !bytes.Equal(object.body, data) {
				t.Fatal("recovery did not leave one complete object at the original key")
			}

			// A third normal startup must finish its first queue scan and expose
			// the archived summary without another network PUT or HEAD.
			startRecoveryChild(t, p, store.server.URL, "idle", boundary).waitSuccess(t)
			last := store.snapshot()
			if last.puts != after.puts || last.heads != after.heads || len(last.failures) != 0 {
				t.Fatal("restarting an already completed archive repeated a network operation")
			}
			assertRecoveryCatalog(t, p, segment, true)
			t.Logf("SIGKILL at %s: first PUT received %d/%d bytes; recovery used 2 PUT attempts, 1 object; third startup performed no network operations", boundary, before.firstBodyBytes, segment.Bytes)
		})
	}
}

func assertRecoveryCatalog(t *testing.T, p Paths, segment Segment, archived bool) {
	t.Helper()
	c, err := OpenCatalog(p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	row, err := c.GetSegment(context.Background(), segment.Path)
	if err != nil {
		t.Fatal(err)
	}
	if row.Segment != segment || row.Missing {
		t.Fatalf("recording identity changed across process death: %+v", row)
	}
	summary, err := c.Summary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Segments != 1 || summary.LocalSegments != 1 || summary.LocalBytes != segment.Bytes || summary.Missing != 0 {
		t.Fatalf("catalog lost or duplicated its recording: %+v", summary)
	}
	if !archived {
		if row.UploadState != "uploading" || row.Attempts != 1 || row.ArchivedAt != nil || row.ObjectKey != "" || summary.Pending != 1 || summary.Archived != 0 {
			t.Fatalf("crash falsely completed or lost the pending upload: %+v, %+v", row, summary)
		}
		return
	}
	expectedKey := "fixture/" + segment.Session + "/00000-" + segment.SHA256 + ".mp4"
	if row.UploadState != "archived" || row.Attempts != 2 || row.ArchivedAt == nil || row.ObjectKey != expectedKey || row.LastError != "" || summary.Pending != 0 || summary.Archived != 1 || summary.ArchivedBytes != segment.Bytes {
		t.Fatalf("restarted uploader did not durably confirm the original object: %+v, %+v", row, summary)
	}
}

func assertRecoveryLocalFile(t *testing.T, p Paths, segment Segment, expected []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(p.Recordings, segment.Path))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if !bytes.Equal(data, expected) || int64(len(data)) != segment.Bytes || hex.EncodeToString(digest[:]) != segment.SHA256 {
		t.Fatal("upload recovery changed or removed the local recording")
	}
}

type recoveryObject struct {
	body     []byte
	metadata map[string]string
	md5      string
}

type recoveryStoreState struct {
	puts, heads     int
	firstBodyBytes  int64
	firstBodyFailed bool
	objects         map[string]recoveryObject
	failures        []string
}

type recoveryStore struct {
	mu             sync.Mutex
	state          recoveryStoreState
	server         *httptest.Server
	paths          Paths
	segment        Segment
	data           []byte
	boundary       string
	expectedPath   string
	gate           chan struct{}
	gateOnce       sync.Once
	release        chan struct{}
	releaseOnce    sync.Once
	firstPutDone   chan struct{}
	lockCatalog    *Catalog
	lockConnection *sql.Conn
}

func newRecoveryStore(t *testing.T, p Paths, segment Segment, data []byte, boundary string) *recoveryStore {
	t.Helper()
	s := &recoveryStore{paths: p, segment: segment, data: data, boundary: boundary,
		expectedPath: "/fixture-bucket/fixture/" + segment.Session + "/00000-" + segment.SHA256 + ".mp4",
		gate:         make(chan struct{}), release: make(chan struct{}), firstPutDone: make(chan struct{}),
		state: recoveryStoreState{objects: map[string]recoveryObject{}},
	}
	s.server = httptest.NewServer(http.HandlerFunc(s.serveHTTP))
	t.Cleanup(func() {
		s.releaseWaiters()
		s.server.CloseClientConnections()
		s.server.Close()
		if err := s.releaseCatalogLock(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func (s *recoveryStore) releaseWaiters() { s.releaseOnce.Do(func() { close(s.release) }) }

func (s *recoveryStore) snapshot() recoveryStoreState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.state
	state.failures = append([]string(nil), s.state.failures...)
	state.objects = make(map[string]recoveryObject, len(s.state.objects))
	for key, object := range s.state.objects {
		state.objects[key] = object // Committed bodies and metadata never mutate.
	}
	return state
}

func (s *recoveryStore) fail(message string) {
	s.mu.Lock()
	s.state.failures = append(s.state.failures, message)
	s.mu.Unlock()
}

func (s *recoveryStore) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && r.URL.Path == "/__test/head-accepted" && s.boundary == "head-accepted-before-catalog-save" {
		// The child sends this only after the real SDK has parsed a successful
		// HEAD. The SQLite lock already prevents the following confirmation.
		w.WriteHeader(http.StatusNoContent)
		w.(http.Flusher).Flush()
		s.gateOnce.Do(func() { close(s.gate) })
		return
	}
	if r.URL.Path != s.expectedPath || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=fixture-access-key/") || r.Header.Get("X-Amz-Security-Token") != "" {
		s.fail("request used an unexpected key or credentials")
		http.Error(w, "fixture request rejected", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.mu.Lock()
		s.state.puts++
		attempt := s.state.puts
		s.mu.Unlock()
		if attempt == 1 {
			defer close(s.firstPutDone)
		}
		var prefix []byte
		var err error
		if attempt == 1 && s.boundary == "partial-put" {
			prefix = make([]byte, 32<<10)
			var n int
			n, err = io.ReadFull(r.Body, prefix)
			prefix = prefix[:n]
			if err == nil {
				s.gateOnce.Do(func() { close(s.gate) })
			}
		}
		rest, readErr := io.ReadAll(io.LimitReader(r.Body, int64(len(s.data))+1))
		body := append(prefix, rest...)
		err = errors.Join(err, readErr)
		if attempt == 1 {
			s.mu.Lock()
			s.state.firstBodyBytes, s.state.firstBodyFailed = int64(len(body)), err != nil
			s.mu.Unlock()
		}
		if err != nil || int64(len(body)) != r.ContentLength {
			if !(attempt == 1 && s.boundary == "partial-put") {
				s.fail("unexpected incomplete PUT")
			}
			return // An incomplete request never becomes a committed object.
		}
		sha, md := sha256.Sum256(body), md5.Sum(body)
		metadata := map[string]string{"sha256": r.Header.Get("X-Amz-Meta-Sha256"), "session": r.Header.Get("X-Amz-Meta-Session"), "source-id": r.Header.Get("X-Amz-Meta-Source-Id")}
		if !bytes.Equal(body, s.data) || r.ContentLength != s.segment.Bytes || hex.EncodeToString(sha[:]) != s.segment.SHA256 || r.Header.Get("Content-MD5") != base64.StdEncoding.EncodeToString(md[:]) || metadata["sha256"] != s.segment.SHA256 || metadata["session"] != s.segment.Session || metadata["source-id"] != s.segment.SourceID {
			s.fail("PUT body, checksum or source identity differs from the finished recording")
			http.Error(w, "fixture integrity check failed", http.StatusBadRequest)
			return
		}
		object := recoveryObject{body: body, metadata: metadata, md5: hex.EncodeToString(md[:])}
		s.mu.Lock()
		s.state.objects[r.URL.Path] = object
		s.mu.Unlock()
		if attempt == 1 && s.boundary == "stored-put-response-withheld" {
			s.gateOnce.Do(func() { close(s.gate) })
			select {
			case <-s.release:
			case <-r.Context().Done():
			}
			return // No response headers or body were sent before process death.
		}
		w.Header().Set("ETag", `"`+object.md5+`"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodHead:
		s.mu.Lock()
		s.state.heads++
		attempt := s.state.heads
		object, exists := s.state.objects[r.URL.Path]
		s.mu.Unlock()
		if !exists {
			s.fail("HEAD preceded a committed object")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if attempt == 1 && s.boundary == "head-accepted-before-catalog-save" {
			if err := s.holdCatalogLock(); err != nil {
				s.fail("could not establish the catalog write barrier")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		// Respond from the stored object, not the expected fixture values.
		w.Header().Set("Content-Length", strconv.Itoa(len(object.body)))
		w.Header().Set("ETag", `"`+object.md5+`"`)
		for key, value := range object.metadata {
			w.Header().Set("X-Amz-Meta-"+key, value)
		}
		w.WriteHeader(http.StatusOK)
	default:
		s.fail("unexpected object-store operation")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *recoveryStore) holdCatalogLock() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	s.lockCatalog, err = OpenCatalog(s.paths)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.lockConnection, err = s.lockCatalog.db.Conn(ctx)
	if err == nil {
		_, err = s.lockConnection.ExecContext(ctx, "BEGIN IMMEDIATE")
	}
	return err
}

func (s *recoveryStore) releaseCatalogLock() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.lockConnection != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err = s.lockConnection.ExecContext(ctx, "ROLLBACK")
		cancel()
		err = errors.Join(err, s.lockConnection.Close())
		s.lockConnection = nil
	}
	if s.lockCatalog != nil {
		err = errors.Join(err, s.lockCatalog.Close())
		s.lockCatalog = nil
	}
	return err
}

type recoveryChild struct {
	command *exec.Cmd
	done    chan struct{}
	err     error
	output  bytes.Buffer
}

func startRecoveryChild(t *testing.T, p Paths, endpoint, mode, boundary string) *recoveryChild {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestArchiveRecoveryProcessHelper$")
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, recoveryHelperPrefix) {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env, recoveryHelperPrefix+"ROOT="+p.Root, recoveryHelperPrefix+"ENDPOINT="+endpoint, recoveryHelperPrefix+"MODE="+mode, recoveryHelperPrefix+"BOUNDARY="+boundary)
	child := &recoveryChild{command: command, done: make(chan struct{})}
	command.Stdout, command.Stderr = &child.output, &child.output
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	go func() { child.err = command.Wait(); close(child.done) }()
	t.Cleanup(func() {
		cancel() // Kills only the exact child created above if still running.
		select {
		case <-child.done:
		case <-time.After(3 * time.Second):
			t.Error("archive test subprocess did not stop")
		}
	})
	return child
}

func (c *recoveryChild) kill(t *testing.T) {
	t.Helper()
	if err := c.command.Process.Kill(); err != nil {
		t.Fatalf("could not SIGKILL uploader at barrier: %v", err)
	}
	select {
	case <-c.done:
	case <-time.After(3 * time.Second):
		t.Fatal("SIGKILL uploader did not exit")
	}
	var exit *exec.ExitError
	if !errors.As(c.err, &exit) {
		t.Fatalf("expected process death, got %v %s", c.err, c.output.String())
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("uploader did not die from SIGKILL: %v %s", c.err, c.output.String())
	}
}

func (c *recoveryChild) waitSuccess(t *testing.T) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(12 * time.Second):
		t.Fatal("restarted archive subprocess did not finish")
	}
	if c.err != nil {
		t.Fatalf("archive subprocess failed: %v %s", c.err, c.output.String())
	}
}

// This test is selected explicitly in child processes. It never reads R2
// settings. The real SDK is restricted to the one parent-owned loopback server.
func TestArchiveRecoveryProcessHelper(t *testing.T) {
	root := os.Getenv(recoveryHelperPrefix + "ROOT")
	if root == "" {
		return
	}
	endpoint := os.Getenv(recoveryHelperPrefix + "ENDPOINT")
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		t.Fatal("archive recovery helper requires its isolated loopback endpoint")
	}
	transport := &http.Transport{ResponseHeaderTimeout: 5 * time.Second, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != u.Host {
			return nil, errors.New("archive recovery helper refused a non-fixture connection")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
	}}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	p := NewPaths(root)
	catalog, err := OpenCatalog(p)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	mode, boundary := os.Getenv(recoveryHelperPrefix+"MODE"), os.Getenv(recoveryHelperPrefix+"BOUNDARY")
	config := testR2Config()
	if mode == "crash" && boundary == "partial-put" {
		// The first 32 KiB arrives in about 125 ms; the whole body needs eight
		// seconds. The parent also verifies that death actually truncated it.
		config.BytesPerSecond = 256 << 10
	}
	archiver, err := NewArchiver(p, catalog, config)
	if err != nil {
		t.Fatal(err)
	}
	archiver.client = newR2Client(config, endpoint, httpClient)
	if mode == "crash" && boundary == "head-accepted-before-catalog-save" {
		archiver.client = recoveryObservedHEAD{objectArchive: archiver.client, client: httpClient, endpoint: endpoint}
	}
	if mode != "idle" && mode != "crash" && mode != "recover" {
		t.Fatal("invalid archive recovery helper mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	monitorDone := make(chan struct{})
	ready := false // Read only after monitorDone closes.
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if mode == "recover" || mode == "idle" {
					state := archiver.Snapshot()
					if state.Running && state.Summary.Archived == 1 && state.Summary.Pending == 0 {
						ready = true
						cancel()
						return
					}
				}
			}
		}
	}()
	err = archiver.Run(ctx)
	cancel()
	<-monitorDone
	if err != nil {
		t.Fatal(err)
	}
	if mode == "crash" {
		t.Fatal("uploader escaped the intended SIGKILL boundary")
	}
	if !ready {
		t.Fatal("uploader did not finish a queue scan and expose its archived summary")
	}
	summary, err := catalog.Summary(context.Background())
	if err != nil || summary.Archived != 1 || summary.Pending != 0 {
		t.Fatalf("normal startup did not preserve completed archive: %+v %v", summary, err)
	}
}

type recoveryObservedHEAD struct {
	objectArchive
	client   *http.Client
	endpoint string
}

func (c recoveryObservedHEAD) HeadObject(ctx context.Context, input *s3.HeadObjectInput, options ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	result, err := c.objectArchive.HeadObject(ctx, input, options...)
	if err != nil {
		return result, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/__test/head-accepted", nil)
	if err != nil {
		return nil, err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return nil, fmt.Errorf("archive recovery barrier status %d", response.StatusCode)
	}
	return result, nil
}
