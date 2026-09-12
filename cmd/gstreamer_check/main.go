// gstreamer_check opens a bounded, read-only viewer of an existing lab source.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"fieldvideolab/internal/gstreamer"
	"fieldvideolab/internal/lab"
)

type options struct {
	Root      string        `json:"root"`
	Source    string        `json:"source"`
	Route     string        `json:"route"`
	Duration  time.Duration `json:"-"`
	LatencyMS int           `json:"latency_ms"`
	Decoder   string        `json:"decoder"`
	Sink      string        `json:"sink"`
	GSTLaunch string        `json:"-"`
	DryRun    bool          `json:"-"`
}

var sourcePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func parseOptions(args []string, output io.Writer) (options, error) {
	var cfg options
	flags := flag.NewFlagSet("gstreamer_check", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&cfg.Root, "root", ".", "lab project folder")
	flags.StringVar(&cfg.Source, "source", "camera-01", "registered source ID")
	flags.StringVar(&cfg.Route, "route", "local", "local or forwarded picture")
	flags.DurationVar(&cfg.Duration, "duration", 30*time.Second, "viewer duration, from 5s to 120s")
	flags.IntVar(&cfg.LatencyMS, "latency-ms", 100, "requested receiver waiting time, from 0 to 200 milliseconds; lower settings may produce broken pictures")
	flags.StringVar(&cfg.Decoder, "decoder", "software", "software (avdec_h264) or hardware (vtdec_hw)")
	flags.StringVar(&cfg.Sink, "sink", "gl", "gl (native window) or headless (no picture displayed)")
	flags.StringVar(&cfg.GSTLaunch, "gst-launch", "", "executable override; otherwise use the private runtime or PATH")
	flags.BoolVar(&cfg.DryRun, "dry-run", false, "print the argument list without launching or writing a report")
	if err := flags.Parse(args); err != nil {
		return cfg, err
	}
	if flags.NArg() != 0 || cfg.Root == "" || !sourcePattern.MatchString(cfg.Source) {
		return cfg, errors.New("use flags, a project folder and a valid source ID")
	}
	if cfg.Duration < 5*time.Second || cfg.Duration > 120*time.Second {
		return cfg, errors.New("duration must be from 5s to 120s")
	}
	if cfg.LatencyMS < 0 || cfg.LatencyMS > 200 {
		return cfg, errors.New("latency-ms must be from 0 to 200")
	}
	if cfg.Route != "local" && cfg.Route != "forwarded" {
		return cfg, errors.New("route must be local or forwarded")
	}
	if cfg.Decoder != "software" && cfg.Decoder != "hardware" {
		return cfg, errors.New("decoder must be software or hardware")
	}
	if cfg.Sink != "gl" && cfg.Sink != "headless" {
		return cfg, errors.New("sink must be gl or headless")
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return cfg, errors.New("project folder unavailable")
	}
	cfg.Root = root
	return cfg, nil
}

func pipeline(cfg options, sources []lab.SourceConfig) ([]string, error) {
	return gstreamer.Pipeline(gstreamer.Config{Source: cfg.Source, Route: cfg.Route,
		Decoder: cfg.Decoder, Sink: cfg.Sink, LatencyMS: cfg.LatencyMS}, sources)
}

type report struct {
	Version          int              `json:"version"`
	Config           options          `json:"config"`
	RequestedSeconds float64          `json:"requested_seconds"`
	Argv             []string         `json:"argv"`
	Process          gstreamer.Result `json:"process"`
	LogBytesKept     int64            `json:"log_bytes_kept"`
	LogBytesDropped  int64            `json:"log_bytes_dropped"`
	Notes            []string         `json:"notes"`
}

func privateReportDir(root string) (string, error) {
	base := lab.NewPaths(root).Reports
	if err := os.MkdirAll(base, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(base)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("reports must be a real directory")
	}
	return os.MkdirTemp(base, "gstreamer-check-")
}

func run(ctx context.Context, args []string, output, errorOutput io.Writer) int {
	cfg, err := parseOptions(args, errorOutput)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(errorOutput, err)
		return 2
	}
	settings, err := lab.NewPaths(cfg.Root).LoadSettings()
	if err != nil {
		fmt.Fprintln(errorOutput, err)
		return 1
	}
	argv, err := pipeline(cfg, lab.GetSources(settings))
	if err != nil {
		fmt.Fprintln(errorOutput, err)
		return 1
	}
	if cfg.DryRun {
		executable := cfg.GSTLaunch
		if executable == "" {
			executable, err = gstreamer.ResolveExecutable(cfg.Root, "")
			if err != nil {
				// The media arguments are still useful before installation. This
				// placeholder is not a successful executable selection; an actual
				// run still requires ResolveExecutable to succeed.
				executable = "gst-launch-1.0"
				fmt.Fprintf(errorOutput, "Dry run: executable not resolved (%v). The first argument below is a placeholder; a real run currently cannot start.\n", err)
			}
		}
		return printJSON(output, append([]string{executable}, argv...), errorOutput)
	}
	executable, err := gstreamer.ResolveExecutable(cfg.Root, cfg.GSTLaunch)
	if err != nil {
		fmt.Fprintln(errorOutput, err)
		return 1
	}
	directory, err := privateReportDir(cfg.Root)
	if err != nil {
		fmt.Fprintln(errorOutput, "cannot create private report:", err)
		return 1
	}
	logFile, err := os.OpenFile(filepath.Join(directory, "process.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fmt.Fprintln(errorOutput, "cannot create private log:", err)
		return 1
	}
	log := gstreamer.NewLimitedLog(logFile, 1<<20)
	fmt.Fprintf(output, "Opening the %s picture for up to %.0f seconds. Private report: %s\n", cfg.Route, cfg.Duration.Seconds(), directory)
	cmd := exec.Command(executable, argv...)
	cmd.Dir = cfg.Root
	result := gstreamer.RunProcess(ctx, cmd, cfg.Duration, 2*time.Second, log)
	syncErr := logFile.Sync()
	closeErr := logFile.Close()
	kept, dropped, logErr := log.Snapshot()
	r := report{Version: 1, Config: cfg, RequestedSeconds: cfg.Duration.Seconds(), Argv: append([]string{executable}, argv...), Process: result,
		LogBytesKept: kept, LogBytesDropped: dropped,
		Notes: []string{
			"Process lifetime is not an end-to-end video delay measurement or proof that pictures were displayed.",
			"The requested receiver waiting time is not the total picture delay. TCP can also wait for missing data.",
			"This viewer reads an existing source; camera, forwarding, recording and upload settings are unchanged.",
			"Headless mode discards decoded pictures and cannot measure screen presentation.",
		}}
	data, err := json.MarshalIndent(r, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(directory, "report.json"), append(data, '\n'), 0600)
	}
	if err != nil || syncErr != nil || closeErr != nil || logErr != nil {
		fmt.Fprintln(errorOutput, "the private report or log could not be fully written")
		return 1
	}
	fmt.Fprintf(output, "Viewer result: %s. This does not establish picture delay.\n", result.Result)
	switch result.Result {
	case "duration_elapsed":
		return 0
	case "canceled":
		return 130
	default:
		return 1
	}
}

func printJSON(output io.Writer, value any, errorOutput io.Writer) int {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintln(errorOutput, "cannot write output:", err)
		return 1
	}
	return 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
