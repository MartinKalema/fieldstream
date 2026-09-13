package lab

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const videoHealthVersion = 1
const maxHealthRecords = 100_000
const maxHealthReportBytes = 64 << 20

// These are dated observations about the exact cataloged bytes, not a promise
// that footage is complete, visually clear, or still present in R2.
type VideoHealthResult struct {
	Segment
	State         string    `json:"state"`
	Reason        string    `json:"reason,omitempty"`
	Frames        int64     `json:"frames"`
	CheckedAt     time.Time `json:"checked_at"`
	FFmpegVersion string    `json:"ffmpeg_version"`
}

type VideoHealthSummary struct {
	Total       int `json:"total"`
	Unchecked   int `json:"unchecked"`
	Decodable   int `json:"decodable"`
	DecodeError int `json:"decode_error"`
	CheckFailed int `json:"check_failed"`
	Missing     int `json:"missing_local_file"`
}

type VideoHealthReport struct {
	Version   int                 `json:"version"`
	UpdatedAt time.Time           `json:"updated_at"`
	Summary   VideoHealthSummary  `json:"summary"`
	Results   []VideoHealthResult `json:"results"`
}

type VideoHealthOptions struct {
	SourceID string
	Path     string
	Limit    int
	Recheck  bool
}

func (p Paths) VideoHealthPath() string { return filepath.Join(p.Local, "video-health.json") }

func loadVideoHealth(p Paths) (VideoHealthReport, error) {
	report := VideoHealthReport{Version: videoHealthVersion, Results: []VideoHealthResult{}}
	f, err := os.Open(p.VideoHealthPath())
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return report, errors.New("saved video-health report cannot be read")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxHealthReportBytes {
		return report, errors.New("saved video-health report is not a bounded regular file")
	}
	decoder := json.NewDecoder(io.LimitReader(f, maxHealthReportBytes+1))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&report) != nil || report.Version != videoHealthVersion || len(report.Results) > maxHealthRecords {
		return report, errors.New("saved video-health report is invalid or from a different checker version")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return report, errors.New("saved video-health report has extra data")
	}
	seen := map[string]bool{}
	for _, r := range report.Results {
		if validateSegment(r.Segment) != nil || seen[r.Path] || r.CheckedAt.IsZero() || r.Frames < 0 ||
			(r.State != "decodable" && r.State != "decode_error" && r.State != "check_failed") ||
			(r.State == "decodable" && r.Frames == 0) {
			return report, errors.New("saved video-health result is invalid")
		}
		seen[r.Path] = true
	}
	return report, nil
}

func healthInventory(ctx context.Context, p Paths) ([]CatalogSegment, error) {
	// Do not create a new catalog just to report that there is nothing to check.
	if _, err := os.Stat(filepath.Join(p.Local, "recordings.sqlite")); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	catalog, err := OpenCatalog(p)
	if err != nil {
		return nil, errors.New("recording catalog is unavailable for video-health checks")
	}
	defer catalog.Close()
	var all []CatalogSegment
	for offset := 0; ; offset += 1000 {
		page, err := catalog.ListSegments(ctx, 1000, offset)
		if err != nil {
			return nil, errors.New("recording catalog could not be read for video-health checks")
		}
		all = append(all, page...)
		if len(all) > maxHealthRecords {
			return nil, errors.New("video-health inventory exceeds 100000 recordings")
		}
		if len(page) < 1000 {
			return all, nil
		}
	}
}

func healthSnapshot(inventory []CatalogSegment, saved VideoHealthReport, source string) VideoHealthReport {
	indexed := make(map[string]VideoHealthResult, len(saved.Results))
	for _, r := range saved.Results {
		indexed[r.Path] = r
	}
	report := VideoHealthReport{Version: videoHealthVersion, UpdatedAt: saved.UpdatedAt, Results: []VideoHealthResult{}}
	for _, s := range inventory {
		if source != "" && s.SourceID != source {
			continue
		}
		report.Summary.Total++
		if s.Missing {
			report.Summary.Missing++
		}
		r, ok := indexed[s.Path]
		if !ok || r.Segment != s.Segment {
			report.Summary.Unchecked++
			continue
		}
		report.Results = append(report.Results, r)
		switch r.State {
		case "decodable":
			report.Summary.Decodable++
		case "decode_error":
			report.Summary.DecodeError++
		case "check_failed":
			report.Summary.CheckFailed++
		}
	}
	return report
}

