package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClockCheckAcceptsOnlyLoopbackAndClockOptions(t *testing.T) {
	for _, args := range [][]string{
		{"--listen", "0.0.0.0:19080"}, {"--listen", "example.com:19080"},
		{"--listen", "localhost:19080"}, {"--listen", "[::]:19080"},
		{"--listen", "127.0.0.1:65536"}, {"--listen", "127.0.0.1:-1"},
		{"--listen", "127.0.0.1"}, {"unexpected"},
		{"--source", "camera-01"}, {"--sources", "camera-01,camera-02"},
		{"--local-port", "18889"}, {"--forwarded-port", "28889"},
	} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatalf("accepted unsupported options: %q", args)
		}
	}
	for _, listen := range []string{"127.0.0.1:19080", "127.0.0.1:0", "[::1]:19080"} {
		if _, err := parseOptions([]string{"--listen", listen}, io.Discard); err != nil {
			t.Fatalf("rejected loopback address %s: %v", listen, err)
		}
	}
}

func clockHandler(t *testing.T, args ...string) (options, http.Handler) {
	t.Helper()
	cfg, err := parseOptions(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	h, err := pageHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, h
}

func requestClock(h http.Handler, host, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestClockCheckServesOnlyClockAssets(t *testing.T) {
	cfg, h := clockHandler(t)
	for path, mime := range map[string]string{
		"/": "text/html", "/clock.js": "text/javascript", "/style.css": "text/css",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := requestClock(h, cfg.Listen, method, path)
			if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), mime) {
				t.Fatalf("%s %s: got %d %s", method, path, rec.Code, rec.Header().Get("Content-Type"))
			}
			if (method == http.MethodHead) != (rec.Body.Len() == 0) {
				t.Fatalf("unexpected body for %s %s", method, path)
			}
			if rec.Header().Get("Cache-Control") != "no-store" ||
				rec.Header().Get("Referrer-Policy") != "no-referrer" ||
				rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("clock asset lost response protections")
			}
			csp := rec.Header().Get("Content-Security-Policy")
			for _, restriction := range []string{
				"default-src 'none'", "script-src 'self'", "style-src 'self'",
				"connect-src 'none'", "media-src 'none'", "worker-src 'none'",
				"frame-ancestors 'none'", "base-uri 'none'", "form-action 'none'",
			} {
				if !strings.Contains(csp, restriction) {
					t.Fatalf("missing page isolation: %s", restriction)
				}
			}
		}
	}
	for _, path := range []string{
		"/.local/settings.json", "/assets/", "/assets/clock.js", "/../settings",
		"/reader.js", "/mediamtx-LICENSE.txt", "/app.mjs", "/watch.mjs", "/age.mjs",
		"/optical.mjs", "/qr-worker.js", "/vendor/", "/vendor/qrcode.js", "/vendor/jsQR.js",
		"/whep/local/camera-01", "/whep/forwarded/camera-01", "/api/state",
	} {
		rec := requestClock(h, cfg.Listen, http.MethodGet, path)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("obsolete or private route %s remains accessible: %d", path, rec.Code)
		}
	}
}

func TestClockPageHasNoVideoOrAutomaticMeasurement(t *testing.T) {
	cfg, h := clockHandler(t)
	page := requestClock(h, cfg.Listen, http.MethodGet, "/").Body.String()
	for _, obsolete := range []string{
		"<video", "<iframe", "<canvas", "/whep/", "/reader.js",
		"data-source=", "delay-marker", "age-start", "local-state", "forwarded-state",
	} {
		if strings.Contains(page, obsolete) {
			t.Fatalf("clock page retains browser playback or automatic measurement: %s", obsolete)
		}
	}
	if strings.Count(page, "<script ") != 1 || !strings.Contains(page, "src=\"/clock.js\"") {
		t.Fatal("clock page does not have exactly one local clock script")
	}
}

func TestClockCheckRejectsOtherHostsMethodsAndQueryParameters(t *testing.T) {
	cfg, h := clockHandler(t)
	for _, path := range []string{"/", "/clock.js", "/style.css", "/whep/local/camera-01"} {
		for _, host := range []string{"evil.example", "localhost:19080", "127.0.0.1:19081"} {
			rec := requestClock(h, host, http.MethodGet, path)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("accepted host %s for %s", host, path)
			}
		}
		for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT"} {
			rec := requestClock(h, cfg.Listen, method, path)
			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
				t.Fatalf("accepted %s for %s", method, path)
			}
		}
	}
	if rec := requestClock(h, cfg.Listen, http.MethodGet, "/?source=camera-01"); rec.Code != http.StatusBadRequest {
		t.Fatal("old source-selection query was silently accepted")
	}
}

type addressWriter struct {
	address chan string
}

func (writer addressWriter) Write(data []byte) (int, error) {
	for _, field := range strings.Fields(string(data)) {
		if strings.HasPrefix(field, "http://") {
			select {
			case writer.address <- strings.TrimPrefix(field, "http://"):
			default:
			}
		}
	}
	return len(data), nil
}

func TestClockServerCancellationClosesItsListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addresses := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--listen", "127.0.0.1:0"}, addressWriter{address: addresses})
	}()
	var address string
	select {
	case address = <-addresses:
	case err := <-done:
		t.Fatalf("server exited before opening: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("server did not open")
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 2 * time.Second, Transport: transport}
	response, err := client.Get("http://" + address + "/")
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	if response.StatusCode != http.StatusOK || readErr != nil || closeErr != nil {
		t.Fatalf("clock was not available: status=%d read=%v close=%v", response.StatusCode, readErr, closeErr)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("graceful shutdown failed: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("server did not stop after cancellation")
	}
	connection, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatal("clock listener remained open after shutdown")
	}
}

func TestClockServerAlreadyCanceledContextStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"--listen", "127.0.0.1:0"}, io.Discard) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("canceled startup did not shut down cleanly: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("already canceled server stayed open")
	}
}
