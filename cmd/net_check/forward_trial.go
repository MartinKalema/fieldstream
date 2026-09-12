package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func isRelayTrial(p profile) bool { return strings.HasPrefix(p.Name, "relay-") }

func relayTrialAuthentication(secret string) string {
	return fmt.Sprintf("authMethod: internal\nauthInternalUsers:\n  - user: benchmark\n    pass: %s\n    ips: [127.0.0.1]\n    permissions:\n      - action: publish\n        path: reference\n      - action: publish\n        path: impaired\n  - user: any\n    ips: [127.0.0.1]\n    permissions:\n      - action: read\n        path: reference\n      - action: read\n        path: impaired\n      - action: api\n", secret)
}

func relayTrialCommand(p profile, rtspPort int, secret, srtDestination string) []string {
	// Keep the ordinary controller input options unchanged in every variant.
	// In particular, nobuffer would discard packets used for input discovery.
	args := []string{"-hide_banner", "-loglevel", "warning", "-nostdin", "-rtsp_transport", "tcp", "-timeout", "3000000", "-i", fmt.Sprintf("rtsp://127.0.0.1:%d/reference", rtspPort), "-map", "0:v:0", "-an", "-c:v", "copy"}
	if p.Name == "relay-srt120" {
		return append(args, "-f", "mpegts", srtDestination)
	}
	if p.Name == "relay-rtsp-flush" {
		args = append(args, "-flush_packets", "1", "-muxdelay", "0")
	}
	destination := &url.URL{Scheme: "rtsp", User: url.UserPassword("benchmark", secret), Host: fmt.Sprintf("127.0.0.1:%d", rtspPort), Path: "/impaired"}
	return append(args, "-f", "rtsp", "-rtsp_transport", "tcp", destination.String())
}

func relayTrialSourceType(ctx context.Context, api int) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/v3/paths/get/impaired", api), nil)
	if err != nil {
		return ""
	}
	client := http.Client{Timeout: time.Second}
	response, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	var v struct {
		Source struct {
			Type string `json:"type"`
		} `json:"source"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&v) != nil {
		return ""
	}
	return v.Source.Type
}

func relayTrialMetadata(metadata map[string]any, sourceFrames int) {
	expected := lastScored - firstScored
	metadata["experiment"] = "copy relay transport comparison"
	metadata["source_frames"] = sourceFrames
	metadata["source_encoding"] = "18s testsrc2 1280x720 30fps, libx264 single thread ultrafast zerolatency Baseline yuv420p, no Bframes, keyframe every30frames, 2Mbps maxrate, 1Mbit buffer"
	metadata["topology"] = "generated file -> SRT120 -> reference path -> FFmpeg copy relay -> impaired path; one isolated MediaMTX process serves both paths; independent identical RTSP/TCP decoders share one Go monotonic clock"
	metadata["scope"] = "Measures added same-frame decoded arrival delay across a copy relay on the same Mac; no physical camera, viewer, separate-host network or absolute source-age measurement. One receiver process is used for both paths, whereas the normal lab uses two."
	metadata["impairment"] = "No deliberate loss or delay. Seeds are repeat labels; the SRT relay alone crosses a zero-impairment proxy. RTSP variants connect directly over loopback TCP."
	metadata["tee_fifo_queue_frames"] = 0
	metadata["authentication"] = "Private per-trial random credential, localhost-only publisher permission limited to the two trial paths; anonymous localhost readers/API only. RTSP credentials are redacted from logs."
	metadata["relay_input"] = "All variants retain controller RTSP/TCP input, 3-second input timeout and default probing; output video is copied without re-encoding."
	metadata["relay_variants"] = map[string]any{
		"relay-srt120":     "controller-style MPEG-TS/SRT output; requested120ms and receiver negotiation verified",
		"relay-rtsp":       "authenticated direct RTSP/TCP output with default mux options; no SRT wait applies",
		"relay-rtsp-flush": "same authenticated RTSP/TCP output plus output flush_packets1 and muxdelay0",
	}
	metadata["quality"] = fmt.Sprintf("All %d original encoded-then-decoded frames in preselected source interval[6,17)seconds form the denominator; first6seconds permit default relay probing, never an adaptive exclusion. Exact identities use unique pixel hashes; missing/nonmatching assignments require consistent PTS mapping.", expected)
	metadata["timeliness"] = fmt.Sprintf("Exact relay frame arrival minus exact reference frame arrival <=250/500ms; denominator all%dexpected, missing/nonmatching/unpaired frames never timely.", expected)
	metadata["comparison_gates"] = fmt.Sprintf("Timing inconclusive unless reference is%d/%d exact with consistent PTS mappings, maximum reference output gap<=200ms and pacing drift<=150ms, no proxy resource drops/FIFO overflow, correct receiver-observed publisher protocol and requested SRT waits negotiated. Startup failure or insufficient fixed-window frames remain visible.", expected, expected)
}
