// Package gstreamer provides the shared native viewer pipeline and process
// supervision used by the normal viewer and the bounded diagnostic command.
package gstreamer

import (
	"errors"
	"regexp"
	"strconv"

	"fieldvideolab/internal/lab"
)

// Config selects a registered source and the explicitly supported viewer settings.
type Config struct {
	Source    string
	Route     string
	Decoder   string
	Sink      string
	LatencyMS int
}

var sourcePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// Pipeline returns gst-launch arguments for fixed loopback receiver addresses.
// It never puts publisher credentials or an arbitrary URL into those arguments.
func Pipeline(cfg Config, sources []lab.SourceConfig) ([]string, error) {
	if !sourcePattern.MatchString(cfg.Source) {
		return nil, errors.New("invalid source ID")
	}
	registered := false
	for _, source := range sources {
		registered = registered || source.ID == cfg.Source
	}
	if !registered {
		return nil, errors.New("source is not registered in this lab")
	}
	port := lab.Field.RTSP
	if cfg.Route == "forwarded" {
		port = lab.Central.RTSP
	} else if cfg.Route != "local" {
		return nil, errors.New("invalid route")
	}
	if cfg.LatencyMS < 0 || cfg.LatencyMS > 200 {
		return nil, errors.New("invalid receiver waiting time")
	}
	args := []string{"-e", "-m", "rtspsrc", "location=rtsp://127.0.0.1:" + strconv.Itoa(port) + "/" + cfg.Source,
		"protocols=tcp", "latency=" + strconv.Itoa(cfg.LatencyMS), "drop-on-latency=true", "tcp-timeout=5000000",
		"!", "application/x-rtp,media=video,encoding-name=H264", "!", "rtph264depay", "!", "h264parse",
		"!", "video/x-h264,stream-format=avc,alignment=au", "!"}
	switch cfg.Decoder {
	case "software":
		args = append(args, "avdec_h264", "max-threads=1")
	case "hardware":
		args = append(args, "vtdec_hw")
	default:
		return nil, errors.New("invalid decoder")
	}
	// Drop only decoded pictures. Dropping arbitrary compressed H.264 frames can
	// damage the later pictures that depend on them.
	args = append(args, "!", "queue", "max-size-buffers=1", "max-size-bytes=0", "max-size-time=0", "leaky=downstream", "!", "videoconvert", "!")
	switch cfg.Sink {
	case "gl":
		args = append(args, "glimagesink", "sync=false")
	case "headless":
		args = append(args, "fakesink", "sync=false")
	default:
		return nil, errors.New("invalid sink")
	}
	return args, nil
}
