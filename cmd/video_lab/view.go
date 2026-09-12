package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"fieldvideolab/internal/gstreamer"
	"fieldvideolab/internal/lab"
)

const liveViewLogLimit = 1 << 20

type viewOptions struct {
	Route, Decoder, GSTLaunch string
	LatencyMS                 int
	DryRun                    bool
}

func parseViewOptions(args []string, output io.Writer) (viewOptions, error) {
	cfg := viewOptions{Route: "local"}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cfg.Route, args = args[0], args[1:]
	}
	flags := flag.NewFlagSet("view", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.IntVar(&cfg.LatencyMS, "latency-ms", 50, "receiver waiting allowance: 50 ms, or the longer 100 ms option")
	flags.StringVar(&cfg.Decoder, "decoder", "software", "software or hardware video decoder")
	flags.StringVar(&cfg.GSTLaunch, "gst-launch", "", "explicit gst-launch executable; otherwise use the private runtime or PATH")
	flags.BoolVar(&cfg.DryRun, "dry-run", false, "print the planned viewer without opening it or writing files")
	if err := flags.Parse(args); err != nil {
		return cfg, err
	}
	if flags.NArg() != 0 || (cfg.Route != "local" && cfg.Route != "forwarded") {
		return cfg, errors.New("use view [local|forwarded] followed by viewer flags")
	}
	if cfg.LatencyMS != 50 && cfg.LatencyMS != 100 {
		return cfg, errors.New("latency-ms must be 50 or 100")
	}
	if cfg.Decoder != "software" && cfg.Decoder != "hardware" {
		return cfg, errors.New("decoder must be software or hardware")
	}
	return cfg, nil
}

// This explicit data shape must not grow to include private lab settings.
type liveViewConfig struct {
	Version   int    `json:"version"`
	Source    string `json:"source"`
	Route     string `json:"route"`
	Decoder   string `json:"decoder"`
	LatencyMS int    `json:"latency_ms"`
	Sink      string `json:"sink"`
	Lifetime  string `json:"lifetime"`
}

type liveViewReport struct {
	Version         int              `json:"version"`
	Outcome         string           `json:"outcome"`
	Process         gstreamer.Result `json:"process"`
	LogBytesKept    int64            `json:"log_bytes_kept"`
	LogBytesDropped int64            `json:"log_bytes_dropped"`
	LogComplete     bool             `json:"log_complete"`
	Notes           []string         `json:"notes"`
}

func liveViewDirectory(p lab.Paths) (string, error) {
	if err := os.MkdirAll(p.Reports, 0700); err != nil {
		return "", errors.New("private viewer reports directory is unavailable")
	}
	info, err := os.Lstat(p.Reports)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("viewer reports must use a real directory, not a symlink")
	}
	directory, err := os.MkdirTemp(p.Reports, "live-view-")
	if err != nil {
		return "", errors.New("private viewer directory could not be created")
	}
	return directory, nil
}

func writeViewJSON(path string, value any) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	writeErr := encoder.Encode(value)
	syncErr, closeErr := file.Sync(), file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

var closedWindowError = regexp.MustCompile(`^ERROR: from element (/\S+): Output window was closed\.?$`)

// glimagesink commonly reports closing its native window as exit status 1.
// Accept only its specific close diagnostic with a complete retained log and
// no unrelated ERROR lines; exit status 1 alone is always a viewer failure.
func windowWasClosed(log []byte) bool {
	scanner := bufio.NewScanner(bytes.NewReader(log))
	scanner.Buffer(make([]byte, 4096), liveViewLogLimit+1)
	found := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "ERROR") {
			continue
		}
		match := closedWindowError.FindStringSubmatch(line)
		if match == nil || !strings.Contains(match[1], "/GstGLImageSink:") {
			return false
		}
		found = true
	}
	return found && scanner.Err() == nil
}

func classifyViewResult(result gstreamer.Result, log []byte, logComplete bool) (string, error) {
	if result.ForcedStop {
		return "forced_stop", errors.New("viewer required a forced shutdown; see its private report")
	}
	if result.Result == "canceled" {
		return "stopped", nil
	}
	if result.Result == "exited" && result.ExitCode != nil {
		if *result.ExitCode == 0 && result.ProcessError == "" {
			return "closed", nil
		}
		if *result.ExitCode == 1 && result.ProcessError == "exit status 1" && logComplete && windowWasClosed(log) {
			return "window_closed", nil
		}
		return "failed", fmt.Errorf("viewer exited with status %d; see its private log", *result.ExitCode)
	}
	if result.Result == "start_failed" {
		return "start_failed", errors.New("viewer could not start; see its private report")
	}
	return "failed", errors.New("viewer ended unexpectedly; see its private report")
}

