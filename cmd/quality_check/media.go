package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	comparisonFPS        = 20
	maximumInputBytes    = 64 << 20
	maximumCapturedBytes = 2 << 20
	// The same timestamp normalization and fps rounding precede encoding and
	// reference selection. The reference keeps every selected original pixel.
	comparisonSelection = "setpts=PTS-STARTPTS,fps=fps=20:round=near:eof_action=round"
	smallGeometry       = "scale=640:360:force_original_aspect_ratio=decrease:force_divisible_by=2,pad=640:360:(ow-iw)/2:(oh-ih)/2"
)

type inputMedia struct {
	File                string  `json:"file"`
	Codec               string  `json:"codec"`
	PixelFormat         string  `json:"pixel_format"`
	SHA256              string  `json:"sha256"`
	Width               int     `json:"width"`
	Height              int     `json:"height"`
	Frames              int     `json:"frames"`
	FPS                 float64 `json:"fps"`
	Duration            float64 `json:"duration"`
	Bytes               int64   `json:"bytes"`
	StartTime           float64 `json:"start_time"`
	SampleAspectRatio   string  `json:"sample_aspect_ratio"`
	SquarePixelsAssumed bool    `json:"square_pixels_assumed"`
}

type Variant struct {
	Name           string  `json:"name"`
	File           string  `json:"file"`
	SHA256         string  `json:"sha256"`
	Width          int     `json:"width"`
	Height         int     `json:"height"`
	FPS            float64 `json:"fps"`
	Frames         int     `json:"frames"`
	Bytes          int64   `json:"bytes"`
	EncodeSeconds  float64 `json:"encode_seconds"`
	CPUSeconds     float64 `json:"cpu_seconds"`
	SSIM           float64 `json:"ssim"`
	ComparedFrames int     `json:"compared_frames"`
}

type comparisonReport struct {
	CreatedAt      time.Time  `json:"created_at"`
	FFmpegVersion  string     `json:"ffmpeg_version"`
	Input          inputMedia `json:"input"`
	Playback       inputMedia `json:"playback"`
	Variants       []Variant  `json:"variants"`
	ComparisonNote string     `json:"comparison_note"`
	TestScene      *testScene `json:"test_scene,omitempty"`
}

type decodedMedia struct {
	inputMedia
	timestamps []float64
}

