package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type bandwidthOptions struct {
	Rates      []int    `json:"rates_kbps_each_direction"`
	QueueBytes int      `json:"queue_bytes_each_direction"`
	Encodings  []string `json:"encodings"`
	Repeats    []int    `json:"repeat_labels"`
}

func parseBandwidthOptions(rates string, queueBytes int, encodings, repeats string) (bandwidthOptions, error) {
	o := bandwidthOptions{QueueBytes: queueBytes}
	parse := func(s string, valid func(int) bool) ([]int, error) {
		var values []int
		seen := map[int]bool{}
		for _, part := range strings.Split(s, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || !valid(n) || seen[n] {
				return nil, fmt.Errorf("invalid or repeated value %q", part)
			}
			values = append(values, n)
			seen[n] = true
		}
		return values, nil
	}
	var err error
	o.Rates, err = parse(rates, func(n int) bool { return n == 0 || (n >= 64 && n <= 100000) })
	if err != nil || len(o.Rates) > 6 {
		return o, errors.New("rates must contain up to six distinct values: 0 (unlimited) or 64 through 100000 kilobits/second")
	}
	if queueBytes < 2048 || queueBytes > 1024*1024 {
		return o, errors.New("queue-bytes must be between 2048 and 1048576")
	}
	seen := map[string]bool{}
	for _, part := range strings.Split(encodings, ",") {
		name := strings.TrimSpace(part)
		if (name != "copy" && name != "detail20" && name != "small20") || seen[name] {
			return o, errors.New("encodings must be distinct names from copy,detail20,small20")
		}
		o.Encodings = append(o.Encodings, name)
		seen[name] = true
	}
	o.Repeats, err = parse(repeats, func(n int) bool { return n >= 0 && n <= 1000000 })
	if err != nil || len(o.Repeats) > 3 {
		return o, errors.New("bandwidth comparison accepts up to three distinct repeat labels between 0 and 1000000")
	}
	if len(o.Rates)*len(o.Encodings)*len(o.Repeats) > 36 {
		return o, errors.New("one bandwidth comparison is limited to 36 trials")
	}
	return o, nil
}

type bandwidthEncoding struct {
	Name               string  `json:"name"`
	FPS                int     `json:"fps"`
	Frames             int     `json:"frames"`
	Width              int     `json:"width"`
	Height             int     `json:"height"`
	TargetKbps         int     `json:"video_target_kbps"`
	File               string  `json:"file"`
	SHA256             string  `json:"sha256"`
	Bytes              int64   `json:"bytes"`
	ContainerKbps      float64 `json:"file_bits_per_12_seconds_kbps"`
	PreparationSeconds float64 `json:"preparation_elapsed_seconds"`
	PreparationCPU     float64 `json:"preparation_cpu_seconds"`
	hashes             map[string]int
}

type bandwidthResult struct {
	Encoding string `json:"encoding"`
	Trial    trial  `json:"trial"`
}

type bandwidthReport struct {
	Version         int                    `json:"version"`
	CreatedAt       time.Time              `json:"created_at"`
	CompletedAt     *time.Time             `json:"completed_at"`
	Options         bandwidthOptions       `json:"options"`
	ReceiverVersion string                 `json:"receiver_version"`
	FFmpegVersion   string                 `json:"ffmpeg_version"`
	ReceiverSHA256  string                 `json:"receiver_sha256"`
	HarnessSHA256   string                 `json:"harness_binary_sha256"`
	SourceHashes    map[string]string      `json:"harness_source_sha256"`
	Encodings       []bandwidthEncoding    `json:"encodings"`
	Results         []bandwidthResult      `json:"results"`
	Calibrations    []bandwidthCalibration `json:"calibrations"`
	Notes           []string               `json:"method_and_limits"`
}

