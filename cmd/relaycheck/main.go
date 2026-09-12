// relaycheck reads the running lab. It never changes a video or upload setting.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"fieldvideolab/internal/lab"
)

var sourcePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
var identityPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var labelPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

type options struct {
	Root, Source string
	Duration     time.Duration
	Sample       bool
}

func parseOptions(args []string, output io.Writer) (options, error) {
	var cfg options
	flags := flag.NewFlagSet("relaycheck", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&cfg.Root, "root", ".", "lab project folder")
	flags.StringVar(&cfg.Source, "source", "camera-01", "source ID to observe")
	flags.DurationVar(&cfg.Duration, "duration", 30*time.Second, "observation duration, from 5s to 120s")
	flags.BoolVar(&cfg.Sample, "sample", false, "internal bounded read-only sample")
	if err := flags.Parse(args); err != nil {
		return cfg, err
	}
	if flags.NArg() != 0 || !sourcePattern.MatchString(cfg.Source) || cfg.Root == "" {
		return cfg, errors.New("use flags, a project folder and a valid source ID")
	}
	if cfg.Duration < 5*time.Second || cfg.Duration > 120*time.Second {
		return cfg, errors.New("duration must be from 5s to 120s")
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return cfg, errors.New("project folder unavailable")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return cfg, errors.New("project folder unavailable")
	}
	cfg.Root = root
	return cfg, nil
}

type sampleReply struct {
	Value       *snapshot `json:"value"`
	Unavailable string    `json:"unavailable_reason,omitempty"`
}

func observe(ctx context.Context, executable string, cfg options) sample {
	start := time.Now()
	bounded, cancel := context.WithTimeout(ctx, 1800*time.Millisecond)
	defer cancel()
	data, err := childOutput(bounded, executable, "--root", cfg.Root, "--source", cfg.Source, "--sample")
	end := time.Now()
	midpoint := start.Add(end.Sub(start) / 2)
	result := sample{At: midpoint.UTC(), monotonic: midpoint, ReadSeconds: end.Sub(start).Seconds()}
	if err != nil {
		result.Unavailable = "status or process read failed or timed out"
		return result
	}
	var reply sampleReply
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&reply) != nil || decoder.Decode(new(any)) != io.EOF || (reply.Value == nil && reply.Unavailable == "") {
		result.Unavailable = "sample response unavailable"
		return result
	}
	result.Value, result.Unavailable = reply.Value, reply.Unavailable
	return result
}

type report struct {
	Version          int           `json:"version"`
	Source           string        `json:"source"`
	StartedAt        time.Time     `json:"started_at"`
	RequestedSeconds float64       `json:"requested_seconds"`
	Result           string        `json:"result"`
	Unavailable      string        `json:"unavailable_reason,omitempty"`
	Measurements     *measurements `json:"measurements"`
	Samples          []sample      `json:"samples"`
	Notes            []string      `json:"notes"`
}

func measure(ctx context.Context, cfg options, read func(context.Context) sample) report {
	result := report{Version: 1, Source: cfg.Source, StartedAt: time.Now().UTC(), RequestedSeconds: cfg.Duration.Seconds(), Result: "inconclusive", Notes: []string{
		"CPU is cumulative processor time for this forwarding process only. One fully used processor core is 100%; more than 100% is possible.",
		"The bitrate is payload bytes received by the forwarded MediaMTX path, not total network traffic, playable frames or picture quality.",
		"CPU and payload counters are sampled within each bounded read. Midpoints use a monotonic clock; read_seconds shows the boundary timing uncertainty.",
		"Memory is the arithmetic mean and maximum of sampled resident memory in KiB, not every allocation or the whole Mac.",
		"Any unavailable observation, stale status, counter reset, identity change or sampling gap makes the whole window inconclusive. Short interruptions between samples can go undetected.",
		"This does not measure camera-to-screen delay, freezes, video quality or long-term reliability.",
	}}
	bounded, cancel := context.WithTimeout(ctx, cfg.Duration+5*time.Second)
	defer cancel()
	start := time.Now()
	for offset := time.Duration(0); ; {
		if delay := time.Until(start.Add(offset)); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-bounded.Done():
				timer.Stop()
				result.Unavailable = "observation window was interrupted"
				return result
			}
		}
		item := read(bounded)
		if len(result.Samples) > 0 {
			item.ElapsedSeconds = item.monotonic.Sub(result.Samples[0].monotonic).Seconds()
		}
		result.Samples = append(result.Samples, item)
		if bounded.Err() != nil {
			result.Unavailable = "observation window was interrupted"
			return result
		}
		if offset == cfg.Duration {
			break
		}
		offset += time.Second
		if offset > cfg.Duration {
			offset = cfg.Duration
		}
	}
	result.Measurements, result.Unavailable = summarize(result.Samples)
	if result.Measurements != nil {
		result.Result = "complete"
	}
	return result
}

func saveReport(root string, result report) (string, error) {
	reports := lab.NewPaths(root).Reports
	if err := os.MkdirAll(reports, 0700); err != nil {
		return "", errors.New("private reports folder unavailable")
	}
	directory, err := os.MkdirTemp(reports, "relay-check-")
	if err != nil {
		return "", errors.New("private report folder could not be created")
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", errors.New("report could not be encoded")
	}
	path := filepath.Join(directory, "result.json")
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		return "", errors.New("private report could not be saved")
	}
	return path, nil
}

func run(ctx context.Context, args []string, output io.Writer) error {
	cfg, err := parseOptions(args, output)
	if err != nil {
		return err
	}
	if cfg.Sample {
		bounded, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		defer cancel()
		value, err := makeSnapshot(bounded, cfg.Root, cfg.Source)
		reply := sampleReply{}
		if err != nil {
			reply.Unavailable = err.Error()
		} else {
			reply.Value = &value
		}
		return json.NewEncoder(output).Encode(reply)
	}
	executable, err := os.Executable()
	if err != nil {
		return errors.New("diagnostic executable unavailable")
	}
	fmt.Fprintf(output, "Observing %s forwarding for %s. Camera and upload settings remain in use.\n", cfg.Source, cfg.Duration)
	result := measure(ctx, cfg, func(ctx context.Context) sample { return observe(ctx, executable, cfg) })
	path, err := saveReport(cfg.Root, result)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Private measurement: %s\n", path)
	if result.Measurements == nil {
		return fmt.Errorf("measurement inconclusive: %s", result.Unavailable)
	}
	m := result.Measurements
	fmt.Fprintf(output, "Forwarding process: %.1f%% CPU (one core = 100%%), %.1f MiB average sampled memory, %.1f MiB maximum.\n", m.RelayCPUPercent, m.RelayRSSMeanKiB/1024, float64(m.RelayRSSMaxKiB)/1024)
	fmt.Fprintf(output, "Forwarded payload received: %.0f kbit/s over %.2f seconds. This does not measure picture delay or freezes.\n", m.ForwardedPayloadKbps, m.WindowSeconds)
	return nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "Relay check:", err)
		os.Exit(1)
	}
}
