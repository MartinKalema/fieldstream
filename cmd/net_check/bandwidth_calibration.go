package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

const (
	calibrationPayloadBytes = 1316
	calibrationWarmup       = 500 * time.Millisecond
	calibrationSample       = 2 * time.Second
	calibrationMaximumKbps  = 100_000
)

type bandwidthCalibration struct {
	RateKbps          int        `json:"nominal_payload_kbps"`
	MeasuredKbps      float64    `json:"measured_payload_kbps"`
	Ratio             float64    `json:"measured_to_nominal_ratio"`
	Valid             bool       `json:"valid"`
	Flags             []string   `json:"flags"`
	PayloadBytes      int        `json:"payload_bytes_per_datagram"`
	WarmupMS          float64    `json:"warmup_ms"`
	SampleDurationMS  float64    `json:"sample_duration_ms"`
	ReceivedDatagrams int64      `json:"scored_received_datagrams"`
	ReceivedBytes     int64      `json:"scored_received_payload_bytes"`
	SentDatagrams     int64      `json:"sent_datagrams_including_warmup"`
	SentBytes         int64      `json:"sent_payload_bytes_including_warmup"`
	SaturationDrops   int64      `json:"deliberate_bandwidth_drops"`
	Proxy             proxyStats `json:"proxy"`
}

// assessBandwidthCalibration uses a fixed, declared scoring interval rather
// than shortening the denominator to the first and last delivered packets.
// The sender must fill the finite model queue sufficiently to cause an actual
// deliberate drop: an underloaded stream cannot calibrate the link's capacity.
func assessBandwidthCalibration(c bandwidthCalibration) bandwidthCalibration {
	c.Valid = false
	c.Flags = []string{}
	c.MeasuredKbps, c.Ratio = 0, 0
	c.SaturationDrops = c.Proxy.BandwidthDrops
	if c.SampleDurationMS != float64(calibrationSample)/float64(time.Millisecond) || c.WarmupMS != float64(calibrationWarmup)/float64(time.Millisecond) {
		c.Flags = append(c.Flags, "calibration did not retain its fixed 500ms warmup and 2000ms scoring interval")
	}
	if c.RateKbps < 1 || c.RateKbps > calibrationMaximumKbps {
		c.Flags = append(c.Flags, "calibration rate is outside the supported 1–100000kbps interval")
	}
	if c.PayloadBytes != calibrationPayloadBytes || c.ReceivedDatagrams < 0 || c.ReceivedBytes != c.ReceivedDatagrams*calibrationPayloadBytes {
		c.Flags = append(c.Flags, "calibration payload byte accounting is inconsistent")
	}
	if c.SentDatagrams < c.ReceivedDatagrams || c.SentDatagrams < 1 || c.SentBytes != c.SentDatagrams*calibrationPayloadBytes || c.Proxy.Forwarded < c.ReceivedDatagrams {
		c.Flags = append(c.Flags, "calibration sender and receiver counts are inconsistent")
	}
	if c.ReceivedDatagrams < 20 {
		c.Flags = append(c.Flags, "fewer than 20 datagrams arrived in the fixed scoring interval")
	}
	if c.SampleDurationMS > 0 {
		c.MeasuredKbps = float64(c.ReceivedBytes) * 8 / c.SampleDurationMS
	}
	if c.RateKbps > 0 {
		c.Ratio = c.MeasuredKbps / float64(c.RateKbps)
	}
	if c.Ratio < .85 || c.Ratio > 1.01 {
		c.Flags = append(c.Flags, "measured payload capacity is outside 85–101% of the nominal rate")
	}
	if c.Proxy.ResourceDrops != 0 {
		c.Flags = append(c.Flags, "proxy implementation or socket resource drops occurred")
	}
	for _, direction := range c.Proxy.Directions {
		if direction.TimerLatenessMaxMS > 25 {
			c.Flags = append(c.Flags, "proxy timer was more than 25ms late")
			break
		}
	}
	if c.SaturationDrops < 1 {
		c.Flags = append(c.Flags, "no deliberate queue drop demonstrated a saturated link")
	}
	c.Valid = len(c.Flags) == 0
	return c
}

type calibrationSenderResult struct {
	packets int64
	err     error
}