// runBenchmark reads the caller's private original.mp4 and writes only named
// comparison files in its directory. It never reads controller settings or
// connects to a stream. Its results describe this saved clip, not live latency.
func runBenchmark(parent context.Context, input, dir, ffmpeg, ffprobe string) (report comparisonReport, runError error) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	var err error
	dir, err = filepath.Abs(dir)
	if err != nil {
		return report, err
	}
	input, err = filepath.Abs(input)
	if err != nil || input != filepath.Join(dir, "original.mp4") {
		return report, errors.New("comparison input must be the private original.mp4 in the output directory")
	}
	before, size, err := hashRegularMedia(input, maximumInputBytes)
	if err != nil {
		return report, err
	}
	defer func() {
		after, afterSize, err := hashRegularMedia(input, maximumInputBytes)
		if err != nil || after != before || afterSize != size {
			runError = errors.Join(runError, errors.New("the original recording changed during the comparison"))
		}
	}()
	version, err := mediaCommand(ctx, dir, ffmpeg, "-version")
	if err != nil {
		return report, errors.New("cannot read FFmpeg version")
	}
	report.CreatedAt = time.Now().UTC()
	report.FFmpegVersion = strings.SplitN(strings.TrimSpace(string(version.stdout)), "\n", 2)[0]
	source, err := inspectDecodedMedia(ctx, dir, input, ffprobe)
	if err != nil {
		return report, fmt.Errorf("original recording: %w", err)
	}
	if source.Duration < 1 || source.Duration > 15 || source.Frames > 900 || source.FPS > 60.1 {
		return report, errors.New("original recording must be 1–15 seconds, with at most 900 frames and 60 fps")
	}
	if err := verifyDecoding(ctx, dir, input, ffmpeg); err != nil {
		return report, fmt.Errorf("original recording: %w", err)
	}
	report.Input = source.inputMedia
	report.Input.File, report.Input.SHA256, report.Input.Bytes = "original.mp4", before, size
	report.ComparisonNote = "SSIM compares the two 20 fps outputs at the original pixel dimensions, with bicubic enlargement when needed. The reference uses the same frame-selection filter. This score excludes motion samples removed by reducing frame rate. Conversion timing includes decoding, filtering, encoding and MP4 writing."
	if source.SquarePixelsAssumed {
		report.ComparisonNote += " The source does not declare its pixel aspect ratio; square pixels are assumed for this 16:9 lab recording and set explicitly in both outputs."
	}
	report.Playback, err = createPlayback(ctx, dir, input, ffmpeg, ffprobe, source)
	if err != nil {
		return report, fmt.Errorf("original playback copy: %w", err)
	}
	report.ComparisonNote += " The original panel uses a timestamp-normalized MP4 copy with no video re-encoding; every decoded frame was checked against the unchanged original.mp4."
	reference, err := referenceFrames(ctx, dir, input, ffmpeg)
	if err != nil {
		return report, err
	}
	for _, spec := range []struct {
		name, filter string
		bitrate      int
	}{
		{"detail-20", comparisonSelection, 1200},
		// Geometry precedes fps, matching the existing live small profile;
		// the normalization and fps options are identical to the reference.
		{"small-20", "setpts=PTS-STARTPTS," + smallGeometry + ",fps=fps=20:round=near:eof_action=round", 650},
	} {
		file := spec.name + ".mp4"
		target := filepath.Join(dir, file)
		args := append(mediaReadArgs(input), "-map", "0:v:0", "-an", "-sn", "-dn", "-vf", spec.filter+",setsar=1",
			"-c:v", "libx264", "-threads:v", "2", "-preset", "veryfast", "-tune", "zerolatency",
			"-profile:v", "baseline", "-pix_fmt", "yuv420p", "-bf", "0", "-g", "20",
			"-b:v", strconv.Itoa(spec.bitrate)+"k", "-maxrate", strconv.Itoa(spec.bitrate)+"k",
			"-bufsize", strconv.Itoa(spec.bitrate/2)+"k", "-fps_mode:v", "passthrough", "-movflags", "+faststart", "-f", "mp4", target)
		encoded, err := mediaCommand(ctx, dir, ffmpeg, args...)
		if err != nil || len(bytes.TrimSpace(encoded.stderr)) != 0 {
			return report, fmt.Errorf("%s encoding failed", spec.name)
		}
		if err := os.Chmod(target, 0600); err != nil {
			return report, err
		}
		candidate, err := inspectDecodedMedia(ctx, dir, target, ffprobe)
		if err != nil {
			return report, fmt.Errorf("%s: %w", spec.name, err)
		}
		width, height := source.Width, source.Height
		if spec.name == "small-20" {
			width, height = 640, 360
		}
		if candidate.Width != width || candidate.Height != height {
			return report, fmt.Errorf("%s output dimensions do not match the selected profile", spec.name)
		}
		score, compared, err := compareCandidate(ctx, dir, input, target, ffmpeg, ffprobe, source, reference, spec.name+".ssim.log")
		if err != nil {
			return report, fmt.Errorf("%s comparison: %w", spec.name, err)
		}
		digest, finalSize, err := hashRegularMedia(target, maximumInputBytes)
		if err != nil || finalSize != candidate.Bytes {
			return report, fmt.Errorf("%s changed while its comparison was running", spec.name)
		}
		report.Variants = append(report.Variants, Variant{Name: spec.name, File: file, SHA256: digest, Width: width, Height: height,
			FPS: candidate.FPS, Frames: candidate.Frames, Bytes: candidate.Bytes,
			EncodeSeconds: encoded.wall.Seconds(), CPUSeconds: encoded.cpu.Seconds(), SSIM: score, ComparedFrames: compared})
	}
	return report, nil
}

// Stream-copy the encoded packets, shifting by the first decoded presentation
// timestamp rather than trusting container start-time metadata. Negative decode
// timestamps (possible with B frames) must not shift the presentation start back
// above zero, so the muxer's automatic negative-timestamp adjustment is disabled.
func createPlayback(ctx context.Context, dir, original, ffmpeg, ffprobe string, source decodedMedia) (inputMedia, error) {
	file := filepath.Join(dir, "playback.mp4")
	args := append([]string{"-copyts", "-itsoffset", strconv.FormatFloat(-source.StartTime, 'f', 6, 64)}, mediaReadArgs(original)...)
	args = append(args, "-map", "0:v:0", "-an", "-sn", "-dn", "-c:v", "copy", "-copytb", "1", "-fps_mode:v", "passthrough",
		"-avoid_negative_ts", "disabled", "-movflags", "+faststart", "-f", "mp4", file)
	result, err := mediaCommand(ctx, dir, ffmpeg, args...)
	if err != nil || len(bytes.TrimSpace(result.stderr)) != 0 {
		return inputMedia{}, errors.New("could not normalize the playback timestamps without re-encoding")
	}
	if err := os.Chmod(file, 0600); err != nil {
		return inputMedia{}, err
	}
	return validatePlayback(ctx, dir, original, file, ffmpeg, ffprobe, source)
}

