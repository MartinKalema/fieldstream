package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClockCheckRejectsNonlocalAndInvalidOptions(t *testing.T) {
	for _, args := range [][]string{
		{"--listen", "0.0.0.0:19080"}, {"--listen", "example.com:19080"},
		{"--listen", "127.0.0.1:65536"}, {"--source", "../settings"},
		{"--local-port", "0"}, {"--forwarded-port", "65536"}, {"unexpected"},
		{"--sources", "camera-02"}, {"--sources", "camera-01,camera-01"},
		{"--sources", "camera-01,"}, {"--sources", "camera-01,../settings"},
		{"--sources", "camera-01,b,c,d,e"},
	} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatalf("accepted invalid options: %q", args)
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

func TestClockCheckPageAndRestrictedAssets(t *testing.T) {
	cfg, h := clockHandler(t, "--source", "camera-02", "--local-port", "19001")
	for _, tc := range []struct {
		method, path, contains string
		status                 int
	}{
		{"GET", "/", `data-source="camera-02"`, 200},
		{"HEAD", "/", "", 200}, {"POST", "/", "", 405},
		{"GET", "/.local/settings.json", "", 404}, {"GET", "/assets/", "", 404},
		{"GET", "/app.mjs", "requestVideoFrameCallback", 200},
		{"GET", "/watch.mjs", "createWatch", 200},
		{"GET", "/age.mjs", "createAge", 200},
		{"GET", "/optical.mjs", "Start a new test", 200},
		{"GET", "/qr-worker.js", "importScripts", 200},
		{"GET", "/vendor/qrcode.js", "QRCode", 200},
		{"GET", "/vendor/jsQR.js", "jsQR", 200},
		{"GET", "/vendor/", "", 404},
		{"GET", "/vendor/other.js", "", 404},
		{"GET", "/reader.js", "MediaMTXWebRTCReader", 200},
		{"GET", "/mediamtx-LICENSE.txt", "MIT", 200},
		{"GET", "/style.css", "", 200}, {"HEAD", "/app.mjs", "", 200},
		{"HEAD", "/reader.js", "", 200},
		{"GET", "/?source=camera-01", "", 400},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Host = cfg.Listen
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("%s %s: got %d: %s", tc.method, tc.path, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), tc.contains) {
			t.Fatalf("%s missing %q", tc.path, tc.contains)
		}
		if tc.method == "HEAD" && rec.Body.Len() != 0 {
			t.Fatal("HEAD returned a body")
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("response can be cached")
		}
		if tc.path == "/" && tc.method == "GET" {
			for _, want := range []string{`id="local-video"`, `id="forwarded-video"`, "Camera age is not verified", `id="delay-marker"`, "not a guaranteed delay bound"} {
				if !strings.Contains(rec.Body.String(), want) {
					t.Fatalf("missing %q", want)
				}
			}
			if strings.Contains(rec.Body.String(), "<iframe") {
				t.Fatal("page still embeds unobservable players")
			}
			csp := rec.Header().Get("Content-Security-Policy")
			if !strings.Contains(csp, "connect-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
				t.Fatal("missing browser boundary")
			}
		}
	}
}

func TestClockCheckSourceAllowlistAndRequestBoundary(t *testing.T) {
	cfg, h := clockHandler(t, "--sources", "camera-01,camera-02")
	for _, tc := range []struct {
		path, host, origin, site string
		status                   int
	}{
		{"/?source=camera-02", cfg.Listen, "", "", 200},
		{"/?source=camera-03", cfg.Listen, "", "", 400},
		{"/", "evil.example", "", "", 403},
		{"/whep/local/camera-01", cfg.Listen, "https://evil.example", "cross-site", 403},
		{"/whep/local/camera-01", cfg.Listen, "", "same-site", 403},
		{"/whep/local/camera-03", cfg.Listen, "http://" + cfg.Listen, "same-origin", 404},
		{"/whep/publish/camera-01", cfg.Listen, "", "", 404},
	} {
		req := httptest.NewRequest("OPTIONS", tc.path, nil)
		if strings.HasPrefix(tc.path, "/?") || tc.path == "/" {
			req.Method = "GET"
		}
		req.Host = tc.host
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Sec-Fetch-Site", tc.site)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("%s: got %d", tc.path, rec.Code)
		}
		if tc.status == 200 && !strings.Contains(rec.Body.String(), `data-source="camera-02"`) {
			t.Fatal("allowed source not selected")
		}
	}
}
