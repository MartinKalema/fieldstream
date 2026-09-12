// clock_check serves a clock to film when manually checking video delay.
// It does not receive video or change any camera or lab settings.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

//go:embed clock.html assets/clock.js assets/style.css
var content embed.FS

type options struct {
	Listen string
}

func parseOptions(args []string, output io.Writer) (options, error) {
	var cfg options
	flags := flag.NewFlagSet("clock_check", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&cfg.Listen, "listen", "127.0.0.1:19080", "loopback IP and port for this clock")
	if err := flags.Parse(args); err != nil {
		return cfg, err
	}
	if flags.NArg() != 0 {
		return cfg, errors.New("use only the --listen flag")
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
	return cfg, nil
}

func pageHandler(cfg options) (http.Handler, error) {
	type asset struct {
		file, mime string
		data       []byte
	}
	assets := map[string]asset{
		"/":          {file: "clock.html", mime: "text/html; charset=utf-8"},
		"/clock.js":  {file: "assets/clock.js", mime: "text/javascript; charset=utf-8"},
		"/style.css": {file: "assets/style.css", mime: "text/css; charset=utf-8"},
	}
	for path, item := range assets {
		data, err := content.ReadFile(item.file)
		if err != nil {
			return nil, err
		}
		item.data = data
		assets[path] = item
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'none'; media-src 'none'; worker-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.Host != cfg.Listen {
			http.Error(w, "Use the printed loopback address.", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		item, ok := assets[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.URL.RawQuery != "" {
			http.Error(w, "This clock does not accept query parameters.", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", item.mime)
		if r.Method != http.MethodHead {
			_, _ = w.Write(item.data)
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
	fmt.Fprintf(output, "Clock for manual video delay checks: http://%s\nFilm the white clock and place the GStreamer window beside it.\n", listener.Addr())
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