func validatePlayback(ctx context.Context, dir, original, file, ffmpeg, ffprobe string, source decodedMedia) (inputMedia, error) {
	playback, err := inspectDecodedMedia(ctx, dir, file, ffprobe)
	if err != nil {
		return inputMedia{}, err
	}
	if playback.Frames != source.Frames || playback.Width != source.Width || playback.Height != source.Height || playback.PixelFormat != source.PixelFormat ||
		math.Abs(playback.StartTime) > 0.000002 || math.Abs(playback.Duration-source.Duration) > 0.002 {
		return inputMedia{}, errors.New("playback copy changed video geometry, frame count or duration, or did not start at zero")
	}
	for i, timestamp := range playback.timestamps {
		if math.Abs(timestamp-(source.timestamps[i]-source.StartTime)) > 0.000002 {
			return inputMedia{}, errors.New("playback copy changed the relative presentation time of a frame")
		}
	}
	before, err := decodedPictureEvidence(ctx, dir, original, ffmpeg)
	if err != nil {
		return inputMedia{}, err
	}
	after, err := decodedPictureEvidence(ctx, dir, file, ffmpeg)
	if err != nil {
		return inputMedia{}, err
	}
	if len(before) != source.Frames || len(after) != source.Frames {
		return inputMedia{}, errors.New("playback identity check did not decode every original frame")
	}
	for i := range before {
		if before[i].sha256 != after[i].sha256 || before[i].bytes != after[i].bytes || math.Abs(before[i].timestamp-after[i].timestamp) > 0.000002 ||
			math.Abs(before[i].timestamp-(source.timestamps[i]-source.StartTime)) > 0.000002 {
			return inputMedia{}, errors.New("playback copy did not preserve every original decoded picture and timestamp")
		}
	}
	checksum, size, err := hashRegularMedia(file, maximumInputBytes)
	if err != nil || size != playback.Bytes {
		return inputMedia{}, errors.New("playback copy changed during verification")
	}
	playback.SHA256 = checksum
	return playback.inputMedia, nil
}

type pictureEvidence struct {
	timestamp float64
	bytes     int64
	sha256    string
}

// Decode without an fps filter or output frame duplication. Use the filter's
// original time base so variable presentation intervals are not quantized to a
// nominal frame rate before the checksum evidence is checked.
func decodedPictureEvidence(ctx context.Context, dir, input, ffmpeg string) ([]pictureEvidence, error) {
	args := append(mediaReadArgs(input), "-map", "0:v:0", "-an", "-sn", "-dn", "-vf", "setpts=PTS-STARTPTS,format=yuv420p",
		"-c:v", "rawvideo", "-threads:v", "1", "-enc_time_base:v", "filter", "-fps_mode:v", "passthrough", "-f", "framehash", "-hash", "sha256", "pipe:1")
	result, err := mediaCommand(ctx, dir, ffmpeg, args...)
	if err != nil || len(bytes.TrimSpace(result.stderr)) != 0 {
		return nil, errors.New("could not decode complete playback identity evidence")
	}
	scanner := bufio.NewScanner(bytes.NewReader(result.stdout))
	var frames []pictureEvidence
	timebase := 0.0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#tb 0:") {
			parts := strings.Split(strings.TrimSpace(strings.TrimPrefix(line, "#tb 0:")), "/")
			if len(parts) != 2 {
				return nil, errors.New("playback identity time base is invalid")
			}
			n, nerr := strconv.ParseFloat(parts[0], 64)
			d, derr := strconv.ParseFloat(parts[1], 64)
			if nerr != nil || derr != nil || n <= 0 || d <= 0 || !finite(n/d) {
				return nil, errors.New("playback identity time base is invalid")
			}
			timebase = n / d
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) != 6 || strings.TrimSpace(fields[0]) != "0" || timebase == 0 || len(frames) >= 900 {
			return nil, errors.New("playback identity frame list is invalid")
		}
		pts, perr := strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64)
		size, serr := strconv.ParseInt(strings.TrimSpace(fields[4]), 10, 64)
		digest := strings.TrimSpace(fields[5])
		decoded, herr := hex.DecodeString(digest)
		timestamp := float64(pts) * timebase
		if perr != nil || serr != nil || herr != nil || len(decoded) != sha256.Size || size <= 0 || !finite(timestamp) ||
			(len(frames) == 0 && math.Abs(timestamp) > 0.000002) || (len(frames) > 0 && timestamp <= frames[len(frames)-1].timestamp) {
			return nil, errors.New("playback identity frame timing or checksum is invalid")
		}
		frames = append(frames, pictureEvidence{timestamp: timestamp, bytes: size, sha256: digest})
	}
	if scanner.Err() != nil || len(frames) == 0 {
		return nil, errors.New("playback identity evidence is incomplete")
	}
	return frames, nil
}