// RecordingHealth reads dated results and current catalog counts; it performs
// no media decoding, file hashing, network access or upload-state changes.
func (p Paths) RecordingHealth(ctx context.Context, source string) (VideoHealthReport, error) {
	if source != "" && !catalogSourceIDPattern.MatchString(source) {
		return VideoHealthReport{}, errors.New("invalid video-health source ID")
	}
	saved, err := loadVideoHealth(p)
	if err != nil {
		return saved, err
	}
	inventory, err := healthInventory(ctx, p)
	if err != nil {
		return saved, err
	}
	return healthSnapshot(inventory, saved, source), nil
}

// CheckRecordingHealth checks an explicit, bounded batch. The rebuildable
// report is separate from the durable upload catalog, so a slow decoder never
// holds a catalog transaction or blocks recording/upload completion.
func (p Paths) CheckRecordingHealth(ctx context.Context, ffmpeg string, options VideoHealthOptions) (VideoHealthReport, int, error) {
	if err := ctx.Err(); err != nil {
		return VideoHealthReport{}, 0, err
	}
	if options.Limit < 1 || options.Limit > 1000 {
		return VideoHealthReport{}, 0, errors.New("video-health limit must be between 1 and 1000")
	}
	if options.SourceID != "" && !catalogSourceIDPattern.MatchString(options.SourceID) {
		return VideoHealthReport{}, 0, errors.New("invalid video-health source ID")
	}
	if err := os.MkdirAll(p.Local, 0700); err != nil {
		return VideoHealthReport{}, 0, err
	}
	lock, err := fileLock(filepath.Join(p.Local, "video-health.lock"), true)
	if err != nil {
		return VideoHealthReport{}, 0, errors.New("another video-health check is already running")
	}
	defer lock.Close()
	saved, err := loadVideoHealth(p)
	if err != nil {
		return saved, 0, err
	}
	inventory, err := healthInventory(ctx, p)
	if err != nil {
		return saved, 0, err
	}
	if options.Path != "" {
		found := false
		for _, s := range inventory {
			if s.Path == options.Path && (options.SourceID == "" || s.SourceID == options.SourceID) {
				found = true
				break
			}
		}
		if !found {
			return saved, 0, errors.New("requested recording is not in the selected catalog")
		}
	}
	saved = healthSnapshot(inventory, saved, "")
	positions := make(map[string]int, len(saved.Results))
	for i, r := range saved.Results {
		positions[r.Path] = i
	}
	version, err := videoHealthToolVersion(ctx, ffmpeg)
	if err != nil {
		return healthSnapshot(inventory, saved, options.SourceID), 0, err
	}
	checked := 0
	for _, s := range inventory {
		if checked == options.Limit {
			break
		}
		if options.SourceID != "" && s.SourceID != options.SourceID {
			continue
		}
		if options.Path != "" && s.Path != options.Path {
			continue
		}
		position, known := positions[s.Path]
		if known && !options.Recheck {
			continue
		}
		if err := ctx.Err(); err != nil {
			return healthSnapshot(inventory, saved, options.SourceID), checked, err
		}
		result := checkVideoFile(ctx, p, ffmpeg, version, s.Segment, 30*time.Second)
		if err := ctx.Err(); err != nil {
			// An interrupted check remains eligible for the next invocation.
			return healthSnapshot(inventory, saved, options.SourceID), checked, err
		}
		if known {
			saved.Results[position] = result
		} else {
			positions[s.Path] = len(saved.Results)
			saved.Results = append(saved.Results, result)
		}
		checked++
		saved.UpdatedAt = time.Now().UTC()
		// Persist every finished result. A process crash loses at most the
		// current check; AtomicJSON leaves the previous complete report intact.
		saved = healthSnapshot(inventory, saved, "")
		data, marshalErr := json.MarshalIndent(saved, "", "  ")
		if marshalErr != nil || len(data)+1 > maxHealthReportBytes {
			return healthSnapshot(inventory, saved, options.SourceID), checked, errors.New("video-health report exceeds its size limit")
		}
		if err := privateWrite(p.VideoHealthPath(), append(data, '\n')); err != nil {
			return healthSnapshot(inventory, saved, options.SourceID), checked, errors.New("video-health result could not be saved")
		}
		// healthSnapshot restores catalog ordering; rebuild positions too.
		for i, r := range saved.Results {
			positions[r.Path] = i
		}
	}
	return healthSnapshot(inventory, saved, options.SourceID), checked, nil
}

// Bound captured tool output while still draining its pipes. Raw diagnostics
// may contain media metadata; only fixed reason codes enter the saved report.
type healthOutput struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *healthOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