func bandwidthEncodeArgs(name, source, output string) ([]string, bandwidthEncoding) {
	e := bandwidthEncoding{Name: name, FPS: 30, Frames: 360, Width: 1280, Height: 720, TargetKbps: 2000, File: filepath.Base(output)}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-n"}
	if name == "copy" {
		args = append(args, "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30", "-frames:v", "360", "-an", "-c:v", "libx264", "-threads:v", "1", "-preset", "ultrafast", "-tune", "zerolatency", "-profile:v", "baseline", "-pix_fmt", "yuv420p", "-bf", "0", "-g", "30", "-keyint_min", "30", "-sc_threshold", "0", "-b:v", "2000k", "-maxrate", "2000k", "-bufsize", "1000k", output)
		return args, e
	}
	e.FPS, e.Frames, e.TargetKbps = 20, 240, 1200
	filter := "fps=20"
	if name == "small20" {
		e.Width, e.Height, e.TargetKbps = 640, 360, 650
		filter = "scale=640:360:flags=bicubic,fps=20"
	}
	rate, buffer := fmt.Sprintf("%dk", e.TargetKbps), fmt.Sprintf("%dk", e.TargetKbps/2)
	args = append(args, "-threads:v", "2", "-filter_threads", "1", "-i", source, "-map", "0:v:0", "-an", "-vf", filter, "-frames:v", "240", "-c:v", "libx264", "-threads:v", "2", "-preset", "veryfast", "-tune", "zerolatency", "-profile:v", "baseline", "-pix_fmt", "yuv420p", "-bf", "0", "-g", "20", "-keyint_min", "20", "-sc_threshold", "0", "-b:v", rate, "-maxrate", rate, "-bufsize", buffer, output)
	return args, e
}

func prepareBandwidthEncoding(ctx context.Context, ffmpeg, dir, name, source string) (bandwidthEncoding, error) {
	output := filepath.Join(dir, name+".mp4")
	args, evidence := bandwidthEncodeArgs(name, source, output)
	jobCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	started := time.Now()
	c, err := startChild(jobCtx, ffmpeg, args, filepath.Join(dir, name+"-encode.log"), "", nil, started)
	if err != nil {
		return evidence, err
	}
	err = <-c.done
	<-c.outputDone
	if err != nil {
		return evidence, fmt.Errorf("%s preparation failed; see private encode log", name)
	}
	evidence.PreparationSeconds = time.Since(started).Seconds()
	evidence.PreparationCPU = (c.cmd.ProcessState.UserTime() + c.cmd.ProcessState.SystemTime()).Seconds()
	info, err := os.Stat(output)
	if err != nil || info.Size() == 0 || info.Size() > 32*1024*1024 {
		return evidence, fmt.Errorf("%s preparation produced an unavailable or oversized file", name)
	}
	evidence.Bytes, evidence.SHA256 = info.Size(), fileSHA256(output)
	evidence.ContainerKbps = float64(info.Size()) * 8 / 12 / 1000
	if evidence.SHA256 == "" {
		return evidence, errors.New("could not identify generated file")
	}
	var cap capture
	args = []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-xerror", "-err_detect", "explode", "-threads:v", "1", "-i", output, "-map", "0:v:0", "-an", "-c:v", "rawvideo", "-pix_fmt", "yuv420p", "-threads:v", "1", "-fps_mode:v", "passthrough", "-f", "framemd5", "-flush_packets", "1", "pipe:1"}
	c, err = startChild(jobCtx, ffmpeg, args, filepath.Join(dir, name+"-decode.log"), "", &cap, time.Now())
	if err != nil {
		return evidence, err
	}
	err = <-c.done
	<-c.outputDone
	if err != nil {
		return evidence, fmt.Errorf("%s offline reference decode failed", name)
	}
	ref := snapshot(&cap)
	if len(ref.Frames) != evidence.Frames {
		return evidence, fmt.Errorf("%s: got %d frames, expected %d", name, len(ref.Frames), evidence.Frames)
	}
	evidence.hashes = map[string]int{}
	for i, f := range ref.Frames {
		if _, exists := evidence.hashes[f.Hash]; exists {
			return evidence, fmt.Errorf("%s contains repeated frame hashes; identities are ambiguous", name)
		}
		if ref.Timebase <= 0 || absFloat(float64(f.PTS-ref.Frames[0].PTS)*ref.Timebase-float64(i)/float64(evidence.FPS)) > .00001 {
			return evidence, fmt.Errorf("%s reference frame timing differs from declared frame rate", name)
		}
		evidence.hashes[f.Hash] = i
	}
	if err = writeJSON(filepath.Join(dir, name+"-offline-reference.json"), ref); err != nil {
		return evidence, err
	}
	return evidence, nil
}

