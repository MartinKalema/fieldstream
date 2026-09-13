package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"fieldvideolab/internal/lab"
)

func recordingsCommand(p lab.Paths, source string, args []string, output io.Writer) error {
	if len(args) == 0 || (args[0] != "check" && args[0] != "health") {
		return errors.New("use recordings check [--limit 20] [--path SESSION/FILE.mp4] [--recheck] [--json], or recordings health [--json]")
	}
	flags := flag.NewFlagSet("recordings "+args[0], flag.ContinueOnError)
	flags.SetOutput(output)
	asJSON := flags.Bool("json", false, "print the dated report as JSON")
	limit, recheck := 20, false
	var recordingPath string
	if args[0] == "check" {
		flags.IntVar(&limit, "limit", 20, "maximum completed files to check, from 1 to 1000")
		flags.BoolVar(&recheck, "recheck", false, "check previously checked files again")
		flags.StringVar(&recordingPath, "path", "", "check one exact relative filename from the catalog")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected recordings arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var report lab.VideoHealthReport
	var checked int
	var err error
	if args[0] == "check" {
		settings, loadErr := p.LoadSettings()
		if loadErr != nil {
			return loadErr
		}
		report, checked, err = p.CheckRecordingHealth(ctx, settings.FFmpeg, lab.VideoHealthOptions{SourceID: source, Path: recordingPath, Limit: limit, Recheck: recheck})
	} else {
		report, err = p.RecordingHealth(ctx, source)
	}
	if err != nil {
		return err
	}
	if *asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(struct {
			lab.VideoHealthReport
			CheckedThisRun int `json:"checked_this_run"`
		}{report, checked})
	}
	if args[0] == "check" {
		fmt.Fprintf(output, "Checked %d completed recording(s) this run.\n", checked)
	}
	fmt.Fprintf(output, "Saved video checks: %d decoded, %d decode errors, %d checks failed, %d unchecked.\n", report.Summary.Decodable, report.Summary.DecodeError, report.Summary.CheckFailed, report.Summary.Unchecked)
	if report.Summary.Missing > 0 {
		fmt.Fprintf(output, "Catalog reports %d local recording(s) missing; an earlier decode result does not prove they still exist.\n", report.Summary.Missing)
	}
	shown := 0
	for _, result := range report.Results {
		if result.State == "decodable" {
			continue
		}
		if shown == 20 {
			fmt.Fprintln(output, "More results are available with --json.")
			break
		}
		fmt.Fprintf(output, "  %q: %s (%s), checked %s\n", result.Path, lab.VideoHealthDescription(result.State), result.Reason, result.CheckedAt.Local().Format("2006-01-02 15:04:05 MST"))
		shown++
	}
	fmt.Fprintln(output, "These are dated decode results, not a check for missing scenes, visual quality or the current R2 copy.")
	fmt.Fprintln(output, "Private report:", p.VideoHealthPath())
	return nil
}