func videoHealthToolVersion(parent context.Context, ffmpeg string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	var output healthOutput
	output.limit = 16 << 10
	command := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-version")
	command.Stdout, command.Stderr = &output, &output
	command.WaitDelay = time.Second
	if command.Run() != nil || output.overflow {
		return "", errors.New("FFmpeg could not start for video-health checks")
	}
	line := strings.SplitN(output.String(), "\n", 2)[0]
	if !strings.HasPrefix(line, "ffmpeg version ") || len(line) > 256 {
		return "", errors.New("FFmpeg returned an unexpected version response")
	}
	return line, nil
}

func videoHealthArgs() []string {
	return []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-nostats", "-xerror", "-max_error_rate", "0",
		"-abort_on", "empty_output", "-max_alloc", "67108864", "-filter_threads", "1", "-progress", "pipe:1",
		"-protocol_whitelist", "fd", "-fd", "3", "-f", "mov", "-enable_drefs", "0", "-use_absolute_path", "0",
		"-threads", "1", "-max_pixels", "8294400", "-err_detect", "explode", "-c:v", "h264", "-i", "fd:",
		"-map", "0:v:0", "-an", "-sn", "-dn", "-fps_mode", "passthrough", "-threads", "1", "-f", "null", "-"}
}

func checkVideoFile(parent context.Context, p Paths, ffmpeg, version string, s Segment, timeout time.Duration) VideoHealthResult {
	r := VideoHealthResult{Segment: s, State: "check_failed", CheckedAt: time.Now().UTC(), FFmpegVersion: version}
	fail := func(reason string) VideoHealthResult { r.Reason = reason; return r }
	if validateSegment(s) != nil {
		return fail("invalid_recording_identity")
	}
	if s.Bytes > maxUploadSegmentBytes {
		return fail("size_limit_exceeded")
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	root, err := os.OpenRoot(p.Recordings)
	if err != nil {
		return fail("local_file_unavailable")
	}
	defer root.Close()
	// NONBLOCK avoids waiting on a substituted FIFO. The descriptor is passed
	// directly to FFmpeg, which cannot follow an input URL or external track.
	f, err := root.OpenFile(s.Path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fail("local_file_unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fail("local_file_unavailable")
	}
	verify := func() bool {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return false
		}
		hash := sha256.New()
		n, err := io.Copy(hash, io.LimitReader(archiveContextReader{ctx: ctx, reader: f}, s.Bytes+1))
		return err == nil && n == s.Bytes && hex.EncodeToString(hash.Sum(nil)) == s.SHA256
	}
	if info.Size() != s.Bytes || !verify() {
		return fail("local_bytes_changed_or_unreadable")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return fail("local_file_unavailable")
	}
	stdout, stderr := &healthOutput{limit: 64 << 10}, &healthOutput{limit: 32 << 10}
	command := exec.CommandContext(ctx, ffmpeg, videoHealthArgs()...)
	command.ExtraFiles = []*os.File{f}
	command.Stdout, command.Stderr = stdout, stderr
	command.WaitDelay = time.Second
	runErr := command.Run()
	r.CheckedAt = time.Now().UTC()
	if ctx.Err() != nil {
		return fail("check_timed_out_or_canceled")
	}
	if !verify() {
		return fail("local_bytes_changed_or_unreadable")
	}
	after, err := root.Stat(s.Path)
	if err != nil || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return fail("local_file_changed_during_check")
	}
	var exit *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exit) {
		return fail("decoder_could_not_run")
	}
	for _, unavailable := range []string{"Unrecognized option", "Option not found", "Protocol not found", "Unknown decoder", "Error selecting an encoder", "Error loading shared"} {
		if strings.Contains(stderr.String(), unavailable) {
			return fail("decoder_configuration_unsupported")
		}
	}
	if stdout.overflow || stderr.overflow {
		return fail("decoder_output_limit_exceeded")
	}
	ended := false
	for _, line := range strings.Split(stdout.String(), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if key == "frame" {
			frames, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil || frames < 0 {
				return fail("decoder_progress_invalid")
			}
			r.Frames = frames
		}
		if key == "progress" && value == "end" {
			ended = true
		}
	}
	if runErr != nil || stderr.Len() != 0 || r.Frames == 0 {
		r.State, r.Reason = "decode_error", "video_decode_failed"
		return r
	}
	if !ended {
		return fail("decoder_did_not_confirm_completion")
	}
	r.State, r.Reason = "decodable", ""
	return r
}

func VideoHealthDescription(state string) string {
	switch state {
	case "decodable":
		return "decoded without reported errors"
	case "decode_error":
		return "video could not be fully decoded"
	case "check_failed":
		return "check could not finish"
	default:
		return fmt.Sprintf("unknown health state %q", state)
	}
}
