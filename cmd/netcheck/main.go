// netcheck benchmarks synthetic video on isolated loopback ports. It never loads
// normal lab settings, records a physical camera, or contacts object storage.
package main

import (
	"bufio"
	"container/heap"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	prng "math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const fps = 30

var firstScored, lastScored = 60, 330 // Default [2,11) seconds; relay mode uses [6,17).

type frame struct {
	PTS       int64   `json:"pts"`
	Hash      string  `json:"hash"`
	ArrivalMS float64 `json:"arrival_ms"`
}
type capture struct {
	Frames   []frame `json:"frames"`
	Timebase float64 `json:"timebase_seconds"`
	mu       *sync.Mutex
}
type distribution struct {
	Count                   int `json:"count"`
	Min, P50, P95, P99, Max float64
}

func dist(v []float64) distribution {
	if len(v) == 0 {
		return distribution{}
	}
	v = append([]float64(nil), v...)
	sort.Float64s(v)
	p := func(q float64) float64 { return v[int(math.Ceil(q*float64(len(v))))-1] }
	return distribution{len(v), v[0], p(.5), p(.95), p(.99), v[len(v)-1]}
}

type quality struct {
	Expected                    int             `json:"expected"`
	Exact                       int             `json:"exact"`
	Missing                     int             `json:"missing"`
	Nonmatching                 int             `json:"nonmatching"`
	DuplicateOutputs            int             `json:"duplicate_outputs"`
	IntactFraction              float64         `json:"intact_fraction"`
	MappingConsistent           bool            `json:"mapping_consistent"`
	Offset                      int             `json:"source_index_offset"`
	ScoredOutputs               int             `json:"scored_outputs"`
	ArrivalGapsMS               distribution    `json:"arrival_gaps_ms"`
	IntactArrivalGapsMS         distribution    `json:"intact_arrival_gaps_ms"`
	LongestNonIntactRunFrames   int             `json:"longest_nonintact_run_frames_including_boundaries"`
	LongestNonIntactRunMS       float64         `json:"longest_nonintact_run_source_ms"`
	UnlocatedNonmatchingOutputs int             `json:"unlocated_nonmatching_outputs"`
	MaxScheduleDriftMS          float64         `json:"max_schedule_drift_ms"`
	Times                       map[int]float64 `json:"-"`
}

type profile struct {
	Name          string  `json:"name"`
	DelayMS       int     `json:"one_way_delay_ms"`
	JitterMS      int     `json:"uniform_jitter_plus_minus_ms"`
	Loss          float64 `json:"independent_packet_loss"`
	BlackoutMS    int     `json:"bidirectional_blackout_ms"`
	BandwidthKbps int     `json:"bandwidth_kbps_each_direction,omitempty"`
	QueueBytes    int     `json:"bandwidth_queue_bytes_each_direction,omitempty"`
}
type proxyStats struct {
	Received       int64                  `json:"received_datagrams"`
	Forwarded      int64                  `json:"forwarded_datagrams"`
	RandomDrops    int64                  `json:"random_drops"`
	BlackoutDrops  int64                  `json:"blackout_drops"`
	BandwidthDrops int64                  `json:"bandwidth_drops"`
	ResourceDrops  int64                  `json:"resource_drops"`
	MaxQueued      int                    `json:"max_queued_datagrams"`
	Directions     [2]proxyDirectionStats `json:"directions"`
}

type proxyDirectionStats struct {
	Direction                string  `json:"direction"`
	Received                 int64   `json:"received_datagrams"`
	ReceivedBytes            int64   `json:"received_payload_bytes"`
	Forwarded                int64   `json:"forwarded_datagrams"`
	ForwardedBytes           int64   `json:"forwarded_payload_bytes"`
	BandwidthDrops           int64   `json:"bandwidth_drops"`
	BandwidthDropBytes       int64   `json:"bandwidth_drop_payload_bytes"`
	MaxQueuedBytes           int     `json:"max_bandwidth_queued_payload_bytes"`
	ShutdownQueuedBytes      int     `json:"shutdown_bandwidth_queued_payload_bytes"`
	QueueResidenceTotalMS    float64 `json:"queue_residence_total_ms"`
	QueueResidenceMaxMS      float64 `json:"queue_residence_max_ms"`
	SerializationCompletions int64   `json:"serialization_completions"`
	TimerLatenessMaxMS       float64 `json:"timer_lateness_max_ms"`
	TimerLatenessTotalMS     float64 `json:"timer_lateness_total_ms"`
	ForwardingWindowMS       float64 `json:"forwarding_window_ms"`
	ForwardedPayloadKbps     float64 `json:"forwarded_payload_kbps"`
	firstReceivedAt          time.Time
	lastForwardedAt          time.Time
}
type datagram struct {
	data      []byte
	addr      *net.UDPAddr
	at        time.Time
	direction int
}
type queue []datagram

func (q queue) Len() int           { return len(q) }
func (q queue) Less(i, j int) bool { return q[i].at.Before(q[j].at) }
func (q queue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *queue) Push(v any)        { *q = append(*q, v.(datagram)) }
func (q *queue) Pop() any          { a := *q; v := a[len(a)-1]; *q = a[:len(a)-1]; return v }

type proxy struct {
	conn     *net.UDPConn
	done     chan struct{}
	stats    proxyStats
	overflow atomic.Int64
}

func startProxy(ctx context.Context, destination *net.UDPAddr, p profile, seed int64) (*proxy, error) {
	bandwidth, err := newBandwidthQueue(p.BandwidthKbps, p.QueueBytes)
	if err != nil {
		return nil, err
	}
	if p.DelayMS < 0 || p.DelayMS > 60000 || p.JitterMS < 0 || p.JitterMS > 60000 || p.BlackoutMS < 0 || p.BlackoutMS > 60000 || math.IsNaN(p.Loss) || p.Loss < 0 || p.Loss > 1 {
		return nil, errors.New("invalid proxy delay, jitter, loss or outage setting")
	}
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, err
	}
	x := &proxy{conn: c, done: make(chan struct{})}
	x.stats.Directions[0].Direction = "source_to_receiver"
	x.stats.Directions[1].Direction = "receiver_to_source"
	incoming := make(chan datagram, 512)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(incoming)
		buf := make([]byte, 2048)
		for {
			n, _, flags, a, e := c.ReadMsgUDP(buf, nil)
			if e != nil {
				return
			}
			if flags&syscall.MSG_TRUNC != 0 {
				x.overflow.Add(1)
				continue
			}
			b := append([]byte(nil), buf[:n]...)
			select {
			case incoming <- datagram{data: b, addr: a, at: time.Now()}:
			default:
				x.overflow.Add(1)
			}
		}
	}()
	go func() {
		defer func() {
			c.Close()
			<-readerDone
			if bandwidth != nil {
				for i := range bandwidth.directions {
					x.stats.Directions[i].ShutdownQueuedBytes = bandwidth.directions[i].bytes
				}
			}
			close(x.done)
		}()
		r := [2]*prng.Rand{prng.New(prng.NewSource(seed)), prng.New(prng.NewSource(seed + 1_000_003))}
		var caller *net.UDPAddr
		var mediaStart time.Time
		q := queue{}
		forward := func(d datagram) {
			// A stuck socket cannot indefinitely delay cancellation or cleanup.
			_ = c.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
			if _, e := c.WriteToUDP(d.data, d.addr); e == nil {
				x.stats.Forwarded++
				s := &x.stats.Directions[d.direction]
				s.Forwarded++
				s.ForwardedBytes += int64(len(d.data))
				s.lastForwardedAt = time.Now()
			} else {
				x.stats.ResourceDrops++
			}
		}
		timer := time.NewTimer(time.Hour)
		defer timer.Stop()
		for {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			var tick <-chan time.Time
			if bandwidth != nil {
				if due, ok := bandwidth.nextDue(); ok {
					timer.Reset(max(time.Until(due), 0))
					tick = timer.C
				}
			} else if len(q) > 0 {
				timer.Reset(max(time.Until(q[0].at), 0))
				tick = timer.C
			}
			select {
			case <-ctx.Done():
				return
			case d, ok := <-incoming:
				if !ok {
					return
				}
				x.stats.Received++
				dir := 0
				target := destination
				if d.addr.Port == destination.Port {
					dir = 1
					target = caller
					if target == nil {
						continue
					}
				} else {
					caller = d.addr
				}
				d.direction = dir
				s := &x.stats.Directions[dir]
				s.Received++
				s.ReceivedBytes += int64(len(d.data))
				if s.firstReceivedAt.IsZero() {
					s.firstReceivedAt = d.at
				}
				if dir == 0 && len(d.data) >= 16 && d.data[0]&0x80 == 0 && mediaStart.IsZero() {
					mediaStart = d.at
				}
				elapsed := d.at.Sub(mediaStart)
				if p.BlackoutMS > 0 && !mediaStart.IsZero() && elapsed >= 4*time.Second && elapsed < 4*time.Second+time.Duration(p.BlackoutMS)*time.Millisecond {
					x.stats.BlackoutDrops++
					continue
				}
				if r[dir].Float64() < p.Loss {
					x.stats.RandomDrops++
					continue
				}
				j := 0
				if p.JitterMS > 0 {
					j = r[dir].Intn(2*p.JitterMS+1) - p.JitterMS
				}
				d.at = d.at.Add(time.Duration(max(0, p.DelayMS+j)) * time.Millisecond)
				d.addr = target
				if bandwidth != nil {
					switch bandwidth.enqueue(d, time.Now()) {
					case bandwidthRejected:
						x.stats.BandwidthDrops++
						s.BandwidthDrops++
						s.BandwidthDropBytes += int64(len(d.data))
					case bandwidthResourceRejected:
						x.stats.ResourceDrops++
					case bandwidthAdmitted:
						s.MaxQueuedBytes = max(s.MaxQueuedBytes, bandwidth.directions[dir].bytes)
						x.stats.MaxQueued = max(x.stats.MaxQueued, bandwidth.count)
					}
					continue
				}
				if len(q) >= maxProxyQueuedDatagrams {
					x.stats.ResourceDrops++
					continue
				}
				heap.Push(&q, d)
				x.stats.MaxQueued = max(x.stats.MaxQueued, len(q))
			case <-tick:
				if bandwidth != nil {
					for dir := range bandwidth.directions {
						if delivery, ok := bandwidth.take(dir, time.Now()); ok {
							s := &x.stats.Directions[dir]
							residence := float64(delivery.residence) / float64(time.Millisecond)
							s.QueueResidenceTotalMS += residence
							s.QueueResidenceMaxMS = max(s.QueueResidenceMaxMS, residence)
							s.TimerLatenessMaxMS = max(s.TimerLatenessMaxMS, float64(delivery.lateness)/float64(time.Millisecond))
							s.TimerLatenessTotalMS += float64(delivery.lateness) / float64(time.Millisecond)
							s.SerializationCompletions++
							forward(delivery.datagram)
							bandwidth.sent(dir, time.Now())
						}
					}
					continue
				}
				for len(q) > 0 && !q[0].at.After(time.Now()) {
					d := heap.Pop(&q).(datagram)
					forward(d)
				}
			}
		}
	}()
	return x, nil
}
func (p *proxy) finish() proxyStats {
	p.conn.Close()
	<-p.done
	s := p.stats
	s.ResourceDrops += p.overflow.Load()
	for i := range s.Directions {
		d := &s.Directions[i]
		if !d.firstReceivedAt.IsZero() && d.lastForwardedAt.After(d.firstReceivedAt) {
			d.ForwardingWindowMS = float64(d.lastForwardedAt.Sub(d.firstReceivedAt)) / float64(time.Millisecond)
			d.ForwardedPayloadKbps = float64(d.ForwardedBytes) * 8 / d.ForwardingWindowMS
		}
	}
	return s
}