func hashRegularMedia(file string, limit int64) (string, int64, error) {
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		return "", 0, errors.New("media must be a regular, nonempty file no larger than 64 MiB")
	}
	f, err := os.Open(file)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, limit+1))
	if err != nil || n != info.Size() {
		return "", 0, errors.New("media changed while reading its checksum")
	}
	return hex.EncodeToString(hash.Sum(nil)), n, nil
}

// All decoders are restricted to local files and bounded threads. -n prevents
// an existing output, especially original.mp4, from being overwritten.
func mediaReadArgs(input string) []string {
	return []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-n", "-xerror", "-max_alloc", "67108864",
		"-filter_threads", "1", "-filter_complex_threads", "1", "-protocol_whitelist", "file,pipe", "-format_whitelist", "mov", "-err_detect", "explode", "-threads:v", "2", "-i", input}
}

func verifyDecoding(ctx context.Context, dir, input, ffmpeg string) error {
	args := append(mediaReadArgs(input), "-map", "0:v:0", "-an", "-sn", "-dn", "-fps_mode:v", "passthrough", "-f", "null", "-")
	result, err := mediaCommand(ctx, dir, ffmpeg, args...)
	if err != nil || len(bytes.TrimSpace(result.stderr)) != 0 {
		return errors.New("the complete video could not be decoded without errors")
	}
	return nil
}

