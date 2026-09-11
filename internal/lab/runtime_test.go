package lab

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestUnavailableObservationDoesNotStopMedia(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusOK, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v3/paths/get/camera-02" {
					t.Errorf("wrong source requested: %s", r.URL.Path)
				}
				w.WriteHeader(status)
				w.Write([]byte(`{}`)) // Missing fields are not proof that the camera stopped.
			}))
			defer server.Close()
			observed := sourceState(Ports{API: server.Listener.Addr().(*net.TCPAddr).Port}, "camera-02")
			knownAbsent := status == http.StatusNotFound
			if observed.Observed != knownAbsent || mediaWanted(observed, true) == knownAbsent {
				t.Fatalf("unsafe decision for status %d: %+v", status, observed)
			}
			if mediaWanted(observed, false) {
				t.Fatal("failed status check started a previously absent source")
			}
		})
	}
}

func TestMediaLogOpenFailureStillDrainsOutput(t *testing.T) {
	log := mediaLog(filepath.Join(t.TempDir(), "missing-directory", "worker.log"))
	defer log.Close()
	if n, err := log.Write([]byte("worker output")); n != 13 || err != nil || log.errorText() == "" {
		t.Fatalf("failed logging would stop media or hide the error: %d, %v", n, err)
	}
}

func TestControlChangesStayWithinSelectedSource(t *testing.T) {
	p := NewPaths(t.TempDir())
	if err := os.MkdirAll(p.Local, 0700); err != nil {
		t.Fatal(err)
	}
	s := validTestSettings()
	first, err := newSource("camera-01", "First camera")
	if err != nil {
		t.Fatal(err)
	}
	second, err := newSource("camera-02", "Second camera")
	if err != nil {
		t.Fatal(err)
	}
	s.Sources = []SourceConfig{first, second}
	if err := AtomicJSON(filepath.Join(p.Local, "settings.json"), s); err != nil {
		t.Fatal(err)
	}
	if err := p.ChangeSourceControl("camera-02", func(c *Control) { c.RelayEnabled = false; c.Profile = "small" }); err != nil {
		t.Fatal(err)
	}
	got, err := p.loadControls(s)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sources["camera-01"] != defaultControl() || got.Sources["camera-02"].RelayEnabled || got.Sources["camera-02"].Profile != "small" {
		t.Fatalf("source controls leaked: %+v", got)
	}
	if err := p.ChangeSourceControl("unknown", func(c *Control) { c.DemoEnabled = true }); err == nil {
		t.Fatal("unknown source accepted")
	}
	data, err := os.ReadFile(filepath.Join(p.Local, "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted ControlSet
	if json.Unmarshal(data, &persisted) != nil || len(persisted.Sources) != 2 {
		t.Fatal("control registry was damaged")
	}
}
