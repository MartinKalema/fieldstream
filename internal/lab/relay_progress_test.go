package lab

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRelayProgressParsesPartialLinesAndDrainsOversizedLines(t *testing.T) {
	now := time.Unix(1000, 0)
	var log bytes.Buffer
	p := newRelayProgress(&log, func() time.Time { return now })
	chunks := []string{"frame=1\nout_t", "ime_us=", "1000000\r", "\nprogress=continue\n"}
	now = now.Add(time.Second)
	for _, chunk := range chunks {
		if n, err := p.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatal("progress writer did not drain stdout")
		}
	}
	if p.outTimeUS != 1_000_000 || !p.lastAdvance.Equal(now) || log.String() != strings.Join(chunks, "") {
		t.Fatal("partial progress line was lost or logging changed")
	}
	advance := p.lastAdvance
	now = now.Add(time.Second)
	_, _ = p.Write([]byte("out_time_us=N/A\nout_time_us=1000000\nout_time_us=0\nout_time_us=-3\nout_time_us=9999999999999999999999999\nout_time_ms=2000000\n"))
	if !p.lastAdvance.Equal(advance) {
		t.Fatal("missing, invalid or repeated timestamp counted as progress")
	}
	_, _ = p.Write([]byte(strings.Repeat("x", 10*relayProgressLineLimit) + "out_time_us=3000000\n"))
	if p.outTimeUS != 1_000_000 || p.lineLen != 0 || p.dropping {
		t.Fatal("oversized line was accepted or prevented recovery at the next newline")
	}
	_, _ = p.Write([]byte("out_time_us=2000000\n"))
	if p.outTimeUS != 2_000_000 || !p.lastAdvance.Equal(now) {
		t.Fatal("valid progress after oversized line was lost")
	}
}

type unavailableProgressLog struct{}

func (unavailableProgressLog) Write([]byte) (int, error) { return 0, errors.New("unavailable") }

func TestRelayProgressLoggingFailureDoesNotStopDrain(t *testing.T) {
	p := newRelayProgress(unavailableProgressLog{}, time.Now)
	data := []byte("out_time_us=1\n")
	if n, err := p.Write(data); n != len(data) || err != nil || p.outTimeUS != 1 {
		t.Fatal("logging failure backed up progress or discarded its observation")
	}
}

func TestRelayProgressStartupGraceAndGenuineStall(t *testing.T) {
	now := time.Unix(1000, 0)
	start := now
	p := newRelayProgress(io.Discard, func() time.Time { return now })
	for second := 0; second <= 13; second++ {
		now = start.Add(time.Duration(second) * time.Second)
		reason := p.reason(SourceState{Observed: true, Ready: true, PublisherID: "first", BytesReceived: uint64(100 + second)}, now)
		if second < 13 && reason != "" {
			t.Fatalf("restarted during startup/input grace at second %d", second)
		}
		if second == 13 && !strings.Contains(reason, "no output progress") {
			t.Fatal("continuing input with no output did not trigger recovery")
		}
	}
}

func TestRelayProgressRequiresFreshObservedInput(t *testing.T) {
	for _, mode := range []string{"unchanging bytes", "observation unavailable", "source offline", "input stops"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Unix(1000, 0)
			start := now
			p := newRelayProgress(io.Discard, func() time.Time { return now })
			for second := 0; second < 30; second++ {
				now = start.Add(time.Duration(second) * time.Second)
				source := SourceState{Observed: true, Ready: true, BytesReceived: 100}
				switch mode {
				case "observation unavailable":
					source.Observed = false
					source.BytesReceived += uint64(second)
				case "source offline":
					source.Ready = false
					source.BytesReceived += uint64(second)
				case "input stops":
					source.BytesReceived += uint64(min(second, 10))
					if second <= 10 {
						fmt.Fprintf(p, "out_time_us=%d\n", second*1_000_000)
					}
				}
				if reason := p.reason(source, now); reason != "" {
					t.Fatalf("%s caused an unrelated forwarder restart: %s", mode, reason)
				}
			}
		})
	}
}

func TestRelayProgressSlowTrickleTriggersAfterGrace(t *testing.T) {
	now := time.Unix(1000, 0)
	start := now
	p := newRelayProgress(io.Discard, func() time.Time { return now })
	var reason string
	for second := 0; second <= 17; second++ {
		now = start.Add(time.Duration(second) * time.Second)
		if second <= 13 {
			fmt.Fprintf(p, "out_time_us=%d\n", second*1_000_000)
		} else if second == 16 {
			// A blocked write can return after a timeout while FFmpeg ignores
			// its error, advancing only one frame instead of catching up.
			fmt.Fprint(p, "out_time_us=13033333\n")
		}
		reason = p.reason(SourceState{Observed: true, Ready: true, BytesReceived: uint64(100 + second)}, now)
		if second < 17 && reason != "" {
			t.Fatalf("healthy cadence or allowed three-second lag triggered: %s", reason)
		}
	}
	if !strings.Contains(reason, "fell behind") || now.Sub(p.lastAdvance) != time.Second {
		t.Fatal("recent but very slow output progress did not trigger recovery")
	}
}