// sendCalibrationPayload targets twice the nominal rate. Credit is bounded to
// five milliseconds (at least one datagram), so delayed ticks cannot produce an
// unbounded burst. This intentionally creates a saturated, payload-only stream;
// it does not imitate SRT timing or allocate one goroutine per packet.
func sendCalibrationPayload(ctx context.Context, conn *net.UDPConn, destination *net.UDPAddr, kbps int) calibrationSenderResult {
	payload := make([]byte, calibrationPayloadBytes)
	payload[0] = 0x40
	rate := float64(kbps) * 1000 * 2 / 8
	burst := max(float64(calibrationPayloadBytes), rate*.005)
	credit := float64(calibrationPayloadBytes)
	last := time.Now()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	var out calibrationSenderResult
	for {
		for credit >= calibrationPayloadBytes {
			if ctx.Err() != nil {
				return out
			}
			_ = conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
			n, err := conn.WriteToUDP(payload, destination)
			if err != nil {
				if ctx.Err() == nil {
					out.err = err
				}
				return out
			}
			if n != len(payload) {
				out.err = errors.New("short UDP payload write")
				return out
			}
			out.packets++
			credit -= calibrationPayloadBytes
		}
		select {
		case <-ctx.Done():
			return out
		case <-ticker.C:
			now := time.Now()
			credit = min(burst, credit+now.Sub(last).Seconds()*rate)
			last = now
		}
	}
}

// calibrateBandwidth measures an isolated loopback proxy with 1316-byte UDP
// payloads. The scored receiver window is [500ms,2500ms) after the sender starts;
// startup and final queue drain are excluded. UDP/IP headers are not counted.
// Its small fixed workload, bounded queues and four-second parent deadline leave
// time for socket/goroutine cleanup within the five-second operation allowance.
func calibrateBandwidth(ctx context.Context, kbps, queueBytes int) (out bandwidthCalibration, runError error) {
	out = bandwidthCalibration{RateKbps: kbps, PayloadBytes: calibrationPayloadBytes,
		WarmupMS: float64(calibrationWarmup) / float64(time.Millisecond), SampleDurationMS: float64(calibrationSample) / float64(time.Millisecond), Flags: []string{}}
	if kbps < 1 || kbps > calibrationMaximumKbps {
		return out, errors.New("calibration bandwidth must be between 1 and 100000 kilobits per second")
	}
	if err := validateBandwidth(kbps, queueBytes); err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	bounded, cancelBounded := context.WithTimeout(ctx, 4*time.Second)
	defer cancelBounded()
	receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return out, err
	}
	defer receiver.Close()
	sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return out, err
	}
	defer sender.Close()
	proxyCtx, stopProxy := context.WithCancel(bounded)
	defer stopProxy()
	p, err := startProxy(proxyCtx, receiver.LocalAddr().(*net.UDPAddr), profile{BandwidthKbps: kbps, QueueBytes: queueBytes}, 17)
	if err != nil {
		return out, err
	}
	proxyAddress := p.conn.LocalAddr().(*net.UDPAddr)
	senderCtx, stopSender := context.WithCancel(bounded)
	defer stopSender()
	// An external cancellation interrupts a blocked receive without closing the
	// receiver while the proxy may still be writing to it.
	stopWake := context.AfterFunc(bounded, func() { _ = receiver.SetReadDeadline(time.Now()); _ = sender.SetWriteDeadline(time.Now()) })
	defer stopWake()
	start := time.Now()
	scoreStart, scoreEnd := start.Add(calibrationWarmup), start.Add(calibrationWarmup+calibrationSample)
	senderDone := make(chan calibrationSenderResult, 1)
	go func() { senderDone <- sendCalibrationPayload(senderCtx, sender, proxyAddress, kbps) }()
	defer func() {
		stopSender()
		result := <-senderDone
		out.SentDatagrams, out.SentBytes = result.packets, result.packets*calibrationPayloadBytes
		stopProxy()
		<-p.done // Avoid introducing a socket-close error in the proxy's counters.
		out.Proxy = p.finish()
		if runError == nil && result.err != nil {
			runError = fmt.Errorf("calibration sender: %w", result.err)
		}
		if runError == nil {
			out = assessBandwidthCalibration(out)
		}
	}()
	_ = receiver.SetReadDeadline(scoreEnd)
	payload := make([]byte, calibrationPayloadBytes+1)
	for {
		if err := bounded.Err(); err != nil {
			return out, err
		}
		n, address, err := receiver.ReadFromUDP(payload)
		now := time.Now()
		if err != nil {
			if bounded.Err() != nil {
				return out, bounded.Err()
			}
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() && !now.Before(scoreEnd) {
				break
			}
			return out, fmt.Errorf("calibration receiver: %w", err)
		}
		if !now.Before(scoreEnd) {
			break
		}
		if address.Port != proxyAddress.Port || !address.IP.Equal(proxyAddress.IP) || n != calibrationPayloadBytes || payload[0] != 0x40 {
			return out, errors.New("calibration receiver got an unexpected sender or payload")
		}
		if !now.Before(scoreStart) {
			out.ReceivedDatagrams++
			out.ReceivedBytes += int64(n)
		}
	}
	return out, nil
}
