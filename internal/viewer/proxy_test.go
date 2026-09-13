package viewer

import (
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, headers http.Header, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body))}
}

func request(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, "http://127.0.0.1:19081"+path, strings.NewReader(body)))
	return w
}

const session = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

func TestWHEPProxyCustomFixedPortsAndSession(t *testing.T) {
	for _, test := range []struct{ route, port string }{{"local", "31001"}, {"forwarded", "31002"}} {
		t.Run(test.route, func(t *testing.T) {
			called := 0
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				called++
				if r.URL.Scheme != "http" || r.URL.Host != "127.0.0.1:"+test.port || r.URL.Path != "/camera-02/whep" {
					t.Fatal("proxy forwarded an unexpected target")
				}
				return response(201, http.Header{"Location": {"/camera-02/whep/" + session}, "Content-Type": {"application/sdp"}}, "answer"), nil
			})}
			h := WHEPProxy([]string{"camera-02"}, 31001, 31002, client)
			w := request(h, "POST", "/whep/"+test.route+"/camera-02", "offer")
			if w.Code != 201 || w.Header().Get("Location") != "/whep/"+test.route+"/camera-02/"+session || w.Body.String() != "answer" || called != 1 {
				t.Fatalf("unexpected response: status %d", w.Code)
			}
			client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != "PATCH" || r.URL.Path != "/camera-02/whep/"+session || r.Header.Get("If-Match") != "*" {
					t.Fatal("invalid session request")
				}
				return response(204, http.Header{}, ""), nil
			})
			h = WHEPProxy([]string{"camera-02"}, 31001, 31002, client)
			r := httptest.NewRequest("PATCH", "http://127.0.0.1:19081"+w.Header().Get("Location"), strings.NewReader("candidate"))
			r.Header.Set("If-Match", "*")
			w = httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 204 {
				t.Fatalf("PATCH: %d", w.Code)
			}
		})
	}
}

func TestWHEPProxyInvalidConfigurationFailsClosed(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid configuration reached upstream")
		return nil, nil
	})}
	for _, test := range []struct {
		local, forwarded int
		ids              []string
	}{
		{0, 28889, []string{"camera-01"}}, {-1, 28889, []string{"camera-01"}},
		{65536, 28889, []string{"camera-01"}}, {18889, 0, []string{"camera-01"}},
		{18889, -1, []string{"camera-01"}}, {18889, 65536, []string{"camera-01"}},
		{18889, 28889, []string{"camera-01", "../secret"}}, {18889, 28889, []string{""}},
	} {
		w := request(WHEPProxy(test.ids, test.local, test.forwarded, client), "POST", "/whep/local/camera-01", "")
		if w.Code != 500 {
			t.Fatalf("invalid configuration returned %d", w.Code)
		}
	}
	if w := request(WHEPProxy(nil, 18889, 28889, client), "POST", "/whep/local/camera-01", ""); w.Code != 404 {
		t.Fatal("empty source allowlist permitted a source")
	}
}

func TestWHEPProxyAllowsOptionsAndSessionDelete(t *testing.T) {
	for _, test := range []struct{ method, suffix string }{{"OPTIONS", ""}, {"DELETE", "/" + session}} {
		called := false
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			called = true
			if r.Method != test.method || r.URL.Path != "/camera-01/whep"+test.suffix {
				t.Fatal("changed WHEP read request")
			}
			return response(204, http.Header{}, ""), nil
		})}
		w := request(WHEPProxy([]string{"camera-01"}, 18889, 28889, client), test.method, "/whep/local/camera-01"+test.suffix, "")
		if w.Code != 204 || !called {
			t.Fatalf("%s status %d, called %v", test.method, w.Code, called)
		}
	}
}

func TestWHEPProxyRejectsUnboundedRoutesAndMethods(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid request reached upstream")
		return nil, nil
	})}
	h := WHEPProxy([]string{"camera-01"}, 18889, 28889, client)
	for _, path := range []string{
		"/whep/http:evil/camera-01", "/whep/local/camera-02", "/whep/local/camera-01/../../whip",
		"/whep/local/camera-01?token=value", "/whep/local/camera-01?", "/whep/local/camera-01/not-a-session",
		"/local/camera-01", "/whep/local/camera-01/", "/whep/local/camera%2d01", "/whep/local/camera-01/" + session + "/extra",
	} {
		if w := request(h, "POST", path, ""); w.Code != 404 {
			t.Fatalf("unsafe route %q status %d", path, w.Code)
		}
	}
	for _, path := range []string{"/whep/local/camera-01", "/whep/local/camera-01/" + session} {
		for _, method := range []string{"GET", "PUT", "HEAD"} {
			if w := request(h, method, path, ""); w.Code != 405 {
				t.Fatalf("unsafe method %s status %d", method, w.Code)
			}
		}
	}
	if w := request(h, "POST", "/whep/local/camera-01/"+session, ""); w.Code != 405 {
		t.Fatal("POST allowed on a session")
	}
	if w := request(h, "DELETE", "/whep/local/camera-01", ""); w.Code != 405 {
		t.Fatal("DELETE allowed on the source")
	}
	if w := request(h, "POST", "/whep/local/camera-01", strings.Repeat("x", bodyLimit+1)); w.Code != 413 {
		t.Fatalf("oversized request: %d", w.Code)
	}
}

