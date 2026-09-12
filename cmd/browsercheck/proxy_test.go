package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fieldvideolab/internal/viewer"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWHEPWrapperKeepsReceiverPorts(t *testing.T) {
	for _, test := range []struct{ route, port string }{{"local", "18889"}, {"forwarded", "28889"}} {
		t.Run(test.route, func(t *testing.T) {
			called := false
			h := whepProxy([]string{"camera-01"}, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				called = true
				if r.URL.Host != "127.0.0.1:"+test.port || r.URL.Path != "/camera-01/whep" {
					t.Fatal("changed receiver destination")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("answer"))}, nil
			})})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", "http://127.0.0.1:19081/whep/"+test.route+"/camera-01", nil))
			if w.Code != 200 || !called {
				t.Fatalf("request status %d, called %v", w.Code, called)
			}
		})
	}
}

func TestSharedReaderAssetsKeepStaticURLs(t *testing.T) {
	h := handler(19081, []string{"camera-01"}, t.TempDir())
	for _, test := range []struct {
		path, contentType string
		data              []byte
	}{
		{"/reader.js", "text/javascript; charset=utf-8", viewer.ReaderJS},
		{"/mediamtx-LICENSE.txt", "text/plain; charset=utf-8", viewer.ReaderLicense},
	} {
		for _, method := range []string{"GET", "HEAD"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(method, "http://127.0.0.1:19081"+test.path, nil))
			if w.Code != 200 || w.Header().Get("Content-Type") != test.contentType || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%s %s: unexpected static response", method, test.path)
			}
			if method == "GET" && !bytes.Equal(w.Body.Bytes(), test.data) {
				t.Fatalf("%s: changed asset", test.path)
			}
			if method == "HEAD" && w.Body.Len() != 0 {
				t.Fatal("HEAD returned a body")
			}
		}
	}
}

func TestServerRejectsOtherOriginsAndServesEmbeddedPage(t *testing.T) {
	h := handler(19081, []string{"camera-01"}, t.TempDir())
	for _, test := range []struct {
		host, origin, path string
		code               int
	}{
		{"127.0.0.1:19081", "", "/", 200},
		{"malicious.invalid:19081", "", "/", 403},
		{"127.0.0.1:19081", "http://malicious.invalid", "/whep/local/camera-01", 403},
		{"127.0.0.1:19081", "", "/../main.go", 404},
	} {
		r := httptest.NewRequest("GET", "http://"+test.host+test.path, nil)
		r.Header.Set("Origin", test.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.code {
			t.Fatalf("%s: %d, want %d", test.path, w.Code, test.code)
		}
		if test.code == 200 && !strings.Contains(w.Body.String(), "Start comparison") {
			t.Fatal("embedded page missing")
		}
	}
}