func TestRelayProgressHealthyCadenceAndStartupCatchup(t *testing.T) {
	now := time.Unix(1000, 0)
	start := now
	p := newRelayProgress(io.Discard, func() time.Time { return now })
	for second := 0; second <= 60; second++ {
		now = start.Add(time.Duration(second) * time.Second)
		// First frame timestamps need not start at zero; startup probing can
		// also release a backlog. Neither is evidence of growing delay.
		out := second + 10
		if second >= 5 {
			out += 2
		}
		fmt.Fprintf(p, "out_time_us=%d\n", out*1_000_000)
		if reason := p.reason(SourceState{Observed: true, Ready: true, BytesReceived: uint64(100 + second)}, now); reason != "" {
			t.Fatalf("healthy media-time cadence triggered recovery: %s", reason)
		}
	}
}

func TestRelayProgressInputRestartResetsEvidence(t *testing.T) {
	for _, reset := range []string{"counter falls", "publisher changes with higher counter", "offline", "status unavailable", "activity resumes"} {
		t.Run(reset, func(t *testing.T) {
			now := time.Unix(1000, 0)
			start := now
			p := newRelayProgress(io.Discard, func() time.Time { return now })
			for second := 0; second <= 28; second++ {
				now = start.Add(time.Duration(second) * time.Second)
				source := SourceState{Observed: true, Ready: true, PublisherID: "first", BytesReceived: uint64(100 + second)}
				if second < 15 {
					fmt.Fprintf(p, "out_time_us=%d\n", second*1_000_000)
				}
				switch reset {
				case "counter falls":
					if second >= 15 {
						source.BytesReceived = uint64(second - 15)
					}
				case "publisher changes with higher counter":
					if second >= 15 {
						source.PublisherID = "second"
					}
				case "offline":
					source.Ready = second != 14
				case "status unavailable":
					source.Observed = second != 14
				case "activity resumes":
					if second >= 10 && second <= 15 {
						source.BytesReceived = 110
					}
				}
				reason := p.reason(source, now)
				if second < 28 && reason != "" {
					t.Fatalf("input transition did not grant fresh grace at %d: %s", second, reason)
				}
				if second == 28 && reason == "" {
					t.Fatal("grace permanently disabled recovery after input resumed")
				}
			}
		})
	}
}

func TestSourceObservationIncludesPublisherIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ready":true,"bytesReceived":200,"source":{"type":"srtConn","id":"publisher-instance"}}`)
	}))
	defer server.Close()
	got := sourceState(Ports{API: server.Listener.Addr().(*net.TCPAddr).Port})
	if !got.Observed || !got.Ready || got.PublisherID != "publisher-instance" || got.BytesReceived != 200 {
		t.Fatal("publisher identity needed to detect reconnects was lost")
	}
}

func TestWorkerTracksProgressOnlyForLocalRelayAndResetsOnStart(t *testing.T) {
	program, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	p := NewPaths(t.TempDir())
	for _, tc := range []struct {
		name, key string
		tracked   bool
	}{{"relay/camera-01", "copy/local", true}, {"relay/camera-02", "small/local", true}, {"relay/camera-01", "detail/local", true}, {"relay/camera-01", "detail/srt/300ms", false}, {"relay/camera-01", "copy/srt/120ms", false}, {"recorder/camera-01", "camera-01", false}, {"demo/camera-01", "camera-01", false}} {
		w := &worker{name: tc.name}
		defer w.stop()
		build := func() ([]string, error) { return []string{program}, nil }
		if err := w.ensure(p, true, tc.key, build); err != nil {
			t.Fatal(err)
		}
		first := w.progress
		if (first != nil) != tc.tracked {
			t.Fatalf("wrong progress tracking for %s/%s", tc.name, tc.key)
		}
		w.stop()
		w.next = time.Time{}
		if err := w.ensure(p, true, tc.key, build); err != nil {
			t.Fatal(err)
		}
		if tc.tracked && (w.progress == nil || w.progress == first || w.progress.outTimeUS != 0) {
			t.Fatal("new forwarder inherited the previous process's progress")
		}
		w.stop()
	}
}

func TestRelayProgressConcurrentOutputAndObservations(t *testing.T) {
	p := newRelayProgress(io.Discard, time.Now)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i < 1000; i++ {
			fmt.Fprintf(p, "out_time_us=%d\n", i)
		}
	}()
	for i := 0; i < 1000; i++ {
		_ = p.reason(SourceState{Observed: true, Ready: true, BytesReceived: uint64(i)}, time.Now())
	}
	wg.Wait()
}
