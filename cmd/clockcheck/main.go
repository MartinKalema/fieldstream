// clockcheck serves the filmed clock and per-viewer playback-stall warnings.
// It never changes camera connections, recording or forwarding settings.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"fieldvideolab/internal/viewer"
)

//go:embed clock.html assets
var content embed.FS

var sourcePattern = regexp.MustCompile("^[a-z][a-z0-9-]{0,31}$")

type options struct {
	Listen         string
	Source         string
	LocalPort      int
	ForwardedPort  int
	Sources        []string
	AllowedSources string
}

func parseOptions(args []string, output io.Writer) (options, error) {
	var cfg options
	var sources string
	flags := flag.NewFlagSet("clockcheck", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&cfg.Listen, "listen", "127.0.0.1:19080", "loopback IP and port for this page")
	flags.StringVar(&cfg.Source, "source", "camera-01", "source ID shown initially")
	flags.StringVar(&sources, "sources", "", "allowed source IDs, comma-separated; defaults to --source")
	flags.IntVar(&cfg.LocalPort, "local-port", 18889, "local viewer HTTP port")
	flags.IntVar(&cfg.ForwardedPort, "forwarded-port", 28889, "forwarded viewer HTTP port")
	if err := flags.Parse(args); err != nil {
		return cfg, err
	}
	if flags.NArg() != 0 || !sourcePattern.MatchString(cfg.Source) {
		return cfg, errors.New("use flags only and a valid source ID")
	}
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return cfg, errors.New("listen must be a loopback IP and port")
	}
	ip, err := netip.ParseAddr(host)
	listenPort, portErr := strconv.Atoi(port)
	if err != nil || !ip.IsLoopback() || portErr != nil || listenPort < 0 || listenPort > 65535 {
		return cfg, errors.New("listen must be a loopback IP and a port from 0 to 65535")
	}
	if cfg.LocalPort < 1 || cfg.LocalPort > 65535 || cfg.ForwardedPort < 1 || cfg.ForwardedPort > 65535 {
		return cfg, errors.New("viewer ports must be from 1 to 65535")
	}
	if sources == "" {
		sources = cfg.Source
	}
	cfg.Sources = strings.Split(sources, ",")
	seen := map[string]bool{}
	if len(cfg.Sources) > 4 {
		return cfg, errors.New("at most four source IDs are allowed")
	}
	for _, id := range cfg.Sources {
		if !sourcePattern.MatchString(id) || seen[id] {
			return cfg, errors.New("allowed source IDs must be valid and distinct")
		}
		seen[id] = true
	}
	if !seen[cfg.Source] {
		return cfg, errors.New("initial source must be in the allowed sources")
	}
	cfg.AllowedSources = strings.Join(cfg.Sources, ",")
	return cfg, nil
}

func pageHandler(cfg options) (http.Handler, error) {
	tmpl, err := template.ParseFS(content, "clock.html")
	if err != nil {
		return nil, err
	}
	files, err := fs.Sub(content, "assets")
	if err != nil {
		return nil, err
	}
	static := http.FileServer(http.FS(files))
	proxy := viewer.WHEPProxy(cfg.Sources, cfg.LocalPort, cfg.ForwardedPort,
		&http.Client{Timeout: 8 * time.Second})
	allowed := map[string]bool{}
	for _, id := range cfg.Sources {
		allowed[id] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != cfg.Listen {
			http.Error(w, "Use the printed loopback address.", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; media-src blob:; img-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if strings.HasPrefix(r.URL.Path, "/whep/") {
			origin, site := r.Header.Get("Origin"), r.Header.Get("Sec-Fetch-Site")
			if (origin != "" && origin != "http://"+cfg.Listen) || (site != "" && site != "same-origin" && site != "none") {
				http.Error(w, "Open this comparison page directly.", http.StatusForbidden)
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
		case "/":
			pageConfig := cfg
			if requested := r.URL.Query().Get("source"); requested != "" {
				if !allowed[requested] {
					http.Error(w, "Source is not allowed by this comparison server.", http.StatusBadRequest)
					return
				}
				pageConfig.Source = requested
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if r.Method != http.MethodHead {
				_ = tmpl.ExecuteTemplate(w, "clock.html", pageConfig)
			}
		case "/reader.js", "/mediamtx-LICENSE.txt":
			data := viewer.ReaderJS
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			if r.URL.Path == "/mediamtx-LICENSE.txt" {
				data = viewer.ReaderLicense
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			}
			if r.Method != http.MethodHead {
				_, _ = w.Write(data)
			}
		case "/app.mjs", "/watch.mjs", "/style.css":
			if strings.HasSuffix(r.URL.Path, ".mjs") {
				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			}
			static.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	}), nil
}

func run(ctx context.Context, args []string, output io.Writer) error {
	cfg, err := parseOptions(args, output)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	cfg.Listen = listener.Addr().String()
	handler, err := pageHandler(cfg)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 12 * time.Second, IdleTimeout: 30 * time.Second,
	}
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			deadline, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if server.Shutdown(deadline) != nil {
				_ = server.Close()
			}
		case <-finished:
		}
	}()
	fmt.Fprintf(output, "Local camera delay check: http://%s\nPlayback warnings do not verify camera capture time.\n", listener.Addr())
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "Clock check:", err)
		os.Exit(1)
	}
}
