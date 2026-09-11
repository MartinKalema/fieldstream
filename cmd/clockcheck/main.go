// clockcheck serves the manual filmed-clock comparison page on this computer.
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"syscall"
	"time"
)

//go:embed clock.html
var page string

var sourcePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

type options struct {
	Listen        string
	Source        string
	LocalPort     int
	ForwardedPort int
}

func parseOptions(args []string, output io.Writer) (options, error) {
	var cfg options
	flags := flag.NewFlagSet("clockcheck", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&cfg.Listen, "listen", "127.0.0.1:19080", "loopback IP and port for this page")
	flags.StringVar(&cfg.Source, "source", "camera-01", "source ID shown initially")
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
	return cfg, nil
}

func pageHandler(cfg options) (http.Handler, error) {
	tmpl, err := template.New("clock").Parse(page)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method == http.MethodHead {
			return
		}
		_ = tmpl.Execute(w, cfg)
	})
	return mux, nil
}

func run(ctx context.Context, args []string, output io.Writer) error {
	cfg, err := parseOptions(args, output)
	if err != nil {
		return err
	}
	handler, err := pageHandler(cfg)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 3 * time.Second,
		WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second,
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
	fmt.Fprintf(output, "Local camera delay check: http://%s\n", listener.Addr())
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
