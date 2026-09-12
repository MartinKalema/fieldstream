package lab

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Reserve SQLite's only connection, wait until Run is observably blocked on
// that connection, then cancel. This exercises both startup and the later
// queue-recovery loop without mistaking a normal shutdown for disk failure.
func TestArchiveCancellationDuringCatalogWait(t *testing.T) {
	for _, phase := range []string{"startup", "queue-recovery"} {
		t.Run(phase, func(t *testing.T) {
			p, catalog := catalogFixture(t)
			if phase == "queue-recovery" {
				segment := writeArchiveSegment(t, p, catalog, []byte("already archived fixture bytes"))
				if _, err := catalog.claimUpload(context.Background()); err != nil {
					t.Fatal(err)
				}
				if err := catalog.uploadSucceeded(context.Background(), segment.Path, "existing-object"); err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("a catalog-only cancellation test attempted a network operation")
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			archiver, err := NewArchiver(p, catalog, testR2Config())
			if err != nil {
				t.Fatal(err)
			}
			archiver.client = newR2Client(testR2Config(), server.URL, server.Client())
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			var runError error
			var reserved *sql.Conn
			var waitBaseline int64
			t.Cleanup(func() {
				cancel()
				if reserved != nil {
					reserved.Close()
				}
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("canceled archive worker did not stop")
				}
			})
			reserve := func() {
				deadline, stop := context.WithTimeout(context.Background(), time.Second)
				defer stop()
				reserved, err = catalog.db.Conn(deadline)
				if err != nil {
					t.Fatal(err)
				}
				waitBaseline = catalog.db.Stats().WaitCount
			}
			if phase == "startup" {
				reserve()
			}
			go func() { runError = archiver.Run(ctx); close(done) }()
			if phase == "queue-recovery" {
				waitMonitorCondition(t, 3*time.Second, func() bool {
					state := archiver.Snapshot()
					return state.Running && state.Summary.Archived == 1
				})
				reserve()
			}
			// Exclude any wait needed by reserve itself. For startup, the
			// baseline was captured before Run was launched.
			waitMonitorCondition(t, 4*time.Second, func() bool { return catalog.db.Stats().WaitCount > waitBaseline })
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("cancellation did not interrupt the catalog connection wait")
			}
			if runError != nil || archiver.Snapshot().LastError != "" || archiver.Snapshot().Running {
				t.Fatalf("normal cancellation was reported as an archive failure: error=%v state=%+v", runError, archiver.Snapshot())
			}
		})
	}
}
