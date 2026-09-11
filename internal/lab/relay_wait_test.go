package lab

import (
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRelayWaitMigratesAndChangesOnlySelectedSource(t *testing.T) {
	p := NewPaths(t.TempDir())
	if err := os.MkdirAll(p.Local, 0700); err != nil {
		t.Fatal(err)
	}
	s := validTestSettings()
	first, err := newSource("camera-01", "First")
	if err != nil {
		t.Fatal(err)
	}
	second, err := newSource("camera-02", "Second")
	if err != nil {
		t.Fatal(err)
	}
	s.Sources = []SourceConfig{first, second}
	if err := AtomicJSON(filepath.Join(p.Local, "settings.json"), s); err != nil {
		t.Fatal(err)
	}
	controlPath := filepath.Join(p.Local, "control.json")
	legacy := []byte(`{"sources":{"camera-01":{"relay_enabled":true,"profile":"copy","demo_enabled":false},"camera-02":{"relay_enabled":true,"profile":"small","demo_enabled":false}}}`)
	if err := os.WriteFile(controlPath, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.ChangeSourceControl("camera-01", func(c *Control) { c.RelayWaitMS = 120 }); err != nil {
		t.Fatal(err)
	}
	controls, err := p.loadControls(s)
	if err != nil {
		t.Fatal(err)
	}
	if controls.Sources["camera-01"].RelayWaitMS != 120 || controls.Sources["camera-02"].RelayWaitMS != 300 || controls.Sources["camera-02"].Profile != "small" {
		t.Fatal("migration or source isolation failed")
	}
	before, err := os.ReadFile(controlPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []int{-1, 0, 119, 121, 301, 300000} {
		if err := p.ChangeSourceControl("camera-01", func(c *Control) { c.RelayWaitMS = invalid }); err == nil {
			t.Fatalf("accepted wait %d", invalid)
		}
	}
	after, err := os.ReadFile(controlPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rejected changes modified saved controls")
	}
}

func TestRelayWaitChangesProtocolUnitsWithoutChangingEncryption(t *testing.T) {
	s := validTestSettings()
	normal := defaultControl()
	fast := normal
	fast.RelayWaitMS = 120
	before := relayCommand(s, normal)
	after := relayCommand(s, fast)
	if !reflect.DeepEqual(before[:len(before)-1], after[:len(after)-1]) {
		t.Fatal("wait changed video processing")
	}
	a, err := url.Parse(before[len(before)-1])
	if err != nil {
		t.Fatal(err)
	}
	b, err := url.Parse(after[len(after)-1])
	if err != nil {
		t.Fatal(err)
	}
	aq, bq := a.Query(), b.Query()
	if aq.Get("latency") != "300000" || bq.Get("latency") != "120000" {
		t.Fatal("SRT expects microseconds")
	}
	bq.Set("latency", aq.Get("latency"))
	if !reflect.DeepEqual(aq, bq) || a.Host != b.Host || bq.Get("passphrase") == "" || bq.Get("pbkeylen") != "16" {
		t.Fatal("wait changed connection security or destination")
	}
	input, err := url.Parse(srtURL(s, false))
	if err != nil {
		t.Fatal(err)
	}
	if input.Query().Get("latency") != "300000" {
		t.Fatal("forwarding setting changed generated camera input")
	}
}
