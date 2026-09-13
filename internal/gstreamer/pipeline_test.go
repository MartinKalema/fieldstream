package gstreamer

import (
	"slices"
	"testing"

	"fieldvideolab/internal/lab"
)

func TestKnownGoodMediaArgumentsRemainUnchanged(t *testing.T) {
	cfg := Config{Source: "camera-01", Route: "local", Decoder: "software", Sink: "gl", LatencyMS: 100}
	args, err := Pipeline(cfg, []lab.SourceConfig{{ID: "camera-01"}})
	want := []string{"-e", "-m", "rtspsrc", "location=rtsp://127.0.0.1:18554/camera-01",
		"protocols=tcp", "latency=100", "drop-on-latency=true", "tcp-timeout=5000000",
		"!", "application/x-rtp,media=video,encoding-name=H264", "!", "rtph264depay", "!", "h264parse",
		"!", "video/x-h264,stream-format=avc,alignment=au", "!", "avdec_h264", "max-threads=1",
		"!", "queue", "max-size-buffers=1", "max-size-bytes=0", "max-size-time=0", "leaky=downstream",
		"!", "videoconvert", "!", "glimagesink", "sync=false"}
	if err != nil || !slices.Equal(args, want) {
		t.Fatalf("shared viewer changed the tested media pipeline: %q %v", args, err)
	}
}

func TestSharedPipelineRejectsUnsupportedConfiguration(t *testing.T) {
	valid := Config{Source: "camera-01", Route: "local", Decoder: "software", Sink: "gl", LatencyMS: 100}
	for _, alter := range []func(*Config){
		func(c *Config) { c.Source = "camera-02" },
		func(c *Config) { c.Source = "rtsp://example.com/camera" },
		func(c *Config) { c.Route = "other" },
		func(c *Config) { c.Decoder = "autodecode" },
		func(c *Config) { c.Sink = "filesink" },
		func(c *Config) { c.LatencyMS = -1 },
		func(c *Config) { c.LatencyMS = 201 },
	} {
		cfg := valid
		alter(&cfg)
		if _, err := Pipeline(cfg, []lab.SourceConfig{{ID: "camera-01"}}); err == nil {
			t.Fatalf("accepted unsupported configuration: %+v", cfg)
		}
	}
}
