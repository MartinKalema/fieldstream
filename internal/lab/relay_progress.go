package lab

import (
	"bytes"
	"io"
	"strconv"
	"sync"
	"time"
)

const (
	relayProgressLineLimit = 1024
	relayProgressGrace     = 12 * time.Second
	relayInputFreshness    = 3 * time.Second
	relayProgressLagLimit  = 3 * time.Second
)

// relayProgress observes FFmpeg's processed output timestamps, not delivery or
// camera freshness. In particular, FFmpeg's RTSP muxer can ignore TCP write
// errors, so both stopped progress and progress falling behind need watching.
// Only the same-machine forwarder uses this writer. It always drains stdout,
// even when a line is malformed or its bounded rolling log cannot be written.
type relayProgress struct {
	mu  sync.Mutex
	log io.Writer
	now func() time.Time

	line     [relayProgressLineLimit]byte
	lineLen  int
	dropping bool

	startedAt   time.Time
	lastAdvance time.Time
	outTimeUS   int64
	bestOffset  time.Duration
	offsetKnown bool

	inputKnown       bool
	inputBytes       uint64
	publisherID      string
	lastInputAdvance time.Time
	inputGrace       time.Time
}

func newRelayProgress(log io.Writer, now func() time.Time) *relayProgress {
	start := now()
	return &relayProgress{log: log, now: now, startedAt: start, lastAdvance: start, inputGrace: start}
}

func (p *relayProgress) Write(data []byte) (int, error) {
	p.mu.Lock()
	for _, b := range data {
		if b == '\n' {
			if !p.dropping {
				p.readLine(p.line[:p.lineLen])
			}
			p.lineLen, p.dropping = 0, false
		} else if !p.dropping {
			if p.lineLen == len(p.line) {
				p.lineLen, p.dropping = 0, true
			} else {
				p.line[p.lineLen] = b
				p.lineLen++
			}
		}
	}
	p.mu.Unlock()
	if p.log != nil {
		_, _ = p.log.Write(data)
	}
	return len(data), nil
}

// Called with mu held. Duplicate, absent, negative, overflowing and malformed
// timestamps are not progress. A zero initial timestamp is not a moving frame.
func (p *relayProgress) readLine(line []byte) {
	const prefix = "out_time_us="
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return
	}
	value, err := strconv.ParseInt(string(line[len(prefix):]), 10, 64)
	if err != nil || value <= p.outTimeUS || value > int64((1<<63-1)/time.Microsecond) {
		return
	}
	now := p.now()
	p.outTimeUS, p.lastAdvance = value, now
	offset := time.Duration(value)*time.Microsecond - now.Sub(p.startedAt)
	if !p.offsetKnown || offset > p.bestOffset {
		p.bestOffset, p.offsetKnown = offset, true
	}
}

// reason is called by the supervisor, using time.Now's monotonic clock. Source
// continuity is deliberately required: an input outage must not be mistaken for
// an output stall. A reconnect or return of activity starts a fresh grace period.
func (p *relayProgress) reason(source SourceState, now time.Time) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !source.Observed || !source.Ready {
		p.inputKnown, p.offsetKnown = false, false
		p.lastInputAdvance = time.Time{}
		p.inputGrace = now
		return ""
	}
	if !p.inputKnown || source.PublisherID != p.publisherID || source.BytesReceived < p.inputBytes {
		p.inputKnown, p.offsetKnown = true, false
		p.inputBytes, p.publisherID = source.BytesReceived, source.PublisherID
		p.lastInputAdvance = time.Time{}
		p.inputGrace = now
		return ""
	}
	if source.BytesReceived > p.inputBytes {
		if p.lastInputAdvance.IsZero() || now.Sub(p.lastInputAdvance) > relayInputFreshness {
			p.inputGrace, p.offsetKnown = now, false
		}
		p.lastInputAdvance = now
	}
	p.inputBytes = source.BytesReceived
	if p.lastInputAdvance.IsZero() || now.Sub(p.lastInputAdvance) > relayInputFreshness ||
		now.Sub(p.startedAt) < relayProgressGrace || now.Sub(p.inputGrace) < relayProgressGrace {
		return ""
	}
	if now.Sub(p.lastAdvance) >= relayProgressGrace {
		return "Local forwarding made no output progress while input continued; restarting."
	}
	lag := now.Sub(p.startedAt) - time.Duration(p.outTimeUS)*time.Microsecond + p.bestOffset
	if p.offsetKnown && lag > relayProgressLagLimit {
		return "Local forwarding fell behind while input continued; restarting."
	}
	return ""
}