type child struct {
	cmd        *exec.Cmd
	done       chan error
	outputDone chan struct{}
}

func startChild(ctx context.Context, binary string, args []string, logpath, secret string, cap *capture, origin time.Time) (*child, error) {
	if cap != nil && cap.mu == nil {
		cap.mu = &sync.Mutex{}
	}
	log, err := os.OpenFile(logpath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	c := exec.CommandContext(ctx, binary, args...)
	c.WaitDelay = time.Second
	c.Stdin = nil
	stderr, err := c.StderrPipe()
	if err != nil {
		log.Close()
		return nil, err
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		log.Close()
		return nil, err
	}
	if err = c.Start(); err != nil {
		log.Close()
		return nil, err
	}
	x := &child{cmd: c, done: make(chan error, 1), outputDone: make(chan struct{})}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer log.Close()
		s := bufio.NewScanner(stderr)
		s.Buffer(make([]byte, 4096), 65536)
		size := 0
		for s.Scan() {
			line := s.Text()
			if secret != "" {
				line = strings.ReplaceAll(line, secret, "[redacted]")
			}
			if size < 512*1024 {
				n, _ := fmt.Fprintln(log, line)
				size += n
			}
		}
	}()
	go func() {
		defer wg.Done()
		s := bufio.NewScanner(stdout)
		s.Buffer(make([]byte, 4096), 65536)
		for s.Scan() {
			line := s.Text()
			now := float64(time.Since(origin).Microseconds()) / 1000
			if cap == nil {
				continue
			}
			cap.mu.Lock()
			if strings.HasPrefix(line, "#tb 0:") {
				var a, b float64
				if _, e := fmt.Sscanf(line, "#tb 0: %f/%f", &a, &b); e == nil && b > 0 {
					cap.Timebase = a / b
				}
			} else if !strings.HasPrefix(line, "#") {
				parts := strings.Split(line, ",")
				if len(parts) == 6 && len(cap.Frames) < 2048 {
					pts, e := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
					if e == nil {
						cap.Frames = append(cap.Frames, frame{pts, strings.TrimSpace(parts[5]), now})
					}
				}
			}
			cap.mu.Unlock()
		}
	}()
	go func() { wg.Wait(); close(x.outputDone) }()
	go func() { <-x.outputDone; x.done <- c.Wait() }()
	return x, nil
}
func (c *child) stop() {
	if c == nil {
		return
	}
	select {
	case <-c.done:
	case <-time.After(10 * time.Millisecond):
		_ = c.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-c.done:
		case <-time.After(time.Second):
			_ = c.cmd.Process.Kill()
			<-c.done
		}
	}
	<-c.outputDone
}
func snapshot(c *capture) capture {
	c.mu.Lock()
	defer c.mu.Unlock()
	return capture{Frames: append([]frame(nil), c.Frames...), Timebase: c.Timebase}
}
func freePort(network string) (int, error) {
	if network == "udp" {
		c, e := net.ListenPacket("udp4", "127.0.0.1:0")
		if e != nil {
			return 0, e
		}
		defer c.Close()
		return c.LocalAddr().(*net.UDPAddr).Port, nil
	}
	c, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return 0, e
	}
	defer c.Close()
	return c.Addr().(*net.TCPAddr).Port, nil
}
func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}

func fileSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}
func ready(ctx context.Context, api int, paths bool, wanted ...string) error {
	if len(wanted) == 0 {
		wanted = []string{"reference", "impaired"}
	}
	client := http.Client{Timeout: time.Second}
	for {
		u := fmt.Sprintf("http://127.0.0.1:%d/v3/paths/list", api)
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		r, e := client.Do(req)
		if e == nil {
			var v struct {
				Items []struct {
					Name  string
					Ready bool
				}
			}
			e = json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&v)
			r.Body.Close()
			if e == nil && r.StatusCode == 200 {
				if !paths {
					return nil
				}
				n := 0
				for _, p := range v.Items {
					for _, name := range wanted {
						if p.Name == name && p.Ready {
							n++
						}
					}
				}
				if n == len(wanted) {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(30 * time.Millisecond):
		}
	}
}

type trial struct {
	Scoring                    scoringWindow `json:"scoring_window"`
	RelayTransport             string        `json:"relay_transport,omitempty"`
	PublisherSourceType        string        `json:"publisher_source_type,omitempty"`
	Name                       string        `json:"name"`
	LatencyMS                  int           `json:"impaired_latency_ms"`
	ReferenceLatencyMS         int           `json:"reference_latency_ms"`
	NegotiatedReferenceMS      int           `json:"negotiated_reference_ms"`
	NegotiatedImpairedMS       int           `json:"negotiated_impaired_ms"`
	Seed                       int64         `json:"seed"`
	Profile                    profile       `json:"profile"`
	Error                      string        `json:"error,omitempty"`
	Reference                  quality       `json:"reference"`
	Impaired                   quality       `json:"impaired"`
	AddedDelayMS               distribution  `json:"matched_frame_added_delay_ms"`
	Timely250                  int           `json:"timely_250ms"`
	Timely500                  int           `json:"timely_500ms"`
	Timely250Fraction          float64       `json:"timely_250ms_fraction_all_expected"`
	Timely500Fraction          float64       `json:"timely_500ms_fraction_all_expected"`
	TimingConclusive           bool          `json:"timing_comparison_conclusive"`
	Flags                      []string      `json:"flags"`
	Proxy                      proxyStats    `json:"proxy"`
	PublisherFIFOOverflowLines int           `json:"publisher_fifo_overflow_lines"`
}

func decoderArgs(rtsp int, name string) []string {
	return []string{"-hide_banner", "-loglevel", "warning", "-nostdin", "-threads:v", "1", "-rtsp_transport", "tcp", "-analyzeduration", "100000", "-probesize", "100000", "-i", fmt.Sprintf("rtsp://127.0.0.1:%d/%s", rtsp, name), "-map", "0:v:0", "-an", "-c:v", "rawvideo", "-pix_fmt", "yuv420p", "-threads:v", "1", "-fps_mode:v", "passthrough", "-f", "framemd5", "-flush_packets", "1", "pipe:1"}
}
func runTrial(parent context.Context, dir, ffmpeg, mtx, clip string, p profile, latency int, seed int64, reference map[string]int, windows ...scoringWindow) (out trial) {
	out = trial{Name: fmt.Sprintf("%s-%dms-seed%d", p.Name, latency, seed), LatencyMS: latency, ReferenceLatencyMS: 120, Seed: seed, Profile: p}
	w := scoringWindow{FPS: fps, First: firstScored, Last: lastScored}
	if len(windows) > 0 {
		w = windows[0]
	}
	out.Scoring = w
	// Keep the planned denominator even if setup fails before decoding starts.
	// Error/flags distinguish unavailable observations from measured losses.
	out.Reference.Expected = w.Last - w.First
	out.Impaired.Expected = w.Last - w.First
	forward := isRelayTrial(p)
	if forward {
		out.Name = fmt.Sprintf("%s-repeat%d", p.Name, seed)
		out.RelayTransport = "srt"
		if p.Name != "relay-srt120" {
			out.RelayTransport, out.LatencyMS = "rtsp-tcp", 0
		}
	}
	dir = filepath.Join(dir, out.Name)
	if e := os.MkdirAll(dir, 0700); e != nil {
		out.Error = e.Error()
		return
	}
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()
	origin := time.Now()
	var children []*child
	defer func() {
		cancel()
		for _, c := range children {
			c.stop()
		}
	}()
	defer func() { _ = writeJSON(filepath.Join(dir, "summary.json"), out) }()
	fail := func(e error) { out.Error = e.Error() }
	srtport, e := freePort("udp")
	if e != nil {
		fail(e)
		return
	}
	rtsp, e := freePort("tcp")
	if e != nil {
		fail(e)
		return
	}
	api, e := freePort("tcp")
	if e != nil {
		fail(e)
		return
	}
	var key [16]byte
	if _, e = rand.Read(key[:]); e != nil {
		fail(e)
		return
	}
	secret := hex.EncodeToString(key[:])
	config := fmt.Sprintf("logLevel: warn\nrtsp: yes\nrtspAddress: 127.0.0.1:%d\nrtspTransports: [tcp]\nrtmp: no\nhls: no\nwebrtc: no\nsrt: yes\nsrtAddress: 127.0.0.1:%d\napi: yes\napiAddress: 127.0.0.1:%d\npaths:\n  reference:\n    source: publisher\n    srtPublishPassphrase: %s\n  impaired:\n    source: publisher\n    srtPublishPassphrase: %s\n", rtsp, srtport, api, secret, secret)
	if forward {
		config += relayTrialAuthentication(secret)
	}
	configpath := filepath.Join(dir, "private-receiver.yml")
	if e = os.WriteFile(configpath, []byte(config), 0600); e != nil {
		fail(e)
		return
	}
	c, e := startChild(ctx, mtx, []string{configpath}, filepath.Join(dir, "receiver.log"), secret, nil, origin)
	if e != nil {
		fail(e)
		return
	}
	children = append(children, c)
	rctx, rcancel := context.WithTimeout(ctx, 3*time.Second)
	e = ready(rctx, api, false)
	rcancel()
	if e != nil {
		fail(fmt.Errorf("isolated receiver unavailable: %w", e))
		return
	}
	proxyctx, proxycancel := context.WithCancel(ctx)
	defer proxycancel()
	x, e := startProxy(proxyctx, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: srtport}, p, seed)
	if e != nil {
		fail(e)
		return
	}
	defer func() { proxycancel(); out.Proxy = x.finish() }()
	srtURL := func(port, lat int, path string) string {
		q := url.Values{"mode": {"caller"}, "latency": {strconv.Itoa(lat * 1000)}, "streamid": {"publish:" + path}, "passphrase": {secret}, "pbkeylen": {"16"}, "pkt_size": {"1316"}, "conntimeo": {"5000"}}
		if forward {
			q.Set("streamid", "publish:"+path+":benchmark:"+secret)
		}
		return fmt.Sprintf("srt://127.0.0.1:%d?%s", port, q.Encode())
	}
	tee := "[f=mpegts]" + srtURL(srtport, 120, "reference") + "|[f=mpegts]" + srtURL(x.conn.LocalAddr().(*net.UDPAddr).Port, latency, "impaired")
	args := []string{"-hide_banner", "-loglevel", "warning", "-nostdin", "-readrate", "1", "-readrate_initial_burst", "0", "-readrate_catchup", "1", "-i", clip, "-map", "0:v:0", "-an", "-c:v", "copy", "-f", "tee", "-use_fifo", "1", "-fifo_options", "queue_size=30:drop_pkts_on_overflow=1:attempt_recovery=0", tee}
	if forward {
		args = append(args[:len(args)-7], "-f", "mpegts", srtURL(srtport, 120, "reference"))
	}
	publisher, e := startChild(ctx, ffmpeg, args, filepath.Join(dir, "publisher.log"), secret, nil, origin)
	if e != nil {
		fail(e)
		return
	}
	children = append(children, publisher)
	if forward {
		rctx, rcancel = context.WithTimeout(ctx, 4*time.Second)
		e = ready(rctx, api, true, "reference")
		rcancel()
		if e != nil {
			fail(fmt.Errorf("relay source unavailable: %w", e))
			return
		}
		forwardArgs := relayTrialCommand(p, rtsp, secret, srtURL(x.conn.LocalAddr().(*net.UDPAddr).Port, 120, "impaired"))
		relayer, err := startChild(ctx, ffmpeg, forwardArgs, filepath.Join(dir, "relay.log"), secret, nil, origin)
		if err != nil {
			fail(err)
			return
		}
		children = append(children, relayer)
	}
	rctx, rcancel = context.WithTimeout(ctx, 6*time.Second)
	e = ready(rctx, api, true)
	rcancel()
	if e != nil {
		fail(fmt.Errorf("synthetic paths unavailable: %w", e))
		return
	}
	client := http.Client{Timeout: time.Second}
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/v3/srtconns/list", api), nil)
	if response, err := client.Do(req); err == nil {
		var v struct {
			Items []struct {
				Path                string
				MsReceiveTsbPdDelay int
			}
		}
		if json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&v) == nil {
			for _, conn := range v.Items {
				if conn.Path == "reference" {
					out.NegotiatedReferenceMS = conn.MsReceiveTsbPdDelay
				}
				if conn.Path == "impaired" {
					out.NegotiatedImpairedMS = conn.MsReceiveTsbPdDelay
				}
			}
		}
		response.Body.Close()
	}
	if forward {
		out.PublisherSourceType = relayTrialSourceType(ctx, api)
	}
	var refCap, impCap capture
	for _, entry := range []struct {
		name    string
		capture *capture
	}{{"reference", &refCap}, {"impaired", &impCap}} {
		c, e = startChild(ctx, ffmpeg, decoderArgs(rtsp, entry.name), filepath.Join(dir, entry.name+"-decoder.log"), secret, entry.capture, origin)
		if e != nil {
			fail(e)
			return
		}
		children = append(children, c)
	}
	select {
	case err := <-publisher.done:
		if err != nil {
			fail(errors.New("synthetic publisher failed; see redacted log"))
		}
		publisher.done <- err
	case <-ctx.Done():
		fail(ctx.Err())
	}
	select {
	case <-time.After(600 * time.Millisecond):
	case <-ctx.Done():
	}
	cancel()
	for _, child := range children {
		child.stop()
	}
	children = nil
	rc, ic := snapshot(&refCap), snapshot(&impCap)
	proxycancel()
	out.Proxy = x.finish()
	_ = writeJSON(filepath.Join(dir, "reference-frames.json"), rc)
	_ = writeJSON(filepath.Join(dir, "impaired-frames.json"), ic)
	out.Reference = scoreWindow(rc, reference, w)
	out.Impaired = scoreWindow(ic, reference, w)
	var delays []float64
	for i, at := range out.Impaired.Times {
		if baseline, ok := out.Reference.Times[i]; ok {
			d := at - baseline
			delays = append(delays, d)
			if d <= 250 {
				out.Timely250++
			}
			if d <= 500 {
				out.Timely500++
			}
		}
	}
	out.AddedDelayMS = dist(delays)
	out.Timely250Fraction = float64(out.Timely250) / float64(w.Last-w.First)
	out.Timely500Fraction = float64(out.Timely500) / float64(w.Last-w.First)
	logs, _ := os.ReadFile(filepath.Join(dir, "publisher.log"))
	for _, line := range strings.Split(string(logs), "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "fifo") && (strings.Contains(lower, "overflow") || strings.Contains(lower, "queue full")) {
			out.PublisherFIFOOverflowLines++
		}
	}
	if out.Reference.Exact != out.Reference.Expected {
		out.Flags = append(out.Flags, "clean reference lost or changed scored frames")
	}
	if !out.Reference.MappingConsistent || !out.Impaired.MappingConsistent {
		out.Flags = append(out.Flags, "source-index mapping unavailable or inconsistent")
	}
	if out.Reference.ArrivalGapsMS.Max > 200 {
		out.Flags = append(out.Flags, "clean reference gap exceeded200ms")
	}
	if out.Reference.MaxScheduleDriftMS > 150 {
		out.Flags = append(out.Flags, "clean reference pacing drift exceeded150ms")
	}
	if out.Proxy.ResourceDrops > 0 {
		out.Flags = append(out.Flags, "bounded proxy queue or socket dropped packets")
	}
	if p.BandwidthKbps > 0 {
		for _, direction := range out.Proxy.Directions {
			if direction.TimerLatenessMaxMS > 25 {
				out.Flags = append(out.Flags, direction.Direction+" bandwidth scheduler was over25ms late")
			}
		}
	}
	if out.PublisherFIFOOverflowLines > 0 {
		out.Flags = append(out.Flags, "publisher FIFO overflow; source release may be affected")
	}
	if out.NegotiatedReferenceMS != 120 || (out.RelayTransport != "rtsp-tcp" && out.NegotiatedImpairedMS != latency) {
		out.Flags = append(out.Flags, "negotiated latency unavailable or different from requested")
	}
	if forward && ((out.RelayTransport == "srt" && out.PublisherSourceType != "srtConn") || (out.RelayTransport == "rtsp-tcp" && out.PublisherSourceType != "rtspSession")) {
		out.Flags = append(out.Flags, "receiver publisher protocol differs from requested relay transport")
	}
	out.TimingConclusive = len(out.Flags) == 0 && out.Error == ""
	return
}

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "netcheck:", e)
		os.Exit(1)
	}
}
func run() error {
	root := flag.String("root", ".", "project root")
	ffmpeg := flag.String("ffmpeg", "/opt/homebrew/bin/ffmpeg", "FFmpeg executable")
	mtx := flag.String("mediamtx", "", "corrected receiver executable (defaults to selected binary)")
	only := flag.String("only", "", "optional profile name for a smoke trial")
	seedsFlag := flag.String("seeds", "17,41", "comma-separated PRNG seeds")
	latFlag := flag.String("latencies", "120,300", "impaired-side latency settings in milliseconds; experimental60/80 require a receiver with a lower minimum")
	relayCompare := flag.Bool("relay-compare", false, "compare isolated copy forwarding via SRT120 and authenticated local RTSP/TCP")
	bandwidthCompare := flag.Bool("bandwidth-compare", false, "compare generated encodings sent in real time over isolated bandwidth-limited connections")
	rates := flag.String("rates", "0,1500,900", "bandwidth comparison only: UDP payload kilobits/second in each direction; 0 means unlimited")
	queueBytes := flag.Int("queue-bytes", 32768, "bandwidth comparison only: finite packet queue bytes in each direction")
	encodings := flag.String("encodings", "copy,detail20,small20", "bandwidth comparison only: comma-separated generated encodings")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	var bandwidthOptions bandwidthOptions
	if *bandwidthCompare {
		var conflicts []string
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "latencies" || f.Name == "only" || f.Name == "relay-compare" {
				conflicts = append(conflicts, "--"+f.Name)
			}
		})
		if len(conflicts) > 0 {
			return fmt.Errorf("bandwidth comparison uses a fixed 120 ms allowance; incompatible flags: %s", strings.Join(conflicts, ", "))
		}
		var err error
		bandwidthOptions, err = parseBandwidthOptions(*rates, *queueBytes, *encodings, *seedsFlag)
		if err != nil {
			return err
		}
	} else {
		var unexpected string
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "rates" || f.Name == "queue-bytes" || f.Name == "encodings" {
				unexpected = f.Name
			}
		})
		if unexpected != "" {
			return fmt.Errorf("--%s requires --bandwidth-compare", unexpected)
		}
	}
	sourceFrames := 360
	if *relayCompare {
		firstScored, lastScored, sourceFrames = 180, 510, 540
	}
	abs, e := filepath.Abs(*root)
	if e != nil {
		return e
	}
	if *mtx == "" {
		*mtx = filepath.Join(abs, ".tools", "mediamtx-active", "mediamtx")
	}
	version, e := exec.Command(*mtx, "--version").Output()
	if e != nil {
		return errors.New("selected receiver unavailable")
	}
	if !strings.Contains(string(version), "clockfix") {
		return errors.New("benchmark requires corrected receiver version")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *bandwidthCompare {
		return runBandwidth(ctx, abs, *ffmpeg, *mtx, strings.TrimSpace(string(version)), bandwidthOptions)
	}
	dir := filepath.Join(abs, ".local", "diagnostics", "netcheck-"+time.Now().UTC().Format("20060102T150405.000Z"))
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	fmt.Println("Evidence:", dir)
	clip := filepath.Join(dir, "source.mp4")
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30", "-frames:v", strconv.Itoa(sourceFrames), "-an", "-c:v", "libx264", "-threads:v", "1", "-preset", "ultrafast", "-tune", "zerolatency", "-profile:v", "baseline", "-pix_fmt", "yuv420p", "-bf", "0", "-g", "30", "-keyint_min", "30", "-sc_threshold", "0", "-b:v", "2000k", "-maxrate", "2000k", "-bufsize", "1000k", clip}
	c, e := startChild(ctx, *ffmpeg, args, filepath.Join(dir, "encode.log"), "", nil, time.Now())
	if e != nil {
		return e
	}
	e = <-c.done
	<-c.outputDone
	if e != nil {
		return errors.New("reference encoding failed")
	}
	var cap capture
	args = []string{"-hide_banner", "-loglevel", "warning", "-nostdin", "-threads:v", "1", "-i", clip, "-map", "0:v:0", "-an", "-c:v", "rawvideo", "-pix_fmt", "yuv420p", "-threads:v", "1", "-fps_mode:v", "passthrough", "-f", "framemd5", "-flush_packets", "1", "pipe:1"}
	c, e = startChild(ctx, *ffmpeg, args, filepath.Join(dir, "offline-decode.log"), "", &cap, time.Now())
	if e != nil {
		return e
	}
	e = <-c.done
	<-c.outputDone
	if e != nil {
		return errors.New("reference decode failed")
	}
	ref := snapshot(&cap)
	if len(ref.Frames) != sourceFrames {
		return fmt.Errorf("reference has %d frames, expected %d", len(ref.Frames), sourceFrames)
	}
	hashes := map[string]int{}
	for i, f := range ref.Frames {
		if _, ok := hashes[f.Hash]; ok {
			return errors.New("source contains repeated hashes; frame identity ambiguous")
		}
		hashes[f.Hash] = i
	}
	if e = writeJSON(filepath.Join(dir, "offline-reference.json"), ref); e != nil {
		return e
	}
	parseInts := func(s string) ([]int, error) {
		var a []int
		for _, v := range strings.Split(s, ",") {
			n, e := strconv.Atoi(v)
			if e != nil {
				return nil, e
			}
			a = append(a, n)
		}
		return a, nil
	}
	seeds, e := parseInts(*seedsFlag)
	if e != nil {
		return e
	}
	latencies, e := parseInts(*latFlag)
	if e != nil {
		return e
	}
	for _, l := range latencies {
		if l != 60 && l != 80 && l != 120 && l != 300 {
			return errors.New("latency must be60,80,120 or300; values below120 require a receiver with a lower minimum")
		}
	}
	profiles := []profile{{Name: "clean"}, {Name: "delay20-loss1", DelayMS: 20, JitterMS: 10, Loss: .01}, {Name: "delay60-loss1", DelayMS: 60, JitterMS: 20, Loss: .01}, {Name: "blackout200", BlackoutMS: 200}}
	if *relayCompare {
		profiles = []profile{{Name: "relay-srt120"}, {Name: "relay-rtsp"}, {Name: "relay-rtsp-flush"}}
		latencies = []int{120}
	}
	metadata := map[string]any{"receiver_version": strings.TrimSpace(string(version)), "fps": fps, "source_frames": 360, "first_scored_inclusive": firstScored, "last_scored_exclusive": lastScored, "reference_latency_ms": 120, "queue_limit_datagrams": 4096, "incoming_channel_limit": 512, "max_datagram_bytes": 2048, "tee_fifo_queue_frames": 30, "impairment": "Both directions; independent uniform jitter and Bernoulli loss affect all UDP datagrams including handshake/control. Blackout drops both directions for200ms starting4s after first original media packet. PRNG sequence is seeded; OS packet scheduling is not deterministic.", "timing": "Same-process monotonic decoded frame arrival difference against simultaneously published fixed120ms clean reference. Includes receiver/decoder/host scheduling; not absolute source age or camera-to-browser latency. Negative differences retained.", "quality": "Every original encoded-then-decoded source frame in middle270-frame window is denominator. Nonmatching includes corruption/concealment; missing means no matching or nonmatching output assigned to that position. Unknown outputs use PTS mapping inferred from exact matches; inconsistent mappings invalidate timing.", "timeliness": "Exact impaired frame arrival minus exact clean reference arrival <=250/500ms; denominator all270expected, missing/nonmatching/unpaired frames never timely."}
	metadata["source_sha256"] = fileSHA256(clip)
	metadata["receiver_sha256"] = fileSHA256(*mtx)
	metadata["harness_source_sha256"] = fileSHA256(filepath.Join(abs, "cmd/netcheck/main.go"))
	metadata["publisher_readrate"] = 1
	metadata["publisher_readrate_initial_burst"] = 0
	metadata["publisher_readrate_catchup"] = 1
	metadata["source_encoding"] = "12s testsrc2 1280x720 30fps, libx264 single thread ultrafast zerolatency Baseline yuv420p, no Bframes, keyframe every30frames, 2Mbps maxrate, 1Mbit buffer"
	metadata["continuity"] = "arrival_gaps_ms counts all decoded outputs, including nonmatching pixels. intact_arrival_gaps_ms counts unique exact source frames only. longest_nonintact_run_source_ms includes head/tail of scored window and measures absent/changed source-frame duration, not observed wall-clock pause."
	metadata["comparison_gates"] = "Timing inconclusive if reference is not270/270 exact, either PTS mapping inconsistent, reference output gap>200ms or reference pacing drift>150ms, publisher FIFO overflow, proxy resource drop, or negotiated latency mismatch. Missing/nonmatching classification by source position requires consistent PTS mapping; exact hash identities remain valid regardless."
	if *relayCompare {
		relayTrialMetadata(metadata, sourceFrames)
	}
	_ = writeJSON(filepath.Join(dir, "parameters.json"), metadata)
	var results []trial
	for _, p := range profiles {
		if *only != "" && p.Name != *only {
			continue
		}
		for _, seed := range seeds {
			for _, lat := range latencies {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				r := runTrial(ctx, dir, *ffmpeg, *mtx, clip, p, lat, int64(seed), hashes)
				results = append(results, r)
				_ = writeJSON(filepath.Join(dir, "results.json"), results)
				fmt.Printf("%s: exact%d/%d missing%d changed%d timely250 %.1f%% gap%.1fms deltaP95 %.1fms valid=%v error=%s\n", r.Name, r.Impaired.Exact, r.Impaired.Expected, r.Impaired.Missing, r.Impaired.Nonmatching, 100*r.Timely250Fraction, r.Impaired.ArrivalGapsMS.Max, r.AddedDelayMS.P95, r.TimingConclusive, r.Error)
			}
		}
	}
	if len(results) == 0 {
		return errors.New("no profiles selected")
	}
	return nil
}
