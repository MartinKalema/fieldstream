package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
)

//go:embed page.html page.mjs
var assets embed.FS

func comparisonHandler(dir, allowedHost string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != allowedHost {
			http.Error(w, "Use the printed loopback address.", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; media-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Read-only comparison page.", http.StatusMethodNotAllowed)
			return
		}
		asset := ""
		switch r.URL.Path {
		case "/":
			asset = "page.html"
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case "/page.mjs":
			asset = "page.mjs"
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		}
		if asset != "" {
			data, err := assets.ReadFile(asset)
			if err != nil {
				http.Error(w, "Comparison UI unavailable.", http.StatusInternalServerError)
				return
			}
			http.ServeContent(w, r, asset, time.Time{}, strings.NewReader(string(data)))
			return
		}
		name := ""
		switch r.URL.Path {
		case "/report.json":
			name = "result.json"
			w.Header().Set("Content-Type", "application/json")
		case "/playback.mp4", "/detail-20.mp4", "/small-20.mp4":
			name = strings.TrimPrefix(r.URL.Path, "/")
			w.Header().Set("Content-Type", "video/mp4")
		default:
			http.NotFound(w, r)
			return
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer root.Close()
		f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, name, info.ModTime(), f)
	})
}

func serveComparison(ctx context.Context, listen, dir string, output io.Writer) error {
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return errors.New("comparison port is unavailable; choose another loopback port with --listen")
	}
	defer listener.Close()
	server := &http.Server{Handler: comparisonHandler(dir, listener.Addr().String()), ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second}
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if server.Shutdown(shutdown) != nil {
				_ = server.Close()
			}
		case <-finished:
		}
	}()
	fmt.Fprintf(output, "Saved-video comparison: http://%s/\n", listener.Addr())
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