func TestWHEPProxySessionLocationsStayBounded(t *testing.T) {
	for _, location := range []string{
		"http://example.invalid/private", "http://127.0.0.1:31001/camera-01/whep/" + session,
		"https://127.0.0.1:18889/camera-01/whep/" + session,
		"http://user:pass@127.0.0.1:18889/camera-01/whep/" + session,
		"/camera-02/whep/" + session, "/camera-01/whip/" + session,
		"/camera-01/whep/" + session + "?x=1", "/camera-01/whep/" + session + "?",
		"/camera-01/whep/" + session + "#fragment", "/camera-01/whep/not-a-session",
		"/camera-01/whep/%61aaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	} {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(201, http.Header{"Location": {location}}, "answer"), nil
		})}
		w := request(WHEPProxy([]string{"camera-01"}, 18889, 28889, client), "POST", "/whep/local/camera-01", "")
		if w.Code != 502 || w.Header().Get("Location") != "" {
			t.Fatalf("unsafe location %q escaped proxy", location)
		}
	}
}

func TestWHEPProxyDisablesRedirectsAndPrivateHeaders(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	upstream, _ := url.Parse("http://127.0.0.1:18889")
	jar.SetCookies(upstream, []*http.Cookie{{Name: "private", Value: "secret"}})
	calls := 0
	client := &http.Client{Jar: jar, Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error {
		t.Fatal("caller redirect policy was used")
		return nil
	}, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		for _, key := range []string{"Origin", "Authorization", "Cookie", "Proxy-Authorization", "X-Forwarded-For", "Sec-Fetch-Site"} {
			if r.Header.Get(key) != "" {
				t.Fatalf("forwarded private header %s", key)
			}
		}
		if r.Header.Get("Content-Type") != "application/sdp" || r.Header.Get("If-Match") != "*" {
			t.Fatal("lost permitted request headers")
		}
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 8*time.Second {
			t.Fatal("upstream request has no bounded deadline")
		}
		return response(307, http.Header{
			"Location": {"/camera-01/whep/" + session}, "Set-Cookie": {"private=secret"},
			"Access-Control-Allow-Origin": {"*"}, "Authorization": {"secret"}, "Etag": {"match"},
		}, "redirect"), nil
	})}
	h := WHEPProxy([]string{"camera-01"}, 18889, 28889, client)
	r := httptest.NewRequest("POST", "http://127.0.0.1:19081/whep/local/camera-01", strings.NewReader("offer"))
	for _, key := range []string{"Origin", "Authorization", "Cookie", "Proxy-Authorization", "X-Forwarded-For", "Sec-Fetch-Site"} {
		r.Header.Set(key, "private")
	}
	r.Header.Set("Content-Type", "application/sdp")
	r.Header.Set("If-Match", "*")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 307 || calls != 1 || w.Header().Get("ETag") != "match" {
		t.Fatalf("unexpected redirect behavior: status %d, calls %d", w.Code, calls)
	}
	for _, key := range []string{"Set-Cookie", "Access-Control-Allow-Origin", "Authorization"} {
		if w.Header().Get(key) != "" {
			t.Fatalf("private upstream header %s escaped", key)
		}
	}
	if client.Jar == nil || client.Timeout != time.Minute || client.CheckRedirect == nil {
		t.Fatal("shared handler mutated caller client")
	}
}

func TestWHEPProxyBoundsResponsesAndTimeouts(t *testing.T) {
	for _, test := range []struct {
		name string
		trip roundTripFunc
	}{
		{"oversized", func(*http.Request) (*http.Response, error) {
			return response(200, nil, strings.Repeat("x", bodyLimit+1)), nil
		}},
		{"failure", func(*http.Request) (*http.Response, error) { return nil, errors.New("private internal detail") }},
		{"timeout", func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := WHEPProxy([]string{"camera-01"}, 18889, 28889, &http.Client{Timeout: 10 * time.Millisecond, Transport: test.trip})
			w := request(h, "POST", "/whep/local/camera-01", "")
			if w.Code != 502 || strings.Contains(w.Body.String(), "private internal detail") {
				t.Fatalf("invalid response status/body: %d %q", w.Code, w.Body.String())
			}
		})
	}
}
