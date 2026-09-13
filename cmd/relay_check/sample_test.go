package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"fieldvideolab/internal/lab"
)

func validSamples() []sample {
	now := time.Now()
	var result []sample
	for i := range 3 {
		at := now.Add(time.Duration(i) * time.Second)
		result = append(result, sample{At: at.UTC(), monotonic: at, ReadSeconds: .01, ElapsedSeconds: float64(i), Value: &snapshot{
			UpdatedAt: at.Add(-time.Second), Profile: "detail", RelayLink: "local", RelayStarts: 1,
			SourcePublisher: "source-1", RemotePublisher: "remote-1", SourceBytes: uint64(10000 + 1000*i), RemoteBytes: uint64(10000 + 1000*i),
			Relay:         process{PID: 31, Started: "Sat Sep 12 19:20:30 2026", CPUSeconds: float64(i) * 1.25, RSSKiB: uint64(1000 + 100*i)},
			SupervisorPID: 12, SupervisorStarted: "Sat Sep 12 19:20:20 2026",
		}})
	}
	return result
}

func TestSummarizeCompleteWindow(t *testing.T) {
	got, why := summarize(validSamples())
	if got == nil || why != "" {
		t.Fatal(why)
	}
	if got.WindowSeconds != 2 || got.RelayCPUPercent != 125 || got.ForwardedPayloadKbps != 8 || got.ForwardedPayloadBytes != 2000 || got.RelayRSSMeanKiB != 1100 || got.RelayRSSMaxKiB != 1200 {
		t.Fatalf("unexpected counters: %+v", got)
	}
	samples := validSamples()
	for i := range samples {
		samples[i].Value.Relay.CPUSeconds = 0
		samples[i].Value.RemoteBytes = 10000
	}
	got, why = summarize(samples)
	if got == nil || got.RelayCPUPercent != 0 || got.ForwardedPayloadKbps != 0 {
		t.Fatalf("legitimate zero was unavailable: %+v %s", got, why)
	}
}

func TestSummarizeNeverAveragesAcrossFailures(t *testing.T) {
	tests := map[string]func([]sample){
		"missing":               func(s []sample) { s[1].Value = nil },
		"failed":                func(s []sample) { s[1].Unavailable = "read failed" },
		"gap":                   func(s []sample) { s[1].monotonic = s[0].monotonic.Add(3 * time.Second) },
		"backward time":         func(s []sample) { s[1].monotonic = s[0].monotonic },
		"read timeout":          func(s []sample) { s[1].ReadSeconds = 3 },
		"relay pid":             func(s []sample) { s[1].Value.Relay.PID++ },
		"pid reused":            func(s []sample) { s[1].Value.Relay.Started = "Sat Sep 12 19:20:31 2026" },
		"worker restarted":      func(s []sample) { s[1].Value.RelayStarts++ },
		"supervisor pid":        func(s []sample) { s[1].Value.SupervisorPID++ },
		"supervisor pid reused": func(s []sample) { s[1].Value.SupervisorStarted = "Sat Sep 12 19:20:31 2026" },
		"profile":               func(s []sample) { s[1].Value.Profile = "copy" },
		"link":                  func(s []sample) { s[1].Value.RelayLink = "srt" },
		"source publisher":      func(s []sample) { s[1].Value.SourcePublisher = "another" },
		"forwarded publisher":   func(s []sample) { s[1].Value.RemotePublisher = "another" },
		"source reset":          func(s []sample) { s[1].Value.SourceBytes = 0 },
		"remote reset":          func(s []sample) { s[1].Value.RemoteBytes = 0 },
		"cpu reset":             func(s []sample) { s[2].Value.Relay.CPUSeconds = .1 },
		"stale":                 func(s []sample) { s[1].Value.UpdatedAt = s[1].At.Add(-6 * time.Second) },
		"future":                func(s []sample) { s[1].Value.UpdatedAt = s[1].At.Add(2 * time.Second) },
		"missing time":          func(s []sample) { s[1].Value.UpdatedAt = time.Time{} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			samples := validSamples()
			mutate(samples)
			if got, why := summarize(samples); got != nil || why == "" {
				t.Fatalf("bad window produced measurement: %+v %s", got, why)
			}
		})
	}
	if got, why := summarize(validSamples()[:1]); got != nil || why == "" {
		t.Fatal("one sample produced a rate")
	}
}

func TestCounterFieldPresence(t *testing.T) {
	for _, test := range []struct {
		name, body string
		valid      bool
	}{
		{"zero", `{"ready":true,"bytesReceived":0,"source":{"id":"publisher"}}`, true},
		{"online", `{"online":true,"bytesReceived":20,"source":{"id":"publisher"}}`, true},
		{"available", `{"available":true,"bytesReceived":20,"source":{"id":"publisher"}}`, true},
		{"missing bytes", `{"ready":true,"source":{"id":"publisher"}}`, false},
		{"null bytes", `{"ready":true,"bytesReceived":null,"source":{"id":"publisher"}}`, false},
		{"missing readiness", `{"bytesReceived":0,"source":{"id":"publisher"}}`, false},
		{"not ready", `{"ready":false,"bytesReceived":0,"source":{"id":"publisher"}}`, false},
		{"changed publisher", `{"ready":true,"bytesReceived":0,"source":{"id":"another"}}`, false},
		{"missing publisher", `{"ready":true,"bytesReceived":0}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, test.body) }))
			defer server.Close()
			_, portValue, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
			port, _ := strconv.Atoi(portValue)
			err := verifyCounter(context.Background(), port, "camera-01", lab.SourceState{PublisherID: "publisher"})
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
		})
	}
}

func TestReportIsPrivateAndHasNoSensitiveStateFields(t *testing.T) {
	samples := validSamples()
	m, _ := summarize(samples)
	path, err := saveReport(t.TempDir(), report{Version: 1, Samples: samples, Measurements: m})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("report permissions: %v %v", info, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"supervisor_token", "publisher_password", "passphrase", "settings", "last_exit", "log_error", "rtsp://", "srt://"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("private state field in report: %s", private)
		}
	}
	var value map[string]any
	if json.Unmarshal(data, &value) != nil {
		t.Fatal("report is not JSON")
	}
}

func TestInterruptedWindowHasNoMeasurement(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	result := measure(ctx, options{Duration: 5 * time.Second}, func(context.Context) sample { cancel(); return validSamples()[0] })
	if result.Measurements != nil || result.Result != "inconclusive" || result.Unavailable == "" {
		t.Fatalf("interrupted window accepted: %+v", result)
	}
}

func TestOptions(t *testing.T) {
	root := t.TempDir()
	if cfg, err := parseOptions([]string{"--root", root}, io.Discard); err != nil || cfg.Duration != 30*time.Second || cfg.Source != "camera-01" {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	for _, args := range [][]string{{"--duration", "0s"}, {"--duration", "121s"}, {"--source", "../bad"}, {"--source", "a b"}, {"extra"}} {
		if _, err := parseOptions(append([]string{"--root", root}, args...), io.Discard); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}
