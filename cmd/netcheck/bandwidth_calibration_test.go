package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func validCalibrationEvidence() bandwidthCalibration {
	return bandwidthCalibration{RateKbps: 1000, PayloadBytes: 1316, WarmupMS: 500, SampleDurationMS: 2000,
		ReceivedDatagrams: 171, ReceivedBytes: 171 * 1316, SentDatagrams: 400, SentBytes: 400 * 1316,
		Proxy: proxyStats{Forwarded: 210, BandwidthDrops: 160, Directions: [2]proxyDirectionStats{{TimerLatenessMaxMS: 1}}}}
}

func TestBandwidthCalibrationAssessment(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*bandwidthCalibration)
		valid  bool
	}{
		{"valid saturated link", func(*bandwidthCalibration) {}, true},
		{"expected congestion remains valid", func(c *bandwidthCalibration) { c.Proxy.BandwidthDrops = 999 }, true},
		{"25ms lateness is allowed", func(c *bandwidthCalibration) { c.Proxy.Directions[0].TimerLatenessMaxMS = 25 }, true},
		{"timer lost capacity", func(c *bandwidthCalibration) { c.ReceivedDatagrams = 150; c.ReceivedBytes = 150 * 1316 }, false},
		{"too fast", func(c *bandwidthCalibration) { c.ReceivedDatagrams = 192; c.ReceivedBytes = 192 * 1316 }, false},
		{"no saturation evidence", func(c *bandwidthCalibration) { c.Proxy.BandwidthDrops = 0 }, false},
		{"implementation dropped packets", func(c *bandwidthCalibration) { c.Proxy.ResourceDrops = 1 }, false},
		{"timer delay", func(c *bandwidthCalibration) { c.Proxy.Directions[0].TimerLatenessMaxMS = 25.001 }, false},
		{"wrong scoring interval", func(c *bandwidthCalibration) { c.SampleDurationMS = 1900 }, false},
		{"wrong warmup", func(c *bandwidthCalibration) { c.WarmupMS = 0 }, false},
		{"too few samples", func(c *bandwidthCalibration) { c.ReceivedDatagrams = 19; c.ReceivedBytes = 19 * 1316 }, false},
		{"wrong byte accounting", func(c *bandwidthCalibration) { c.ReceivedBytes++ }, false},
		{"no sending evidence", func(c *bandwidthCalibration) { c.SentDatagrams = 0; c.SentBytes = 0 }, false},
		{"received more than forwarded", func(c *bandwidthCalibration) { c.Proxy.Forwarded = 170 }, false},
		{"invalid rate", func(c *bandwidthCalibration) { c.RateKbps = 0 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validCalibrationEvidence()
			tc.mutate(&c)
			result := assessBandwidthCalibration(c)
			if result.Valid != tc.valid {
				t.Fatalf("valid=%t want=%t flags=%v ratio=%f", result.Valid, tc.valid, result.Flags, result.Ratio)
			}
			if !result.Valid && len(result.Flags) == 0 {
				t.Fatal("invalid calibration lacks a reason")
			}
			if result.Valid && len(result.Flags) != 0 {
				t.Fatal("valid calibration retained flags")
			}
		})
	}
	c := assessBandwidthCalibration(validCalibrationEvidence())
	if c.MeasuredKbps != 900.144 || c.Ratio != c.MeasuredKbps/1000 {
		t.Fatalf("wrong fixed-window calculation: %+v", c)
	}
}

func TestBandwidthCalibrationRejectsInvalidArgumentsAndCancellation(t *testing.T) {
	for _, tc := range []struct{ rate, bytes int }{{0, 32768}, {-1, 32768}, {100001, 32768}, {500, 0}, {500, maxBandwidthQueueBytes + 1}} {
		if _, err := calibrateBandwidth(context.Background(), tc.rate, tc.bytes); err == nil {
			t.Fatalf("accepted rate=%d bytes=%d", tc.rate, tc.bytes)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := calibrateBandwidth(ctx, 500, 32768); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled context: %v", err)
	}
}

func TestBandwidthCalibrationLoopback(t *testing.T) {
	if testing.Short() {
		t.Skip("timed loopback calibration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	result, err := calibrateBandwidth(ctx, 512, 32768)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid {
		t.Fatalf("calibration invalid: rate=%f ratio=%f flags=%v", result.MeasuredKbps, result.Ratio, result.Flags)
	}
	if time.Since(start) >= 5*time.Second {
		t.Fatal("calibration exceeded its bounded duration")
	}
	if result.ReceivedDatagrams < 20 || result.SaturationDrops < 1 || result.Proxy.ResourceDrops != 0 {
		t.Fatalf("incomplete calibration evidence: %+v", result)
	}
}
