package lab

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func waitMonitorCondition(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("recording monitor did not reach the expected state")
}

func TestArchiveMonitorRestartsAfterCatalogRecovery(t *testing.T) {
	p, c := catalogFixture(t)
	if err := os.MkdirAll(p.Recordings, 0700); err != nil {
		t.Fatal(err)
	}
	if err := AtomicJSON(filepath.Join(p.Local, "r2.json"), testR2Config()); err != nil {
		t.Fatal(err)
	}
	// Only the archive startup metadata is unavailable. There are no recording
	// objects, so this test never makes a request to R2 or another network service.
	if _, err := c.db.Exec(`DROP TABLE catalog_metadata`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	monitor := &recordingMonitor{done: make(chan struct{}), sourceStates: map[string]RecordingState{}}
	go monitor.run(ctx, p)
	t.Cleanup(func() {
		cancel()
		select {
		case <-monitor.done:
		case <-time.After(5 * time.Second):
			t.Error("monitor did not stop")
		}
	})
	waitMonitorCondition(t, 5*time.Second, func() bool {
		state := monitor.snapshotArchive()
		return state.Enabled && !state.Running && state.LastError != ""
	})
	if _, err := c.db.Exec(`CREATE TABLE catalog_metadata (name TEXT PRIMARY KEY,value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	waitMonitorCondition(t, 12*time.Second, func() bool { return monitor.snapshotArchive().Running })
	if state := monitor.snapshotArchive(); state.LastError != "" {
		t.Fatalf("recovered worker retained its old terminal error: %+v", state)
	}
}
