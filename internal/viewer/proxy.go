// Package viewer shares the local diagnostic viewers' read-only video boundary.
package viewer

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var sourceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
var sessionPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const bodyLimit = 256 << 10

// WHEPProxy permits only read sessions for named sources at the two configured
// 127.0.0.1 ports. Ports must be in 1..65535 and source IDs must match the lab's
// lowercase ID format; invalid configuration returns a handler that fails closed.
// Caller Host and Origin checks belong in the outer diagnostic handler.
//
// The client is copied: redirects and its cookie jar are disabled, and its
// timeout is capped at eight seconds. Its transport is trusted configuration.
// No destination URL, publish route or browser credentials are forwarded.
func WHEPProxy(sourceIDs []string, localPort, forwardedPort int, client *http.Client) http.Handler {
	invalidConfig := localPort < 1 || localPort > 65535 || forwardedPort < 1 || forwardedPort > 65535
	allowed := map[string]bool{}
	for _, id := range sourceIDs {
		if !sourceIDPattern.MatchString(id) {
			invalidConfig = true
		}
		allowed[id] = true
	}
	if invalidConfig {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Invalid local video receiver configuration.", http.StatusInternalServerError)
		})
	}
	boundedClient := http.Client{}
	if client != nil {
		boundedClient = *client
	}
	boundedClient.Jar = nil
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if boundedClient.Timeout <= 0 || boundedClient.Timeout > 8*time.Second {
		boundedClient.Timeout = 8 * time.Second
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/whep/"), "/")
		if !strings.HasPrefix(r.URL.Path, "/whep/") || (len(parts) != 2 && len(parts) != 3) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" || !sourceIDPattern.MatchString(parts[1]) || !allowed[parts[1]] {
			http.NotFound(w, r)
			return
		}
		port := localPort
		if parts[0] == "forwarded" {
			port = forwardedPort
		} else if parts[0] != "local" {
			http.NotFound(w, r)
			return
		}
		withSession := len(parts) == 3
		if withSession && !sessionPattern.MatchString(parts[2]) {
			http.NotFound(w, r)
			return
		}
		if (!withSession && r.Method != http.MethodOptions && r.Method != http.MethodPost) || (withSession && r.Method != http.MethodPatch && r.Method != http.MethodDelete) {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, bodyLimit))
		if err != nil {
			http.Error(w, "Request body is too large or unreadable.", http.StatusRequestEntityTooLarge)
			return
		}
		upstreamPath := "/" + parts[1] + "/whep"
		if withSession {
			upstreamPath += "/" + parts[2]
		}
		target := "http://127.0.0.1:" + strconv.Itoa(port) + upstreamPath
		request, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
		if err != nil {
			http.Error(w, "Could not prepare receiver request.", http.StatusBadGateway)
			return
		}
		for _, key := range []string{"Content-Type", "If-Match"} {
			if value := r.Header.Get(key); value != "" {
				request.Header.Set(key, value)
			}
		}
		response, err := boundedClient.Do(request)
		if err != nil {
			http.Error(w, "Local video receiver is unavailable.", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, bodyLimit+1))
		if err != nil || len(data) > bodyLimit {
			http.Error(w, "Invalid receiver response.", http.StatusBadGateway)
			return
		}
		if location := response.Header.Get("Location"); location != "" {
			base, _ := url.Parse(target)
			u, e := base.Parse(location)
			prefix := "/" + parts[1] + "/whep/"
			if e != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, prefix) || !sessionPattern.MatchString(strings.TrimPrefix(u.Path, prefix)) {
				http.Error(w, "Invalid receiver session location.", http.StatusBadGateway)
				return
			}
			w.Header().Set("Location", "/whep/"+parts[0]+"/"+parts[1]+"/"+strings.TrimPrefix(u.Path, prefix))
		}
		for _, key := range []string{"Content-Type", "ETag", "Accept-Patch", "Accept-Post", "Link"} {
			if value := response.Header.Get(key); value != "" {
				w.Header().Set(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(data)
	})
}