func absFloat(n float64) float64 {
	if n < 0 {
		return -n
	}
	return n
}

func runBandwidth(parent context.Context, root, ffmpeg, mtx, version string, options bandwidthOptions) error {
	ctx, cancel := context.WithTimeout(parent, 18*time.Minute)
	defer cancel()
	var disk syscall.Statfs_t
	if err := syscall.Statfs(root, &disk); err != nil || uint64(disk.Bavail)*uint64(disk.Bsize) < 2*1024*1024*1024 {
		return errors.New("bandwidth comparison requires at least 2 GiB free space")
	}
	base := filepath.Join(root, ".local", "diagnostics")
	if err := os.MkdirAll(base, 0700); err != nil {
		return err
	}
	// Advisory process lock releases on exit, including interruption or a crash.
	lock, err := os.OpenFile(filepath.Join(base, "bandwidth.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another bandwidth comparison is running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	dir, err := os.MkdirTemp(base, "bandwidth-")
	if err != nil {
		return err
	}
	fmt.Println("Evidence:", dir)
	versionCtx, versionCancel := context.WithTimeout(ctx, 3*time.Second)
	ffVersion, err := exec.CommandContext(versionCtx, ffmpeg, "-version").Output()
	versionCancel()
	if err != nil {
		return errors.New("FFmpeg unavailable")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	report := bandwidthReport{Version: 1, CreatedAt: time.Now().UTC(), Options: options, ReceiverVersion: version,
		FFmpegVersion: strings.SplitN(string(ffVersion), "\n", 2)[0], ReceiverSHA256: fileSHA256(mtx), HarnessSHA256: fileSHA256(executable), SourceHashes: map[string]string{},
		Notes: []string{
			"Generated 12-second 1280x720/30fps H.264 source at a 2000 kbps target; detail20 and small20 re-encode that same saved source at 20fps, 1200 and 650 kbps targets. No physical camera or live encoder is tested.",
			"Each encoded candidate is decoded offline to establish its own unique pixel hashes. That identical encoded candidate is then sent at one second of video per real second to a clean path and a limited path. Normal compression changes are not classified as network damage.",
			"Both SRT connections request and must negotiate 120 ms. One isolated receiver and independent identical decoders share a monotonic clock. Added delay is same-frame decoded arrival difference against the simultaneous clean reference, not camera-to-screen age.",
			"The fixed source interval [2,11) seconds is scored: 270 expected frames for copy, 180 for each 20fps candidate. Missing, changed and unpaired frames remain in deadline denominators. Compare fractions across encodings, not their different frame totals.",
			"A connection limit counts complete UDP payload bytes, including video, SRT headers, setup, control and retransmission packets, separately in each direction. It excludes IP, UDP and physical-network headers. Queue-full drops are intentional network congestion; harness resource drops invalidate timing.",
			"There is no injected propagation delay, random loss or outage in this experiment. Seed values label repeated runs; scheduling and congestion can differ between repeats.",
			"Queue residence and decoder gaps are measured. Gaps between correct decoded pictures and missing source-frame durations are not browser freezes. No browser, radio link, changing capacity, concurrent traffic or long-term reliability is measured.",
			"Preparation elapsed and CPU times describe generating saved files before transmission. They are not live forwarding CPU or encoding delay. File bitrate includes MP4 packaging and is not measured network throughput.",
			"Integrity is relative to each compressed candidate, not to an uncompressed scene. These delivery tests do not establish text readability or motion quality; reducing 30 fps to 20 fps removes captured moments.",
			"The normal controller, camera settings, recording catalog and R2 queue are not read or changed. Only private generated files and loopback test services are used; the test still shares this computer's CPU and disk.",
		}}
	for _, file := range []string{"main.go", "scoring.go", "bandwidth_compare.go", "bandwidth_queue.go", "bandwidth_calibration.go"} {
		report.SourceHashes[file] = fileSHA256(filepath.Join(root, "cmd", "net_check", file))
	}
	save := func() error { return writeJSON(filepath.Join(dir, "results.json"), report) }
	if err = save(); err != nil {
		return err
	}
	for _, rate := range options.Rates {
		if rate == 0 {
			continue
		}
		calibration, calibrationErr := calibrateBandwidth(ctx, rate, options.QueueBytes)
		report.Calibrations = append(report.Calibrations, calibration)
		if err = save(); err != nil {
			return err
		}
		if calibrationErr != nil {
			return fmt.Errorf("%d kbps calibration failed: %w", rate, calibrationErr)
		}
		fmt.Printf("Limit %d kbps: calibration %.0f kbps (%.1f%% of nominal), valid=%v\n", rate, calibration.MeasuredKbps, calibration.Ratio*100, calibration.Valid)
		if !calibration.Valid {
			return fmt.Errorf("%d kbps limiter calibration inconclusive: %s", rate, strings.Join(calibration.Flags, "; "))
		}
	}
	source, err := prepareBandwidthEncoding(ctx, ffmpeg, dir, "copy", "")
	if err != nil {
		return err
	}
	// Save the shared source evidence even when only a compressed variant is selected.
	report.Encodings = append(report.Encodings, source)
	if err = save(); err != nil {
		return err
	}
	for _, name := range options.Encodings {
		if name == "copy" {
			continue
		}
		e, err := prepareBandwidthEncoding(ctx, ffmpeg, dir, name, filepath.Join(dir, source.File))
		if err != nil {
			return err
		}
		report.Encodings = append(report.Encodings, e)
		if err = save(); err != nil {
			return err
		}
	}
	var inconclusive int
	for repeatIndex, repeat := range options.Repeats {
		// Rotate encoding order between repeats to reduce a fixed first/last bias.
		for offset := range options.Encodings {
			name := options.Encodings[(offset+repeatIndex)%len(options.Encodings)]
			var encoding bandwidthEncoding
			for _, e := range report.Encodings {
				if e.Name == name {
					encoding = e
				}
			}
			for _, rate := range options.Rates {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				p := profile{Name: name + "-unlimited"}
				if rate != 0 {
					p.Name = fmt.Sprintf("%s-%dkbps", name, rate)
					p.BandwidthKbps, p.QueueBytes = rate, options.QueueBytes
				}
				w := scoringWindow{FPS: encoding.FPS, First: 2 * encoding.FPS, Last: 11 * encoding.FPS}
				r := runTrial(ctx, dir, ffmpeg, mtx, filepath.Join(dir, encoding.File), p, 120, int64(repeat), encoding.hashes, w)
				report.Results = append(report.Results, bandwidthResult{Encoding: name, Trial: r})
				if !r.TimingConclusive {
					inconclusive++
				}
				if err = save(); err != nil {
					return err
				}
				gap, delay := bandwidthTimingText(r)
				fmt.Printf("%s: correct %d/%d; within 250 ms %.1f%%; longest correct-picture gap %s; added delay p95 %s; conclusive=%v\n", r.Name, r.Impaired.Exact, r.Impaired.Expected, 100*r.Timely250Fraction, gap, delay, r.TimingConclusive)
			}
		}
	}
	finished := time.Now().UTC()
	report.CompletedAt = &finished
	if err = save(); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "results.md"), []byte(bandwidthMarkdown(report)), 0600); err != nil {
		return err
	}
	if inconclusive > 0 {
		return fmt.Errorf("%d of %d trials were inconclusive; inspect results.json flags before interpreting timing", inconclusive, len(report.Results))
	}
	fmt.Println("All trials passed measurement validity checks. These checks do not mean every connection delivered useful video.")
	return nil
}

// A conclusive timing measurement can still show an unusable video connection.
func bandwidthUseful(r trial) bool {
	return r.TimingConclusive && r.Error == "" && r.Impaired.Expected > 0 &&
		r.Impaired.Exact == r.Impaired.Expected && r.Impaired.Nonmatching == 0 &&
		r.Impaired.DuplicateOutputs == 0 && r.Timely250Fraction >= .99 && r.Impaired.IntactArrivalGapsMS.Max <= 150
}

func bandwidthTimingText(r trial) (gap, delay string) {
	gap, delay = "unavailable", "unavailable"
	if r.Impaired.IntactArrivalGapsMS.Count > 0 {
		gap = fmt.Sprintf("%.1f ms", r.Impaired.IntactArrivalGapsMS.Max)
	}
	if r.AddedDelayMS.Count > 0 && r.TimingConclusive {
		delay = fmt.Sprintf("%.1f ms", r.AddedDelayMS.P95)
	}
	return
}

func bandwidthMarkdown(report bandwidthReport) string {
	var b strings.Builder
	b.WriteString("# Video under a bandwidth limit\n\nGenerated videos were prepared before transmission, then sent at normal playback speed. This tests delivery of each already-compressed version. It does not measure live encoding, browser freezes or camera-to-screen delay.\n\n")
	if len(report.Calibrations) > 0 {
		b.WriteString("## Check the requested connection limits\n\n| Nominal payload cap | Measured saturated payload rate | Measurement valid |\n| --- | --- | --- |\n")
		for _, c := range report.Calibrations {
			fmt.Fprintf(&b, "| %d kbps | %.0f kbps (%.1f%%) | %v |\n", c.RateKbps, c.MeasuredKbps, 100*c.Ratio, c.Valid)
		}
		b.WriteString("\nThese rates count UDP payloads, including SRT control and repair traffic. IP, UDP and physical-network headers are excluded. Timers can make the effective limit lower than requested.\n\n")
	}
	b.WriteString("## Delivery results\n\nAdded delay is against the identical encoded frame on the simultaneous clean reference. It is not total picture age. Missing or changed pictures remain in the percentage denominator.\n\n| Encoding | Payload cap | Repeat | Correct pictures | Within 250 ms of reference | Longest gap between correct pictures | Added delay p95 | Outcome |\n| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, result := range report.Results {
		r := result.Trial
		cap := "Unlimited"
		if r.Profile.BandwidthKbps != 0 {
			cap = fmt.Sprintf("%d kbps", r.Profile.BandwidthKbps)
		}
		outcome := "Did not meet rule"
		if !r.TimingConclusive {
			outcome = "Inconclusive"
		} else if bandwidthUseful(r) {
			outcome = "Met rule in this trial"
		}
		gap, delay := bandwidthTimingText(r)
		fmt.Fprintf(&b, "| %s | %s | %d | %d/%d | %.1f%% | %s | %s | %s |\n", result.Encoding, cap, r.Seed, r.Impaired.Exact, r.Impaired.Expected, 100*r.Timely250Fraction, gap, delay, outcome)
	}
	b.WriteString("\nThe rule was set before comparison: a valid measurement, every scored picture intact, no duplicate outputs, at least 99% within 250 ms of the reference, and no more than 150 ms between correct pictures. These are short test criteria, not guaranteed field performance.\n\n")
	b.WriteString("`copy` keeps the generated 720p/30 fps source; `detail20` preserves 720p at 20 fps and targets 1200 kbps; `small20` uses 360p/20 fps and targets 650 kbps. The 20 fps versions have 180 expected scored pictures; copy has 270. Compare fractions rather than raw totals.\n\n")
	b.WriteString("## Measurement limits and evidence\n\n")
	for _, note := range report.Notes {
		fmt.Fprintf(&b, "- %s\n", note)
	}
	b.WriteString("\n[Machine-readable results](results.json) include all trials, validity flags, calibrated rates, nominal settings, measured payload rates, queue drops and occupancy, version information, source checksums and preparation costs. Each trial has private frame-arrival evidence and redacted process logs.\n")
	return b.String()
}
