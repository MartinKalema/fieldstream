package main

import (
	"bytes"
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func qualityTools(t *testing.T) (string, string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg is required for the isolated saved-clip tests")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is required for the isolated saved-clip tests")
	}
	return ffmpeg, ffprobe
}

func qualityTestClip(t *testing.T, dir, ffmpeg, size string, sar ...string) string {
	t.Helper()
	file := filepath.Join(dir, "original.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	aspect := "1"
	if len(sar) > 0 {
		aspect = sar[0]
	}
	result, err := mediaCommand(ctx, dir, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-n",
		"-f", "lavfi", "-i", "testsrc2=size="+size+":rate=30", "-frames:v", "30", "-an", "-vf", "setsar="+aspect,
		"-c:v", "libx264", "-threads:v", "2", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-qp", "0", "-movflags", "+faststart", file)
	if err != nil {
		t.Fatalf("could not create isolated clip: %v %s", err, result.stderr)
	}
	return file
}

func qualityLosslessCandidate(t *testing.T, dir, ffmpeg, input, name, filter string, frames string) string {
	t.Helper()
	target := filepath.Join(dir, name+".mp4")
	args := append(mediaReadArgs(input), "-map", "0:v:0", "-an", "-vf", filter, "-frames:v", frames, "-fps_mode:v", "passthrough",
		"-c:v", "libx264", "-threads:v", "2", "-preset", "ultrafast", "-qp", "0", "-pix_fmt", "yuv420p", "-movflags", "+faststart", target)
	result, err := mediaCommand(context.Background(), dir, ffmpeg, args...)
	if err != nil {
		t.Fatalf("could not create isolated comparison: %v %s", err, result.stderr)
	}
	return target
}

func TestBenchmarkPreservesOriginalAndComparesEverySelectedFrame(t *testing.T) {
	ffmpeg, ffprobe := qualityTools(t)
	dir := t.TempDir()
	input := qualityTestClip(t, dir, ffmpeg, "1280x720", "0")
	original, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	report, err := runBenchmark(context.Background(), input, dir, ffmpeg, ffprobe)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(input)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("the compression comparison changed the original recording")
	}
	digest, _, err := hashRegularMedia(input, maximumInputBytes)
	if err != nil || report.Input.SHA256 != digest || report.Input.Frames != 30 || report.Input.FPS != 30 || len(report.Variants) != 2 || report.FFmpegVersion == "" {
		t.Fatalf("invalid original evidence: %+v %v", report, err)
	}
	if !report.Input.SquarePixelsAssumed || !strings.Contains(report.ComparisonNote, "square pixels are assumed") {
		t.Fatal("unspecified pixel aspect ratio was not disclosed as an assumption")
	}
	if report.Playback.File != "playback.mp4" || report.Playback.Frames != report.Input.Frames || report.Playback.StartTime != 0 || report.Playback.Duration != report.Input.Duration || report.Playback.SHA256 == "" {
		t.Fatalf("invalid playback copy evidence: %+v", report.Playback)
	}
	for i, variant := range report.Variants {
		width, height := 1280, 720
		if i == 1 {
			width, height = 640, 360
		}
		if variant.Width != width || variant.Height != height || variant.Frames != 20 || variant.ComparedFrames != 20 || variant.FPS != 20 ||
			variant.Bytes <= 0 || variant.EncodeSeconds <= 0 || variant.CPUSeconds < 0 || !finite(variant.SSIM) || variant.SSIM <= 0 || variant.SSIM > 1 {
			t.Fatalf("invalid variant evidence: %+v", variant)
		}
		hash, size, err := hashRegularMedia(filepath.Join(dir, variant.File), maximumInputBytes)
		if err != nil || hash != variant.SHA256 || size != variant.Bytes {
			t.Fatalf("variant file does not match its evidence: %+v %v", variant, err)
		}
	}
	if report.Variants[0].SSIM <= report.Variants[1].SSIM {
		t.Fatal("the full-resolution test pattern should retain more detail than its downscaled version")
	}
	// Existing outputs must cause an error, rather than overwrite a prior run.
	if _, err := runBenchmark(context.Background(), input, dir, ffmpeg, ffprobe); err == nil {
		t.Fatal("reusing an existing comparison output silently overwrote it")
	}
}

func TestPlaybackNormalizesNonzeroStartWithoutChangingPictures(t *testing.T) {
	ffmpeg, ffprobe := qualityTools(t)
	seedDir, dir := t.TempDir(), t.TempDir()
	seed := qualityTestClip(t, seedDir, ffmpeg, "640x360", "0")
	original := filepath.Join(dir, "original.mp4")
	args := append([]string{"-copyts", "-itsoffset", "1.868"}, mediaReadArgs(seed)...)
	args = append(args, "-map", "0:v:0", "-an", "-c:v", "copy", "-copytb", "1", "-avoid_negative_ts", "disabled", "-movflags", "+faststart", original)
	result, err := mediaCommand(context.Background(), dir, ffmpeg, args...)
	if err != nil {
		t.Fatalf("could not create the positive-start MP4: %v %s", err, result.stderr)
	}
	source, err := inspectDecodedMedia(context.Background(), dir, original, ffprobe)
	if err != nil || source.StartTime < 1 || source.Frames != 30 {
		t.Fatalf("fixture did not have a nonzero timestamp start: %+v %v", source, err)
	}
	before, _, err := hashRegularMedia(original, maximumInputBytes)
	if err != nil {
		t.Fatal(err)
	}
	report, err := runBenchmark(context.Background(), original, dir, ffmpeg, ffprobe)
	if err != nil {
		t.Fatal(err)
	}
	if report.Input.StartTime < 1 || math.Abs(report.Playback.StartTime) > 0.000002 || report.Playback.Frames != 30 || report.Playback.Width != 640 || report.Playback.Height != 360 || math.Abs(report.Playback.Duration-1) > 0.002 {
		t.Fatalf("playback did not preserve the complete zero-based clip: %+v", report.Playback)
	}
	after, _, err := hashRegularMedia(original, maximumInputBytes)
	if err != nil || before != after || report.Input.SHA256 != before {
		t.Fatal("playback normalization altered the original recording")
	}
	hash, size, err := hashRegularMedia(filepath.Join(dir, "playback.mp4"), maximumInputBytes)
	if err != nil || hash != report.Playback.SHA256 || size != report.Playback.Bytes {
		t.Fatal("playback bytes do not match their report evidence")
	}
	// Same dimensions, count and normalized timing are not sufficient: a
	// different picture at every position must fail the decoded hash check.
	changed := qualityLosslessCandidate(t, dir, ffmpeg, original, "changed-playback", "setpts=PTS-STARTPTS,hflip", "30")
	if _, err = validatePlayback(context.Background(), dir, original, changed, ffmpeg, ffprobe, source); err == nil || !strings.Contains(err.Error(), "decoded picture") {
		t.Fatalf("a playback copy with altered pixels passed identity verification: %v", err)
	}
}

func TestSSIMAlignmentIdentityAndRejectedIncompleteEvidence(t *testing.T) {
	ffmpeg, ffprobe := qualityTools(t)
	dir := t.TempDir()
	input := qualityTestClip(t, dir, ffmpeg, "640x360")
	source, err := inspectDecodedMedia(context.Background(), dir, input, ffprobe)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := referenceFrames(context.Background(), dir, input, ffmpeg)
	if err != nil || len(reference) != 20 {
		t.Fatalf("reference did not select exactly 20 frames: %v %v", reference, err)
	}
	compare := func(file, name string) (float64, int, error) {
		return compareCandidate(context.Background(), dir, input, file, ffmpeg, ffprobe, source, reference, name+".ssim.log")
	}
	identical := qualityLosslessCandidate(t, dir, ffmpeg, input, "identical", comparisonSelection, "20")
	score, count, err := compare(identical, "identity")
	if err != nil || score != 1 || count != 20 {
		t.Fatalf("identical selected pictures did not score exactly one: score=%f frames=%d error=%v", score, count, err)
	}
	short := qualityLosslessCandidate(t, dir, ffmpeg, input, "short", comparisonSelection, "19")
	if _, _, err = compare(short, "short"); err == nil || !strings.Contains(err.Error(), "frame count") {
		t.Fatalf("missing candidate frame was not rejected: %v", err)
	}
	offset := qualityLosslessCandidate(t, dir, ffmpeg, input, "offset", comparisonSelection+",setpts=PTS+1", "20")
	if _, _, err = compare(offset, "offset"); err == nil || !strings.Contains(err.Error(), "timestamps") {
		t.Fatalf("shifted candidate timestamps were silently realigned: %v", err)
	}
	shifted := qualityLosslessCandidate(t, dir, ffmpeg, input, "wrong-pictures", comparisonSelection+",trim=start_frame=1,tpad=stop_mode=clone:stop_duration=0.05,setpts=PTS-STARTPTS", "20")
	wrongScore, count, err := compare(shifted, "wrong-pictures")
	if err != nil || count != 20 || wrongScore >= 0.999 {
		t.Fatalf("different temporal content passed the identity control: score=%f frames=%d error=%v", wrongScore, count, err)
	}
	data, err := os.ReadFile(identical)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(dir, "corrupt.mp4")
	if err = os.WriteFile(corrupt, data[:len(data)/2], 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = compare(corrupt, "corrupt"); err == nil {
		t.Fatal("a truncated candidate produced a valid similarity score")
	}
}

func TestSSIMParserRequiresCompleteFiniteEvidence(t *testing.T) {
	valid := "n:1 Y:1.000000 U:1.000000 V:1.000000 All:1.000000 (inf)\nn:2 Y:0.900000 All:0.900000 (10.0)\n"
	score, count, err := parseSSIM(strings.NewReader(valid), 2)
	if err != nil || count != 2 || math.Abs(score-0.95) > 0.0000001 {
		t.Fatalf("valid per-frame evidence rejected: %f %d %v", score, count, err)
	}
	for _, input := range []string{
		"", "n:1 All:1.000000\n", "n:1 All:1\nn:1 All:1\n", "n:1 All:NaN\nn:2 All:1\n",
		"n:1 All:Inf\nn:2 All:1\n", "n:1 All:1.01\nn:2 All:1\n", "n:1 All:1\nn:2 All:1\nn:3 All:1\n",
	} {
		if _, _, err := parseSSIM(strings.NewReader(input), 2); err == nil {
			t.Fatalf("invalid or incomplete evidence accepted: %q", input)
		}
	}
}
