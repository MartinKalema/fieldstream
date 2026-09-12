package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type testScene struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Note           string `json:"note"`
	Filter         string `json:"filter"`
	SourceEncoding string `json:"source_encoding"`
}

func sceneDefinition(id string) (testScene, bool) {
	scene := testScene{ID: id, SourceEncoding: "1280x720, 30 fps, 150 frames (5 seconds); libx264 -crf 12 -preset veryfast -tune zerolatency -profile:v baseline -pix_fmt yuv420p -threads:v 2 -bf 0 -g 30 -keyint_min 30 -sc_threshold 0; one filter thread; square pixels; MP4 +faststart; 64 MiB maximum"}
	switch id {
	case "fast-motion":
		scene.Title = "Fast movement — generated test"
		scene.Note = "Computer-generated shapes move quickly across the picture. This tests compression during movement. It is not camera footage."
		scene.Filter = "testsrc2=size=1600x900:rate=30:duration=5,crop=1280:720:x='160+140*sin(2*PI*t/1.25)':y='90+70*cos(2*PI*t/1.8)'"
	case "fine-detail":
		scene.Title = "Fine lines — generated test"
		scene.Note = "A fixed chart of thin lines shows detail lost when the picture is made smaller. It does not prove that real text will remain readable."
		scene.Filter = "color=c=0x405060:size=1280x720:rate=30:duration=5,drawgrid=w=8:h=8:t=1:c=white,drawgrid=w=64:h=64:t=1:c=black"
	case "dim-noise":
		scene.Title = "Dim picture with noise — generated test"
		scene.Note = "Dark moving shapes have changing speckles added to them. This tests how compression handles noise. We still need a real camera test in low light."
		scene.Filter = "testsrc2=size=1280x720:rate=30:duration=5,lutyuv=y='16+(val-16)*0.22':u='128+(val-128)*0.35':v='128+(val-128)*0.35',noise=c0_seed=20260912:c0_strength=12:c0_flags=t+u:c1_seed=20260913:c1_strength=4:c1_flags=t+u:c2_seed=20260914:c2_strength=4:c2_flags=t+u,lutyuv=y='clip(val,16,235)':u='clip(val,16,240)':v='clip(val,16,240)'"
	default:
		return testScene{}, false
	}
	scene.Filter += ",format=yuv420p,setsar=1"
	return scene, true
}

// Generate only fixed, local filter recipes. Processing is outside the lavfi
// source so filter_threads=1 also controls seeded noise generation. Reproducible
// decoded pixels are tested on the same FFmpeg build; no cross-version identity
// is promised. The existing benchmark records the generated source checksum.
func generateScene(parent context.Context, dir, ffmpeg, id string) (testScene, error) {
	scene, ok := sceneDefinition(id)
	if !ok {
		return testScene{}, errors.New("test scene must be fast-motion, fine-detail or dim-noise")
	}
	file := filepath.Join(dir, "original.mp4")
	if _, err := os.Lstat(file); !errors.Is(err, os.ErrNotExist) {
		return scene, errors.New("synthetic original already exists or cannot be inspected")
	}
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	source, processing, _ := strings.Cut(scene.Filter, ",")
	result, err := mediaCommand(ctx, dir, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-n", "-xerror", "-max_alloc", "67108864",
		"-filter_threads", "1", "-filter_complex_threads", "1", "-f", "lavfi", "-i", source, "-vf", processing,
		"-map", "0:v:0", "-an", "-sn", "-dn", "-frames:v", "150", "-fps_mode:v", "passthrough",
		"-c:v", "libx264", "-crf", "12", "-preset", "veryfast", "-tune", "zerolatency", "-profile:v", "baseline",
		"-pix_fmt", "yuv420p", "-threads:v", "2", "-bf", "0", "-g", "30", "-keyint_min", "30", "-sc_threshold", "0",
		"-fs", "67108864", "-movflags", "+faststart", "-f", "mp4", file)
	if err != nil {
		return scene, fmt.Errorf("synthetic scene generation failed: %w", err)
	}
	if len(bytes.TrimSpace(result.stderr)) != 0 {
		return scene, errors.New("synthetic scene generation reported a media error")
	}
	if err := os.Chmod(file, 0600); err != nil {
		return scene, err
	}
	if _, _, err := hashRegularMedia(file, maximumInputBytes); err != nil {
		return scene, err
	}
	frames, err := decodedPictureEvidence(ctx, dir, file, ffmpeg)
	if err != nil || len(frames) != 150 {
		return scene, errors.New("synthetic scene did not produce all 150 decoded frames; a truncated source is not accepted")
	}
	for i, frame := range frames {
		if frame.bytes != 1280*720*3/2 || math.Abs(frame.timestamp-float64(i)/30) > 0.000002 {
			return scene, errors.New("synthetic scene dimensions or frame timing do not match the fixed recipe")
		}
	}
	return scene, nil
}
