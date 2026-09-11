// browsercheck serves a separate, bounded WebRTC browser-buffer experiment.
// It does not read lab credentials, modify settings or control media services.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed assets
var assets embed.FS

func handler(port int, sourceIDs []string, projectRoot string) http.Handler {
	files, _ := fs.Sub(assets, "assets")
	static := http.FileServer(http.FS(files))
	proxy := whepProxy(sourceIDs, &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})
	saveReport := reportSaver(projectRoot)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "127.0.0.1:"+strconv.Itoa(port) {
			http.Error(w, "Use the printed loopback address.", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; media-src blob:; img-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.URL.Path == "/report" {
			if r.Header.Get("Origin") != "http://"+r.Host || (r.Header.Get("Sec-Fetch-Site") != "" && r.Header.Get("Sec-Fetch-Site") != "same-origin") {
				http.Error(w, "Save reports from this diagnostic page.", http.StatusForbidden)
				return
			}
			saveReport.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/whep/") {
			if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
				http.Error(w, "Unexpected request origin.", http.StatusForbidden)
				return
			}
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				http.Error(w, "Use this diagnostic page directly.", http.StatusForbidden)
				return
			}
			proxy.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/", "/app.mjs", "/metrics.mjs", "/style.css", "/reader.js", "/mediamtx-LICENSE.txt":
		default:
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/app.mjs" || r.URL.Path == "/metrics.mjs" {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		}
		static.ServeHTTP(w, r)
	})
}

func main() {
	port := flag.Int("port", 19081, "loopback HTTP port")
	sources := flag.String("sources", "camera-01", "comma-separated source IDs allowed by the diagnostic")
	root := flag.String("root", ".", "project directory for private reports")
	flag.Parse()
	if *port < 1024 || *port > 65535 {
		fmt.Fprintln(os.Stderr, "port must be between 1024 and 65535")
		os.Exit(2)
	}
	ids := strings.Split(*sources, ",")
	if len(ids) > 4 {
		fmt.Fprintln(os.Stderr, "at most four source IDs are allowed")
		os.Exit(2)
	}
	for _, id := range ids {
		if !sourceIDPattern.MatchString(id) {
			fmt.Fprintln(os.Stderr, "invalid source ID")
			os.Exit(2)
		}
	}
	projectRoot, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid project directory")
		os.Exit(2)
	}
	if info, err := os.Stat(projectRoot); err != nil || !info.IsDir() {
		fmt.Fprintln(os.Stderr, "project directory must already exist")
		os.Exit(2)
	}
	server := &http.Server{Addr: "127.0.0.1:" + strconv.Itoa(*port), Handler: handler(*port, ids, projectRoot), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 12 * time.Second, IdleTimeout: 30 * time.Second}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		deadline, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_ = server.Shutdown(deadline)
	}()
	fmt.Printf("Optional browser buffer check: http://127.0.0.1:%d\nNo video opens until Start comparison is pressed.\n", *port)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
