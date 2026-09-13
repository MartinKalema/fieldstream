package lab

import (
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestDetailProfileUsesSameInputAndAuthenticatedTargets(t *testing.T) {
	s := validTestSettings()
	for _, link := range []string{"local", "srt"} {
		t.Run(link, func(t *testing.T) {
			control := defaultControl()
			control.RelayLink = link
			copyArgs := relayCommand(s, control)
			control.Profile = "detail"
			args := relayCommand(s, control)
			input := append(ffmpegBase(s), "-threads:v", "1")
			input = append(input, inputArgs()...)
			if !reflect.DeepEqual(args[:len(input)], input) {
				t.Fatal("detail profile must change only the decoder thread count before the incoming camera connection")
			}
			options := map[string]string{}
			for i := len(input); i+1 < len(args)-1; i += 2 {
				options[args[i]] = args[i+1]
			}
			for flag, want := range map[string]string{
				"-vf": "fps=20", "-c:v": "libx264", "-threads": "2", "-preset": "veryfast",
				"-tune": "zerolatency", "-profile:v": "baseline", "-pix_fmt": "yuv420p", "-bf": "0",
				"-g": "20", "-keyint_min": "20", "-sc_threshold": "0", "-filter_threads": "1",
				"-b:v": "1200k", "-maxrate": "1200k", "-bufsize": "600k",
			} {
				if options[flag] != want {
					t.Errorf("%s = %q; want %q", flag, options[flag], want)
				}
			}
			for _, flag := range []string{"-s", "-s:v", "-r"} {
				if _, set := options[flag]; set {
					t.Fatalf("%s unexpectedly overrides camera dimensions or filter timing", flag)
				}
			}
			if args[len(args)-1] != copyArgs[len(copyArgs)-1] {
				t.Fatal("detail profile changed destination or connection credentials")
			}
			target, err := url.Parse(args[len(args)-1])
			if err != nil {
				t.Fatal("invalid forwarding target")
			}
			if link == "srt" && (target.Query().Get("passphrase") == "" || target.Query().Get("pbkeylen") != "16") {
				t.Fatal("detail SRT relay lost encryption")
			}
			if link == "local" && (target.Hostname() != "127.0.0.1" || target.User == nil || options["-rtsp_transport"] != "tcp") {
				t.Fatal("detail local relay lost authenticated loopback TCP transport")
			}
		})
	}
}

func TestDetailThreadOptionsApplyToDecoderAndEncoderSeparately(t *testing.T) {
	s := validTestSettings()
	for _, profile := range []string{"copy", "small", "detail"} {
		control := defaultControl()
		control.Profile = profile
		args := relayCommand(s, control)
		inputAt := slices.Index(args, "-i")
		if inputAt < 0 {
			t.Fatal("relay has no input")
		}
		if profile != "detail" {
			originalInput := append(ffmpegBase(s), inputArgs()...)
			if !reflect.DeepEqual(args[:len(originalInput)], originalInput) || slices.Contains(args, "-threads:v") || slices.Contains(args, "-filter_threads") {
				t.Fatalf("%s input or threading changed", profile)
			}
			continue
		}
		decoderAt := slices.Index(args, "-threads:v")
		encoderAt := slices.Index(args, "-threads")
		filterAt := slices.Index(args, "-filter_threads")
		if decoderAt < 0 || decoderAt >= inputAt || args[decoderAt+1] != "1" {
			t.Fatal("one-thread decoder option must appear before -i")
		}
		if encoderAt <= inputAt+1 || args[encoderAt+1] != "2" || filterAt <= inputAt+1 || args[filterAt+1] != "1" {
			t.Fatal("encoder and filter thread limits must be distinct from the decoder input option")
		}
		for _, option := range []string{"-g", "-keyint_min", "-sc_threshold"} {
			if slices.Index(args, option) <= inputAt+1 {
				t.Fatalf("%s must apply to the encoder output", option)
			}
		}
	}
}

func TestProfileRoundTripIsolatesSourcesAndRejectsInvalidChanges(t *testing.T) {
	p := NewPaths(t.TempDir())
	if err := os.MkdirAll(p.Local, 0700); err != nil {
		t.Fatal(err)
	}
	s := validTestSettings()
	second, err := newSource("camera-02", "Second camera")
	if err != nil {
		t.Fatal(err)
	}
	s.Sources = append(GetSources(s), second)
	if err := AtomicJSON(filepath.Join(p.Local, "settings.json"), s); err != nil {
		t.Fatal(err)
	}
	before := defaultControl()
	before.RelayWaitMS = 120
	before.RelayLink = "local"
	before.DemoEnabled = true
	if err := p.ChangeSourceControl("camera-01", func(c *Control) { *c = before }); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"detail", "small", "copy"} {
		if err := p.ChangeSourceControl("camera-01", func(c *Control) { c.Profile = profile }); err != nil {
			t.Fatal(err)
		}
		controls, err := p.loadControls(s)
		if err != nil {
			t.Fatal(err)
		}
		want := before
		want.Profile = profile
		if controls.Sources["camera-01"] != want || controls.Sources["camera-02"] != defaultControl() {
			t.Fatal("profile change altered other controls or another source")
		}
		if controls.Sources["camera-01"].relayKey() != profile+"/local" {
			t.Fatal("profile change will not restart the selected forwarder")
		}
	}
	savedPath := filepath.Join(p.Local, "control.json")
	saved, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"", "DETAIL", "720p", "detail;copy"} {
		if err := p.ChangeSourceControl("camera-01", func(c *Control) { c.Profile = profile }); err == nil {
			t.Errorf("accepted invalid profile %q", profile)
		}
	}
	after, err := os.ReadFile(savedPath)
	if err != nil || !bytes.Equal(saved, after) {
		t.Fatal("rejected profile changes modified saved controls")
	}
}
