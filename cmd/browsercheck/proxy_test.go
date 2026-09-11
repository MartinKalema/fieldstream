package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWHEPProxyRoutesAndSessionLocation(t *testing.T) {
	const session = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	called := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called++
		if r.URL.Host != "127.0.0.1:28889" || r.URL.Path != "/camera-02/whep" || r.Header.Get("Origin") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatal("proxy forwarded an unexpected target or private browser headers")
		}
		return &http.Response{StatusCode: 201, Header: http.Header{"Location": {"/camera-02/whep/" + session}, "Content-Type": {"application/sdp"}}, Body: io.NopCloser(strings.NewReader("answer"))}, nil
	})}
	h := whepProxy([]string{"camera-02"}, client)
	r := httptest.NewRequest("POST", "http://127.0.0.1:19081/whep/forwarded/camera-02", strings.NewReader("offer"))
	r.Header.Set("Origin", "http://127.0.0.1:19081")
	r.Header.Set("Authorization", "private test value")
	r.Header.Set("Cookie", "private test value")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 201 || w.Header().Get("Location") != "/whep/forwarded/camera-02/"+session || w.Body.String() != "answer" || called != 1 {
		t.Fatalf("unexpected response: status %d", w.Code)
	}
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "PATCH" || r.URL.Path != "/camera-02/whep/"+session || r.Header.Get("If-Match") != "*" {
			t.Fatal("invalid session request")
		}
		return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	r = httptest.NewRequest("PATCH", "http://127.0.0.1:19081"+w.Header().Get("Location"), strings.NewReader("candidate"))
	r.Header.Set("If-Match", "*")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("PATCH: %d", w.Code)
	}
}

func TestWHEPProxyRejectsUnboundedRoutesAndResponses(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid request reached upstream")
		return nil, nil
	})}
	h := whepProxy([]string{"camera-01"}, client)
	for _, path := range []string{"/whep/http:evil/camera-01", "/whep/local/camera-02", "/whep/local/camera-01/../../whip", "/whep/local/camera-01?token=value", "/whep/local/camera-01/not-a-session"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "http://127.0.0.1:19081"+path, nil))
		if w.Code != 404 {
			t.Fatalf("unsafe route status %d", w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "http://127.0.0.1:19081/whep/local/camera-01", strings.NewReader(strings.Repeat("x", bodyLimit+1))))
	if w.Code != 413 {
		t.Fatalf("oversized request: %d", w.Code)
	}
	client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 201, Header: http.Header{"Location": {"http://example.invalid/private"}}, Body: io.NopCloser(strings.NewReader("answer"))}, nil
	})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "http://127.0.0.1:19081/whep/local/camera-01", nil))
	if w.Code != 502 || w.Header().Get("Location") != "" {
		t.Fatal("external session location escaped proxy")
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
