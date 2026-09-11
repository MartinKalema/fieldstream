package lab

import (
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLocalRelayUsesAuthenticatedLoopbackWithoutChangingPictures(t *testing.T) {
	s := validTestSettings()
	control := defaultControl()
	before := relayCommand(s, control)
	control.RelayLink = "local"
	after := relayCommand(s, control)
	// Both modes consume the same input and use exactly the same video settings.
	if !reflect.DeepEqual(before[:len(before)-3], after[:len(after)-11]) {
		t.Fatal("local transport changed input or encoded pictures")
	}
	target, err := url.Parse(after[len(after)-1])
	if err != nil {
		t.Fatal("invalid local forwarding URL")
	}
	password, set := target.User.Password()
	if target.Scheme != "rtsp" || target.Host != "127.0.0.1:28554" || target.Path != "/camera-01" || target.User.Username() != s.RelayUser || !set || password != s.RelayPassword {
		t.Fatal("local forwarding must stay on loopback and retain publisher authentication")
	}
	if !reflect.DeepEqual(after[len(after)-11:len(after)-1], []string{"-progress", "pipe:1", "-stats_period", "1", "-f", "rtsp", "-rtsp_transport", "tcp", "-timeout", "3000000"}) {
		t.Fatal("local forwarding must use RTSP over TCP with bounded I/O and progress reporting")
	}
	// A saved SRT wait must not restart an active local connection.
	key := control.relayKey()
	control.RelayWaitMS = 120
	if key != control.relayKey() || !reflect.DeepEqual(after, relayCommand(s, control)) {
		t.Fatal("inactive SRT option changes local forwarding")
	}
}

func TestRelayLinkUpgradeAndInvalidChangesKeepSavedControls(t *testing.T) {
	p := NewPaths(t.TempDir())
	if err := os.MkdirAll(p.Local, 0700); err != nil {
		t.Fatal(err)
	}
	s := validTestSettings()
	if err := AtomicJSON(filepath.Join(p.Local, "settings.json"), s); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(p.Local, "control.json")
	old := []byte(`{"sources":{"camera-01":{"relay_enabled":true,"profile":"copy","demo_enabled":false,"relay_wait_ms":120}}}`)
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	controls, err := p.loadControls(s)
	if err != nil || controls.Sources["camera-01"].RelayLink != "srt" || controls.Sources["camera-01"].RelayWaitMS != 120 {
		t.Fatal("upgrade changed the existing connection choice")
	}
	for _, invalid := range []string{"", "udp", "rtsp://example.com", "LOCAL"} {
		if err := p.ChangeSourceControl("camera-01", func(c *Control) { c.RelayLink = invalid }); err == nil {
			t.Fatal("invalid forwarding link accepted")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(old, after) {
		t.Fatal("rejected link change damaged existing settings")
	}
}