func inspectDecodedMedia(ctx context.Context, dir, input, ffprobe string) (decodedMedia, error) {
	var media decodedMedia
	info, err := os.Lstat(input)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumInputBytes {
		return media, errors.New("media must be a regular file of at most 64 MiB")
	}
	result, err := mediaCommand(ctx, dir, ffprobe, "-v", "error", "-max_alloc", "67108864", "-protocol_whitelist", "file,pipe", "-format_whitelist", "mov", "-err_detect", "explode", "-threads", "2",
		"-show_frames", "-show_entries", "stream=codec_type,codec_name,width,height,pix_fmt,sample_aspect_ratio,field_order,duration:frame=media_type,width,height,pix_fmt,best_effort_timestamp_time:format=duration", "-of", "json", input)
	if err != nil || len(bytes.TrimSpace(result.stderr)) != 0 {
		return media, errors.New("video metadata or decoded frames contain errors")
	}
	var probe struct {
		Streams []struct {
			Type     string `json:"codec_type"`
			Codec    string `json:"codec_name"`
			Width    int    `json:"width"`
			Height   int    `json:"height"`
			Pixel    string `json:"pix_fmt"`
			SAR      string `json:"sample_aspect_ratio"`
			Field    string `json:"field_order"`
			Duration string `json:"duration"`
		} `json:"streams"`
		Frames []struct {
			Type      string `json:"media_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			Pixel     string `json:"pix_fmt"`
			Timestamp string `json:"best_effort_timestamp_time"`
		} `json:"frames"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if json.Unmarshal(result.stdout, &probe) != nil || len(probe.Streams) != 1 || len(probe.Frames) == 0 || len(probe.Frames) > 900 {
		return media, errors.New("comparison needs exactly one video-only stream and 1–900 decoded frames")
	}
	stream := probe.Streams[0]
	assumeSquare := stream.SAR == "" || stream.SAR == "N/A" || stream.SAR == "0:1"
	if stream.Type != "video" || stream.Codec != "h264" || stream.Pixel != "yuv420p" || (!assumeSquare && stream.SAR != "1:1") ||
		stream.Width < 640 || stream.Width > 1920 || stream.Height < 360 || stream.Height > 1080 ||
		stream.Width*9 != stream.Height*16 || stream.Width%2 != 0 || stream.Height%2 != 0 || (stream.Field != "progressive" && stream.Field != "unknown" && stream.Field != "") {
		return media, errors.New("comparison needs progressive H.264 yuv420p, square pixels, and 16:9 dimensions from 640×360 to 1920×1080")
	}
	durationText := stream.Duration
	if durationText == "" || durationText == "N/A" {
		durationText = probe.Format.Duration
	}
	duration, err := strconv.ParseFloat(durationText, 64)
	if err != nil || !finite(duration) || duration <= 0 || duration > 15.05 {
		return media, errors.New("video duration is missing or outside the comparison limit")
	}
	for i, frame := range probe.Frames {
		timestamp, err := strconv.ParseFloat(frame.Timestamp, 64)
		if err != nil || !finite(timestamp) || frame.Type != "video" || frame.Width != stream.Width || frame.Height != stream.Height || frame.Pixel != stream.Pixel ||
			(i > 0 && timestamp <= media.timestamps[i-1]) {
			return media, errors.New("decoded frame timing, dimensions or pixel format are inconsistent")
		}
		media.timestamps = append(media.timestamps, timestamp)
	}
	span := media.timestamps[len(media.timestamps)-1] - media.timestamps[0]
	if span >= duration+0.001 {
		return media, errors.New("decoded frames extend beyond the declared video duration")
	}
	media.inputMedia = inputMedia{File: filepath.Base(input), Codec: stream.Codec, PixelFormat: stream.Pixel,
		Width: stream.Width, Height: stream.Height, Frames: len(media.timestamps), Duration: duration, FPS: float64(len(media.timestamps)) / duration, Bytes: info.Size(),
		StartTime: media.timestamps[0], SampleAspectRatio: stream.SAR, SquarePixelsAssumed: assumeSquare}
	return media, nil
}

// The small reference is never materialized as lossless video. A bounded frame
// hash stream gives the exact number/timestamps selected by FFmpeg's fps filter.
func referenceFrames(ctx context.Context, dir, original, ffmpeg string) ([]float64, error) {
	args := append(mediaReadArgs(original), "-map", "0:v:0", "-an", "-vf", comparisonSelection+",format=yuv420p",
		"-c:v", "rawvideo", "-threads:v", "1", "-fps_mode:v", "passthrough", "-f", "framemd5", "pipe:1")
	result, err := mediaCommand(ctx, dir, ffmpeg, args...)
	if err != nil || len(bytes.TrimSpace(result.stderr)) != 0 {
		return nil, errors.New("could not decode the selected reference frames")
	}
	scanner := bufio.NewScanner(bytes.NewReader(result.stdout))
	var timestamps []float64
	timebase := 0.0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#tb 0:") {
			parts := strings.Split(strings.TrimSpace(strings.TrimPrefix(line, "#tb 0:")), "/")
			if len(parts) != 2 {
				return nil, errors.New("reference time base is invalid")
			}
			n, nerr := strconv.ParseFloat(parts[0], 64)
			d, derr := strconv.ParseFloat(parts[1], 64)
			if nerr != nil || derr != nil || n <= 0 || d <= 0 || !finite(n/d) {
				return nil, errors.New("reference time base is invalid")
			}
			timebase = n / d
			continue
		}
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) != 6 || strings.TrimSpace(fields[0]) != "0" || timebase == 0 {
			return nil, errors.New("reference frame list is invalid")
		}
		pts, err := strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64)
		timestamp := float64(pts) * timebase
		if err != nil || math.Abs(timestamp-float64(len(timestamps))/comparisonFPS) > 0.000002 {
			return nil, errors.New("reference frames do not have the expected 20 fps timestamps")
		}
		timestamps = append(timestamps, timestamp)
	}
	if scanner.Err() != nil || len(timestamps) < 1 || len(timestamps) > 301 {
		return nil, errors.New("reference frame count is outside the comparison limit")
	}
	return timestamps, nil
}

