package main

import (
	"context"
	"net"
	"testing"
	"time"
)

func bandwidthPacketAt(direction, size int, at time.Time) datagram {
	return datagram{direction: direction, data: make([]byte, size), at: at}
}

func TestBandwidthConfiguration(t *testing.T) {
	for _, tc := range []struct {
		rate, bytes int
		valid       bool
	}{
		{0, 0, true}, {80, 2048, true}, {1, 1, true}, {maxBandwidthKbps, maxBandwidthQueueBytes, true},
		{-1, 0, false}, {maxBandwidthKbps + 1, 2048, false}, {80, 0, false}, {80, -1, false},
		{80, maxBandwidthQueueBytes + 1, false}, {0, 2048, false},
	} {
		q, err := newBandwidthQueue(tc.rate, tc.bytes)
		if (err == nil) != tc.valid {
			t.Fatalf("rate=%d queue=%d error=%v", tc.rate, tc.bytes, err)
		}
		if tc.valid && tc.rate == 0 && q != nil {
			t.Fatal("unlimited mode created a limiter")
		}
	}
}

func TestBandwidthAtCapacityAndOverload(t *testing.T) {
	q, _ := newBandwidthQueue(80, 2000)
	start := time.Unix(1, 0)
	first := bandwidthPacketAt(0, 1000, start)
	first.data[0] = 1
	second := bandwidthPacketAt(0, 1000, start)
	second.data[0] = 2
	if q.enqueue(first, start) != bandwidthAdmitted || q.enqueue(second, start) != bandwidthAdmitted {
		t.Fatal("packets within byte limit rejected")
	}
	if q.enqueue(bandwidthPacketAt(0, 1, start), start) != bandwidthRejected {
		t.Fatal("full byte queue did not tail-drop")
	}
	if q.count != 2 || q.directions[0].bytes != 2000 {
		t.Fatal("drop changed accepted queue")
	}
	if _, ok := q.take(0, start.Add(99*time.Millisecond)); ok {
		t.Fatal("packet bypassed serialization")
	}
	delivery, ok := q.take(0, start.Add(100*time.Millisecond))
	if !ok || delivery.datagram.data[0] != 1 || delivery.residence != 100*time.Millisecond || delivery.lateness != 0 {
		t.Fatalf("bad first delivery: %+v", delivery)
	}
	if q.directions[0].bytes != 1000 {
		t.Fatal("serialized packet remained in byte budget")
	}
	delivery, ok = q.take(0, start.Add(200*time.Millisecond))
	if !ok || delivery.datagram.data[0] != 2 || q.count != 0 {
		t.Fatal("FIFO or second serialization failed")
	}
	if _, ok := q.nextDue(); ok {
		t.Fatal("empty queue has timer")
	}
}

func TestBandwidthDirectionsAreIndependent(t *testing.T) {
	q, _ := newBandwidthQueue(80, 1000)
	start := time.Unix(1, 0)
	for dir := 0; dir < 2; dir++ {
		if q.enqueue(bandwidthPacketAt(dir, 1000, start), start) != bandwidthAdmitted {
			t.Fatal("one direction consumed the other's queue")
		}
		if q.enqueue(bandwidthPacketAt(dir, 1, start), start) != bandwidthRejected {
			t.Fatal("direction exceeded byte limit")
		}
	}
	for dir := 0; dir < 2; dir++ {
		if _, ok := q.take(dir, start.Add(100*time.Millisecond)); !ok {
			t.Fatal("one direction consumed the other's serialization capacity")
		}
	}
}

func TestBandwidthLateWakeAndSlowWriteDoNotBurst(t *testing.T) {
	q, _ := newBandwidthQueue(80, 3000)
	start := time.Unix(1, 0)
	for i := 0; i < 3; i++ {
		q.enqueue(bandwidthPacketAt(0, 1000, start), start)
	}
	delivery, ok := q.take(0, start.Add(time.Second))
	if !ok || delivery.lateness != 900*time.Millisecond {
		t.Fatal("late timer evidence lost")
	}
	if _, ok := q.take(0, start.Add(time.Second)); ok {
		t.Fatal("late wake released a catch-up burst")
	}
	// Socket writing took another 50 ms. That interval is not saved capacity.
	q.sent(0, start.Add(1050*time.Millisecond))
	if _, ok := q.take(0, start.Add(1149*time.Millisecond)); ok {
		t.Fatal("slow socket write permitted a burst")
	}
	if _, ok := q.take(0, start.Add(1150*time.Millisecond)); !ok {
		t.Fatal("next packet never became due")
	}
}

