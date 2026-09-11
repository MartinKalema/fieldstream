package main

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var sourceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
var sessionPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const bodyLimit = 256 << 10

// Only read sessions for named sources can pass. No destination URL, arbitrary
// port, publish route, browser credentials or upstream Origin is forwarded.
func whepProxy(sourceIDs []string, client *http.Client) http.Handler {
	allowed := map[string]bool{}
	for _, id := range sourceIDs {
		allowed[id] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/whep/"), "/")
		if (len(parts) != 2 && len(parts) != 3) || r.URL.RawQuery != "" || r.URL.RawPath != "" || !sourceIDPattern.MatchString(parts[1]) || !allowed[parts[1]] {
			http.NotFound(w, r)
			return
		}
		port := "18889"
		if parts[0] == "forwarded" {
			port = "28889"
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
		target := "http://127.0.0.1:" + port + upstreamPath
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
		response, err := client.Do(request)
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
			if e != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, prefix) || !sessionPattern.MatchString(strings.TrimPrefix(u.Path, prefix)) {
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