func compareCandidate(ctx context.Context, dir, original, candidate, ffmpeg, ffprobe string, source decodedMedia, reference []float64, statsFile string) (float64, int, error) {
	media, err := inspectDecodedMedia(ctx, dir, candidate, ffprobe)
	if err != nil {
		return 0, 0, err
	}
	if len(media.timestamps) != len(reference) || len(reference) == 0 {
		return 0, 0, errors.New("candidate frame count differs from the selected reference; comparison refused")
	}
	for i, timestamp := range media.timestamps {
		if math.Abs(timestamp-reference[i]) > 0.000002 {
			return 0, 0, errors.New("candidate timestamps do not match the selected reference; comparison refused")
		}
	}
	if err := verifyDecoding(ctx, dir, candidate, ffmpeg); err != nil {
		return 0, 0, err
	}
	if filepath.Base(statsFile) != statsFile || strings.ContainsAny(statsFile, "'\\:;[]") {
		return 0, 0, errors.New("invalid private metric filename")
	}
	statsPath := filepath.Join(dir, statsFile)
	stats, err := os.OpenFile(statsPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, 0, errors.New("metric output already exists or cannot be created")
	}
	stats.Close()
	graph := fmt.Sprintf("[0:v:0]scale=%d:%d:flags=bicubic,setsar=1,format=yuv420p,settb=AVTB[candidate];[1:v:0]%s,format=yuv420p,settb=AVTB[reference];[candidate][reference]ssim=stats_file=%s:repeatlast=0:eof_action=pass[out]", source.Width, source.Height, comparisonSelection, statsFile)
	args := append(mediaReadArgs(candidate), "-protocol_whitelist", "file,pipe", "-format_whitelist", "mov", "-err_detect", "explode", "-threads:v", "2", "-i", original,
		"-filter_complex", graph, "-map", "[out]", "-an", "-sn", "-dn", "-fps_mode:v", "passthrough", "-f", "null", "-")
	result, err := mediaCommand(ctx, dir, ffmpeg, args...)
	if err != nil || len(bytes.TrimSpace(result.stderr)) != 0 {
		return 0, 0, errors.New("SSIM comparison could not decode and compare the complete clips")
	}
	file, err := os.Open(statsPath)
	if err != nil {
		return 0, 0, errors.New("SSIM did not produce its per-frame evidence")
	}
	defer file.Close()
	return parseSSIM(io.LimitReader(file, 256<<10), len(reference))
}

func parseSSIM(reader io.Reader, expected int) (float64, int, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024), 8192)
	count, total := 0, 0.0
	for scanner.Scan() {
		n, score := -1, math.NaN()
		for _, field := range strings.Fields(scanner.Text()) {
			if value, found := strings.CutPrefix(field, "n:"); found {
				parsed, err := strconv.Atoi(value)
				if err == nil {
					n = parsed
				}
			}
			if value, found := strings.CutPrefix(field, "All:"); found {
				parsed, err := strconv.ParseFloat(value, 64)
				if err == nil {
					score = parsed
				}
			}
		}
		if n != count+1 || !finite(score) || score < -1 || score > 1 || count >= expected {
			return 0, count, errors.New("SSIM frame evidence is invalid or contains an unexpected frame")
		}
		count++
		total += score
	}
	if scanner.Err() != nil || expected == 0 || count != expected {
		return 0, count, errors.New("SSIM compared fewer frames than the complete selected reference")
	}
	return total / float64(count), count, nil
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

type capturedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *capturedOutput) Write(data []byte) (int, error) {
	wanted := len(data)
	if wanted > maximumCapturedBytes-b.Len() {
		b.overflow = true
		data = data[:maximumCapturedBytes-b.Len()]
	}
	b.Buffer.Write(data)
	// Drain the child even after the capture limit, so a full pipe cannot
	// defeat its deadline. Overflow makes the command fail after it exits.
	return wanted, nil
}

type mediaCommandResult struct {
	stdout, stderr []byte
	wall, cpu      time.Duration
}

func mediaCommand(parent context.Context, dir, executable string, args ...string) (mediaCommandResult, error) {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = dir
	command.WaitDelay = time.Second
	var stdout, stderr capturedOutput
	command.Stdout, command.Stderr = &stdout, &stderr
	start := time.Now()
	err := command.Run()
	result := mediaCommandResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), wall: time.Since(start)}
	if command.ProcessState != nil {
		result.cpu = command.ProcessState.UserTime() + command.ProcessState.SystemTime()
	}
	if stdout.overflow || stderr.overflow {
		err = errors.Join(err, errors.New("media tool output exceeded the comparison capture limit"))
	}
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	return result, err
}