func TestBandwidthIdleAndDelayCannotBankCapacity(t *testing.T) {
	q, _ := newBandwidthQueue(80, 2000)
	start := time.Unix(1, 0)
	q.enqueue(bandwidthPacketAt(0, 1000, start), start)
	q.take(0, start.Add(100*time.Millisecond))
	later := start.Add(3 * time.Hour)
	q.enqueue(bandwidthPacketAt(0, 1000, later.Add(200*time.Millisecond)), later)
	q.enqueue(bandwidthPacketAt(0, 1000, later), later)
	if _, ok := q.take(0, later.Add(299*time.Millisecond)); ok {
		t.Fatal("idle credit or jitter bypassed FIFO serialization")
	}
	if _, ok := q.take(0, later.Add(300*time.Millisecond)); !ok {
		t.Fatal("head never became ready")
	}
	if _, ok := q.take(0, later.Add(399*time.Millisecond)); ok {
		t.Fatal("second packet bypassed serialization")
	}
	if _, ok := q.take(0, later.Add(400*time.Millisecond)); !ok {
		t.Fatal("second packet never became ready")
	}
}

func TestBandwidthResourceLimitIsDistinctFromCongestion(t *testing.T) {
	q, _ := newBandwidthQueue(80, maxBandwidthQueueBytes)
	start := time.Unix(1, 0)
	for i := 0; i < maxProxyQueuedDatagrams; i++ {
		if q.enqueue(bandwidthPacketAt(i%2, 1, start), start) != bandwidthAdmitted {
			t.Fatal("early resource rejection")
		}
	}
	if q.enqueue(bandwidthPacketAt(0, 1, start), start) != bandwidthResourceRejected {
		t.Fatal("implementation limit reported as deliberate congestion")
	}
	if q.count != maxProxyQueuedDatagrams {
		t.Fatal("resource rejection changed queue")
	}
	fractional, _ := newBandwidthQueue(3, 1000)
	if fractional.serialization(1) != 2666667*time.Nanosecond {
		t.Fatal("serialization did not round duration upward")
	}
}

func loopbackSocket(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestBandwidthProxyActuallyPacesPayload(t *testing.T) {
	destination := loopbackSocket(t)
	source := loopbackSocket(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := startProxy(ctx, destination.LocalAddr().(*net.UDPAddr), profile{BandwidthKbps: 160, QueueBytes: 16384}, 17)
	if err != nil {
		t.Fatal(err)
	}
	defer p.finish()
	payload := make([]byte, 1000)
	start := time.Now()
	for i := 0; i < 5; i++ {
		payload[0] = byte(i)
		if _, err := source.WriteToUDP(payload, p.conn.LocalAddr().(*net.UDPAddr)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		destination.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, _, err := destination.ReadFromUDP(payload)
		if err != nil || n != 1000 || payload[0] != byte(i) {
			t.Fatalf("packet %d: bytes=%d err=%v", i, n, err)
		}
		now := time.Now()
		if i == 0 && now.Sub(start) < 45*time.Millisecond {
			t.Fatal("first packet was not serialized")
		}
	}
	// A late receiving test goroutine can observe several already delivered
	// packets together, so do not infer sender bursts from receiver read gaps.
	if time.Since(start) < 245*time.Millisecond {
		t.Fatal("five packets exceeded the configured payload rate")
	}
	cancel()
	s := p.finish()
	if s.ResourceDrops != 0 || s.BandwidthDrops != 0 || s.Forwarded != 5 {
		t.Fatalf("unexpected drops: %+v", s)
	}
	d := s.Directions[0]
	if d.ReceivedBytes != 5000 || d.ForwardedBytes != 5000 || d.SerializationCompletions != 5 || d.ForwardedPayloadKbps <= 0 || d.ForwardedPayloadKbps > 160.1 {
		t.Fatalf("incorrect rate/byte evidence: %+v", d)
	}
}

func TestBandwidthProxyDropsAndCancelsPendingQueue(t *testing.T) {
	destination := loopbackSocket(t)
	source := loopbackSocket(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// At 8 kbps the first 1000-byte packet takes one second. Cancellation is
	// tested while both byte-budgeted packets are still waiting.
	p, err := startProxy(ctx, destination.LocalAddr().(*net.UDPAddr), profile{BandwidthKbps: 8, QueueBytes: 2000}, 17)
	if err != nil {
		t.Fatal(err)
	}
	defer p.finish()
	for i := 0; i < 5; i++ {
		if _, err := source.WriteToUDP(make([]byte, 1000), p.conn.LocalAddr().(*net.UDPAddr)); err != nil {
			t.Fatal(err)
		}
	}
	// An event-loop barrier is intentionally not exposed for tests. Give five
	// loopback datagrams a generous window, still well below serialization.
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case <-p.done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancellation waited for queued packets")
	}
	s := p.finish()
	if time.Since(start) > 500*time.Millisecond || s.Forwarded != 0 || s.Received != 5 || s.BandwidthDrops != 3 || s.ResourceDrops != 0 {
		t.Fatalf("cancellation/drop accounting: %+v", s)
	}
	d := s.Directions[0]
	if d.ReceivedBytes != 5000 || d.BandwidthDropBytes != 3000 || d.ShutdownQueuedBytes != 2000 || d.MaxQueuedBytes != 2000 {
		t.Fatalf("byte accounting: %+v", d)
	}
}
