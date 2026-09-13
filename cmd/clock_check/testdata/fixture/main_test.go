package main

import (
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"cmd/clock_check/clock.html":              "<!doctype html><html><head></head><body><main data-source=\"{{.Source}}\" data-sources=\"{{.AllowedSources}}\">Production template</main></body></html>",
		"cmd/clock_check/assets/app.mjs":          "// Production app test marker",
		"cmd/clock_check/assets/watch.mjs":        "// Production watch test marker",
		"cmd/clock_check/assets/style.css":        "/* Production style test marker */",
		"cmd/clock_check/assets/optical.mjs":      "// Production optical test marker",
		"cmd/clock_check/assets/age.mjs":          "// Production age test marker",
		"cmd/clock_check/assets/qr-worker.js":     "// Production QR worker test marker",
		"cmd/clock_check/assets/vendor/qrcode.js": "// Production QR encoder test marker",
		"cmd/clock_check/assets/vendor/jsQR.js":   "// Production QR decoder test marker",
	}
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func TestOptionsRequireLoopbackAddress(t *testing.T) {
	root := fixtureProject(t)
	for _, address := range []string{"0.0.0.0:19090", "example.com:19090", "localhost:19090", "127.0.0.1:-1", "127.0.0.1:65536", "127.0.0.1"} {
		if _, err := parseOptions([]string{"--root", root, "--listen", address}, io.Discard); err == nil {
			t.Fatalf("unsafe listen address accepted: %s", address)
		}
	}
	for _, address := range []string{"127.0.0.1:19090", "127.0.0.1:0", "[::1]:19090"} {
		if _, err := parseOptions([]string{"--root", root, "--listen", address}, io.Discard); err != nil {
			t.Fatalf("valid listen address rejected: %s: %v", address, err)
		}
	}
}

func TestFixtureUsesProductionAssetsAndConspicuousTemplate(t *testing.T) {
	root := fixtureProject(t)
	h, err := pageHandler(options{root: root, listen: "127.0.0.1:19090"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ path, contains string }{
		{"/", "TEST ONLY — generated video inside this browser"},
		{"/app.mjs", "Production app test marker"}, {"/watch.mjs", "Production watch test marker"},
		{"/style.css", "Production style test marker"}, {"/fixture.css", ".fixture-toolbar"},
		{"/optical.mjs", "Production optical test marker"}, {"/age.mjs", "Production age test marker"},
		{"/qr-worker.js", "Production QR worker test marker"},
		{"/vendor/qrcode.js", "Production QR encoder test marker"}, {"/vendor/jsQR.js", "Production QR decoder test marker"},
		{"/reader.js", "new RTCPeerConnection({iceServers: []})"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:19090"+test.path, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), test.contains) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unexpected asset response for %s", test.path)
		}
		if test.path == "/" && (!strings.Contains(w.Body.String(), `data-source="camera-01" data-sources="camera-01"`) || !strings.Contains(w.Body.String(), "fixture-end-forwarded") || !strings.Contains(w.Body.String(), "fixture-pause-forwarded-player") || !strings.Contains(w.Body.String(), "fixture-play-forwarded-player")) {
			t.Fatal("production template data or fixture control missing")
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "worker-src 'self'") {
			t.Fatal("local QR worker is not allowed by fixture policy")
		}
		if test.path == "/" {
			for _, mode := range []string{"normal", "delay", "freeze", "blank", "wrong-session", "two"} {
				if !strings.Contains(w.Body.String(), `id="fixture-age-`+mode+`"`) {
					t.Fatalf("missing optical control %s", mode)
				}
			}
		}
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("HEAD", "http://127.0.0.1:19090"+test.path, nil))
		if w.Code != 200 || w.Body.Len() != 0 {
			t.Fatal("HEAD returned data")
		}
	}
}

func TestFixtureRejectsUnlistedRoutesHostsAndMethods(t *testing.T) {
	h, err := pageHandler(options{root: fixtureProject(t), listen: "127.0.0.1:19090"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, target string
		code           int
	}{
		{"GET", "http://evil.invalid:19090/", 403},
		{"GET", "http://127.0.0.1:19090/whep/local/camera-01", 404},
		{"POST", "http://127.0.0.1:19090/whep/local/camera-01", 405},
		{"GET", "http://127.0.0.1:19090/../main.go", 404},
		{"GET", "http://127.0.0.1:19090/assets/", 404},
		{"GET", "http://127.0.0.1:19090/app.mjs?source=other", 404},
		{"GET", "http://127.0.0.1:19090/reader%2Ejs", 404},
		{"GET", "http://127.0.0.1:19090/vendor/", 404},
		{"GET", "http://127.0.0.1:19090/vendor/manifest.json", 404},
		{"GET", "http://127.0.0.1:19090/qr-worker.js?source=other", 404},
		{"DELETE", "http://127.0.0.1:19090/", 405},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(test.method, test.target, nil))
		if w.Code != test.code {
			t.Fatalf("%s %s: %d, want %d", test.method, test.target, w.Code, test.code)
		}
	}
}

func TestFixtureRejectsAssetSymlinkOutsideProject(t *testing.T) {
	root := fixtureProject(t)
	outside := filepath.Join(t.TempDir(), "private.mjs")
	if err := os.WriteFile(outside, []byte("must never be served"), 0600); err != nil {
		t.Fatal(err)
	}
	asset := filepath.Join(root, "cmd/clock_check/assets/app.mjs")
	if err := os.Remove(asset); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, asset); err != nil {
		t.Fatal(err)
	}
	if _, err := pageHandler(options{root: root, listen: "127.0.0.1:19090"}); err == nil {
		t.Fatal("fixture allowed a production asset outside its repository")
	}
}