// Query only the selected receiver. A missing publisher is different from an
// unavailable API: waiting for the camera cannot repair a stopped lab service.
func viewSourceReady(ctx context.Context, client *http.Client, id, route string) (bool, error) {
	port := lab.Field.API
	if route == "forwarded" {
		port = lab.Central.API
	} else if route != "local" {
		return false, errors.New("unknown viewer route")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d/v3/paths/get/%s", port, url.PathEscape(id)), nil)
	if err != nil {
		return false, err
	}
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if response.StatusCode != http.StatusOK {
		return false, errors.New("receiver status is unavailable")
	}
	const bodyLimit = 64 << 10
	body, err := io.ReadAll(io.LimitReader(response.Body, bodyLimit+1))
	if err != nil || len(body) > bodyLimit {
		return false, errors.New("receiver status could not be read")
	}
	var status struct {
		Ready     *bool `json:"ready"`
		Online    *bool `json:"online"`
		Available *bool `json:"available"`
	}
	if json.Unmarshal(body, &status) != nil {
		return false, errors.New("receiver status is invalid")
	}
	for _, ready := range []*bool{status.Ready, status.Online, status.Available} {
		if ready != nil {
			return *ready, nil
		}
	}
	return false, errors.New("receiver status has no readiness field")
}

func waitForViewSource(ctx context.Context, id, route string, timeout, pollEvery time.Duration, probe func(context.Context) (bool, error), output io.Writer) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	waiting := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if waitCtx.Err() != nil {
			instruction := fmt.Sprintf("start broadcasting from %s", id)
			if route == "forwarded" {
				instruction += " and check that its forwarding is enabled"
			}
			return fmt.Errorf("no video arrived for %s / %s within %s; %s, then run ./lab --source %s view %s again", id, route, timeout, instruction, id, route)
		}
		ready, err := probe(waitCtx)
		if waitCtx.Err() != nil {
			continue
		}
		if err != nil {
			return fmt.Errorf("cannot read %s receiver status for %s; run ./lab start, then retry ./lab --source %s view %s", route, id, id, route)
		}
		if ready {
			return nil
		}
		if !waiting {
			if _, err := fmt.Fprintf(output, "Waiting up to %s for %s / %s. Start broadcasting from that device; Ctrl+C cancels this viewer.\n", timeout, id, route); err != nil {
				return err
			}
			waiting = true
		}
		timer := time.NewTimer(pollEvery)
		select {
		case <-waitCtx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func awaitViewSource(ctx context.Context, id, route string, output io.Writer) error {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return waitForViewSource(ctx, id, route, time.Minute, time.Second, func(checkCtx context.Context) (bool, error) {
		return viewSourceReady(checkCtx, client, id, route)
	}, output)
}

func runView(ctx context.Context, p lab.Paths, selected string, cfg viewOptions, output io.Writer) error {
	return runViewWithSourceWait(ctx, p, selected, cfg, output, awaitViewSource)
}

func runViewWithSourceWait(ctx context.Context, p lab.Paths, selected string, cfg viewOptions, output io.Writer, awaitSource func(context.Context, string, string, io.Writer) error) error {
	id, err := sourceID(p, selected)
	if err != nil {
		return err
	}
	settings, err := p.LoadSettings()
	if err != nil {
		return err
	}
	config := gstreamer.Config{Source: id, Route: cfg.Route, Decoder: cfg.Decoder, Sink: "gl", LatencyMS: cfg.LatencyMS}
	argv, err := gstreamer.Pipeline(config, lab.GetSources(settings))
	if err != nil {
		return err
	}
	safeConfig := liveViewConfig{Version: 1, Source: id, Route: cfg.Route, Decoder: cfg.Decoder,
		LatencyMS: cfg.LatencyMS, Sink: "gl", Lifetime: "until the native window closes or the command is interrupted"}
	if cfg.DryRun {
		// Dry-run works without GStreamer installed and never serializes settings.
		planned := struct {
			Config              liveViewConfig `json:"config"`
			ExecutableSelection string         `json:"executable_selection"`
			Arguments           []string       `json:"arguments"`
		}{safeConfig, "explicit --gst-launch override, otherwise private runtime then PATH", argv}
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(planned)
	}
	executable, err := gstreamer.ResolveExecutable(p.Root, cfg.GSTLaunch)
	if err != nil {
		return err
	}
	if err := awaitSource(ctx, id, cfg.Route, output); err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil
		}
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	directory, err := liveViewDirectory(p)
	if err != nil {
		return err
	}
	if err := writeViewJSON(filepath.Join(directory, "config.json"), safeConfig); err != nil {
		return errors.New("private viewer configuration could not be saved")
	}
	logPath := filepath.Join(directory, "process.log")
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("private viewer log could not be created")
	}
	log := gstreamer.NewLimitedLog(logFile, liveViewLogLimit)
	if _, err := fmt.Fprintf(output, "Opening %s / %s. Close its window or press Ctrl+C to stop this viewer.\nPrivate viewer report: %s\n", id, cfg.Route, directory); err != nil {
		_ = logFile.Close()
		return err
	}
	cmd := exec.Command(executable, argv...)
	cmd.Dir = p.Root
	// Stable English diagnostics allow narrow classification of native closing.
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "GST_DEBUG_NO_COLOR=1")
	result := gstreamer.RunProcess(ctx, cmd, 0, 2*time.Second, log)
	kept, dropped, logErr := log.Snapshot()
	syncErr, closeErr := logFile.Sync(), logFile.Close()
	data, readErr := os.ReadFile(logPath)
	logComplete := logErr == nil && syncErr == nil && closeErr == nil && readErr == nil && dropped == 0
	outcome, runErr := classifyViewResult(result, data, logComplete)
	report := liveViewReport{Version: 1, Outcome: outcome, Process: result, LogBytesKept: kept, LogBytesDropped: dropped, LogComplete: logComplete,
		Notes: []string{
			"This records viewer lifetime, not proof that pictures were shown or a measurement of camera-to-screen delay.",
			"The receiver waiting allowance is not total picture delay. TCP can also wait for missing data.",
			"Only this viewer's process group is stopped; the camera, forwarding, recordings and uploads keep their settings.",
		}}
	reportErr := writeViewJSON(filepath.Join(directory, "run.json"), report)
	if logErr != nil || syncErr != nil || closeErr != nil || readErr != nil || reportErr != nil {
		return errors.Join(runErr, errors.New("the private viewer log or report could not be completely saved"))
	}
	if runErr != nil {
		return runErr
	}
	_, err = fmt.Fprintln(output, "Viewer closed.")
	return err
}

func viewCommand(p lab.Paths, selected string, args []string, output, errorOutput io.Writer) error {
	cfg, err := parseViewOptions(args, errorOutput)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runView(ctx, p, selected, cfg, output)
}
