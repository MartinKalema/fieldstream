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
	} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatalf("accepted invalid options: %q", args)
		}
	}
}

func TestClockCheckEmbeddedPageAndMethods(t *testing.T) {
	cfg, err := parseOptions([]string{"--source", "camera-02", "--local-port", "19001"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := pageHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/", 200}, {http.MethodHead, "/", 200},
		{http.MethodPost, "/", 405}, {http.MethodGet, "/.local/settings.json", 404},
	} {
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest(tc.method, tc.path, nil))
		if r.Code != tc.status {
			t.Fatalf("%s %s: got %d", tc.method, tc.path, r.Code)
		}
		if tc.method == http.MethodGet && tc.path == "/" {
			for _, want := range []string{`data-source="camera-02"`, `data-local-port="19001"`, "performance.now()", "does not calculate delay automatically"} {
				if !strings.Contains(r.Body.String(), want) {
					t.Fatalf("missing embedded page content %q", want)
				}
			}
			if r.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("clock page can be cached")
			}
		}
		if tc.method == http.MethodHead && r.Body.Len() != 0 {
			t.Fatal("HEAD returned a body")
		}
	}
}
