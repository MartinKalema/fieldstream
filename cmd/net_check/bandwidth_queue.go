package main

import (
	"errors"
	"time"
)

const (
	maxBandwidthKbps        = 1_000_000
	maxBandwidthQueueBytes  = 1 << 20
	maxProxyQueuedDatagrams = 4096
)

type bandwidthAdmission uint8

const (
	bandwidthAdmitted bandwidthAdmission = iota
	bandwidthRejected
	bandwidthResourceRejected
)

// bandwidthQueue models two independent full-duplex links. Its byte limit
// includes the packet currently being serialized. Delay/jitter is applied before
// serialization; arrival order within each direction remains FIFO. No unused
// capacity is banked, and a late timer cannot release a catch-up burst.
// Only the proxy's event-loop goroutine accesses this state.
type bandwidthQueue struct {
	kbps       int
	limit      int
	count      int
	directions [2]bandwidthLane
}

type bandwidthLane struct {
	packets []bandwidthPacket
	bytes   int
	due     time.Time
}

type bandwidthPacket struct {
	datagram datagram
	queuedAt time.Time
}

type bandwidthDelivery struct {
	datagram  datagram
	residence time.Duration
	lateness  time.Duration
}

func validateBandwidth(kbps, queueBytes int) error {
	if kbps < 0 || kbps > maxBandwidthKbps {
		return errors.New("bandwidth must be between 0 and 1000000 kilobits per second")
	}
	if kbps == 0 {
		if queueBytes != 0 {
			return errors.New("a bandwidth queue requires a positive bandwidth limit")
		}
		return nil
	}
	if queueBytes < 1 || queueBytes > maxBandwidthQueueBytes {
		return errors.New("bandwidth queue must be between 1 and 1048576 bytes per direction")
	}
	return nil
}

func newBandwidthQueue(kbps, queueBytes int) (*bandwidthQueue, error) {
	if err := validateBandwidth(kbps, queueBytes); err != nil {
		return nil, err
	}
	if kbps == 0 {
		return nil, nil
	}
	return &bandwidthQueue{kbps: kbps, limit: queueBytes}, nil
}

func (q *bandwidthQueue) serialization(bytes int) time.Duration {
	// Ceil rather than truncate: rounding must never increase the configured
	// rate. The validated rate and maximum UDP packet size bound arithmetic.
	bitsNanoseconds := int64(bytes) * 8 * int64(time.Second)
	bitsPerSecond := int64(q.kbps) * 1000
	return time.Duration((bitsNanoseconds + bitsPerSecond - 1) / bitsPerSecond)
}

func (q *bandwidthQueue) enqueue(d datagram, now time.Time) bandwidthAdmission {
	lane := &q.directions[d.direction]
	if len(d.data) > q.limit-lane.bytes {
		return bandwidthRejected
	}
	if q.count >= maxProxyQueuedDatagrams {
		return bandwidthResourceRejected
	}
	lane.packets = append(lane.packets, bandwidthPacket{datagram: d, queuedAt: now})
	lane.bytes += len(d.data)
	q.count++
	if len(lane.packets) == 1 {
		lane.due = later(now, d.at).Add(q.serialization(len(d.data)))
	}
	return bandwidthAdmitted
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func (q *bandwidthQueue) nextDue() (time.Time, bool) {
	var due time.Time
	for i := range q.directions {
		lane := &q.directions[i]
		if len(lane.packets) > 0 && (due.IsZero() || lane.due.Before(due)) {
			due = lane.due
		}
	}
	return due, !due.IsZero()
}

// take returns at most one packet from one direction. The following packet's
// serialization starts at this actual send opportunity, not its former planned
// time. Callers must write this packet before asking for another in this lane.
func (q *bandwidthQueue) take(direction int, now time.Time) (bandwidthDelivery, bool) {
	lane := &q.directions[direction]
	if len(lane.packets) == 0 || now.Before(lane.due) {
		return bandwidthDelivery{}, false
	}
	packet := lane.packets[0]
	out := bandwidthDelivery{datagram: packet.datagram, residence: now.Sub(packet.queuedAt), lateness: now.Sub(lane.due)}
	lane.packets[0] = bandwidthPacket{}
	lane.packets = lane.packets[1:]
	lane.bytes -= len(packet.datagram.data)
	q.count--
	if len(lane.packets) == 0 {
		lane.packets = nil
		lane.due = time.Time{}
	} else {
		next := lane.packets[0].datagram
		lane.due = later(now, next.at).Add(q.serialization(len(next.data)))
	}
	return out, true
}

// Account for time spent in the socket write as well: a slow write must not
// become an opportunity to immediately release the next packet.
func (q *bandwidthQueue) sent(direction int, now time.Time) {
	lane := &q.directions[direction]
	if len(lane.packets) > 0 {
		next := lane.packets[0].datagram
		lane.due = later(now, next.at).Add(q.serialization(len(next.data)))
	}
}
