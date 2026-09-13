// fixture serves the production clock UI with an isolated, browser-generated
// video source. It never connects to WHEP, the live camera, recordings or R2.
package main

import (
	"bytes"
	"context"
	"embed"
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
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed reader.js fixture.css
var fixtureAssets embed.FS

const toolbar = `<aside class="fixture-toolbar" aria-label="Test fixture controls">
<strong>TEST ONLY — generated video inside this browser. No live camera or server is connected.</strong>
<p>These controls stop canvas source frames while keeping the test peer connections open.</p>
<div>
<button id="fixture-hold-forwarded" type="button">TEST: Hold forwarded frames</button>
<button id="fixture-resume-forwarded" type="button">TEST: Resume forwarded frames</button>
<button id="fixture-hold-both" type="button">TEST: Hold both sources</button>
<button id="fixture-resume-both" type="button">TEST: Resume both sources</button>
<button id="fixture-end-forwarded" type="button">TEST: End forwarded connection</button>
<button id="fixture-pause-forwarded-player" type="button">TEST: Pause forwarded player</button>
<button id="fixture-play-forwarded-player" type="button">TEST: Play forwarded player</button>
</div><pre id="fixture-status" aria-live="polite">TEST sources starting…</pre>
</aside>`

type options struct{ root, listen string }

func parseOptions(args []string, output io.Writer) (options, error) {
	var cfg options
	flags := flag.NewFlagSet("fixture", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&cfg.root, "root", ".", "repository containing the production clock UI")
	flags.StringVar(&cfg.listen, "listen", "127.0.0.1:19090", "loopback IP and port")
	if err := flags.Parse(args); err != nil {
		return cfg, err
	}
	if flags.NArg() != 0 {
		return cfg, errors.New("use flags only")
	}
	host, port, err := net.SplitHostPort(cfg.listen)
	ip, ipErr := netip.ParseAddr(host)
	p, portErr := strconv.Atoi(port)
	if err != nil || ipErr != nil || !ip.IsLoopback() || portErr != nil || p < 0 || p > 65535 {
		return cfg, errors.New("listen must be a loopback IP and port from 0 to 65535")
	}
	cfg.root, err = filepath.Abs(cfg.root)
	if err != nil {
		return cfg, err
	}
	cfg.root, err = filepath.EvalSymlinks(cfg.root)
	if err != nil {
		return cfg, errors.New("repository root must exist")
	}
	return cfg, nil
}

func readProjectFile(root, relative string) ([]byte, error) {
	path, err := filepath.EvalSymlinks(filepath.Join(root, relative))
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("production asset must remain inside the repository")
	}
	return os.ReadFile(path)
}

func pageHandler(cfg options) (http.Handler, error) {
	page, err := readProjectFile(cfg.root, "cmd/clockcheck/clock.html")
	if err != nil {
		return nil, err
	}
	injected := strings.Replace(string(page), "</head>", "<link rel=\"stylesheet\" href=\"/fixture.css\">\n</head>", 1)
	injected = strings.Replace(injected, "<body>", "<body>\n"+toolbar, 1)
	if !strings.Contains(injected, "fixture-toolbar") {
		return nil, errors.New("production clock template has no expected body tag")
	}
	tmpl, err := template.New("clock").Parse(injected)
	if err != nil {
		return nil, err
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, struct{ Source, AllowedSources string }{"camera-01", "camera-01"}); err != nil {
		return nil, err
	}
	type asset struct {
		data []byte
		mime string
	}
	assets := map[string]asset{"/": {rendered.Bytes(), "text/html; charset=utf-8"}}
	for route, file := range map[string]string{
		"/app.mjs": "cmd/clockcheck/assets/app.mjs", "/watch.mjs": "cmd/clockcheck/assets/watch.mjs",
		"/style.css": "cmd/clockcheck/assets/style.css",
	} {
		data, err := readProjectFile(cfg.root, file)
		if err != nil {
			return nil, err
		}
		mime := "text/javascript; charset=utf-8"
		if route == "/style.css" {
			mime = "text/css; charset=utf-8"
		}
		assets[route] = asset{data, mime}
	}
	for route, name := range map[string]string{"/reader.js": "reader.js", "/fixture.css": "fixture.css"} {
		data, err := fixtureAssets.ReadFile(name)
		if err != nil {
			return nil, err
		}
		mime := "text/javascript; charset=utf-8"
		if name == "fixture.css" {
			mime = "text/css; charset=utf-8"
		}
		assets[route] = asset{data, mime}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != cfg.listen {
			http.Error(w, "Use the printed loopback fixture address.", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; media-src blob:; img-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		data, ok := assets[r.URL.Path]
		if !ok || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", data.mime)
		if r.Method == http.MethodGet {
			_, _ = w.Write(data.data)
		}
	}), nil
}

func run(ctx context.Context, args []string, output io.Writer) error {
	cfg, err := parseOptions(args, output)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	cfg.listen = listener.Addr().String()
	handler, err := pageHandler(cfg)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 12 * time.Second, IdleTimeout: 30 * time.Second}
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
	fmt.Fprintf(output, "TEST ONLY clock UI fixture: http://%s\nGenerated canvas video stays within this browser; no live camera or WHEP server is contacted.\n", cfg.listen)
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "Fixture:", err)
		os.Exit(1)
	}
}
