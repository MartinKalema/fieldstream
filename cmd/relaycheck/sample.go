package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"fieldvideolab/internal/lab"
)

// Deliberately separate from lab.State: that type includes a private controller
// token. No settings, raw status, URLs, process arguments or log text are saved.
type snapshot struct {
	UpdatedAt         time.Time `json:"controller_updated_at"`
	Profile           string    `json:"profile"`
	RelayLink         string    `json:"relay_link"`
	RelayStarts       int       `json:"relay_starts"`
	SourcePublisher   string    `json:"source_publisher_id"`
	RemotePublisher   string    `json:"remote_publisher_id"`
	SourceBytes       uint64    `json:"source_received_payload_bytes"`
	RemoteBytes       uint64    `json:"forwarded_received_payload_bytes"`
	Relay             process   `json:"relay"`
	SupervisorPID     int       `json:"supervisor_pid"`
	SupervisorStarted string    `json:"supervisor_started"`
}

// Status currently represents an absent API byte counter as zero. Verify the
// required fields in a second bounded observation; absence must not look like
// a legitimate zero. Keep the counters returned by Paths.Status for the sample.
func verifyCounter(ctx context.Context, port int, source string, observed lab.SourceState) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/v3/paths/get/%s", port, source), nil)
	if err != nil {
		return errors.New("counter observation unavailable")
	}
	client := &http.Client{Timeout: 500 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("counter observation unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("counter observation unavailable")
	}
	var value struct {
		Bytes     *uint64 `json:"bytesReceived"`
		Ready     *bool   `json:"ready"`
		Online    *bool   `json:"online"`
		Available *bool   `json:"available"`
		Source    *struct {
			ID string `json:"id"`
		} `json:"source"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&value) != nil || value.Bytes == nil || value.Source == nil {
		return errors.New("counter fields unavailable")
	}
	ready := value.Ready
	if ready == nil {
		ready = value.Online
	}
	if ready == nil {
		ready = value.Available
	}
	if ready == nil || !*ready || value.Source.ID != observed.PublisherID || *value.Bytes < observed.BytesReceived {
		return errors.New("counter identity changed")
	}
	return nil
}

func makeSnapshot(ctx context.Context, root, source string) (snapshot, error) {
	state := lab.NewPaths(root).Status()
	stream, ok := state.Sources[source]
	age := time.Since(state.UpdatedAt)
	if !state.Running || state.SupervisorPID <= 0 || state.HealthNote != "" || state.UpdatedAt.IsZero() || age < -time.Second || age > 5*time.Second {
		return snapshot{}, errors.New("controller status unavailable or stale")
	}
	if !ok || !stream.RelayEnabled || !stream.Source.Observed || !stream.Source.Ready || !stream.Remote.Observed || !stream.Remote.Ready {
		return snapshot{}, errors.New("source or forwarding is not ready")
	}
	worker, ok := stream.Workers["relay"]
	if !ok || !worker.Running || worker.PID <= 0 || worker.Starts <= 0 {
		return snapshot{}, errors.New("relay worker unavailable")
	}
	if !identityPattern.MatchString(stream.Source.PublisherID) || !identityPattern.MatchString(stream.Remote.PublisherID) || !labelPattern.MatchString(stream.Profile) || !labelPattern.MatchString(stream.RelayLink) {
		return snapshot{}, errors.New("stream identity or settings unavailable")
	}
	checks := make(chan error, 2)
	go func() { checks <- verifyCounter(ctx, lab.Field.API, source, stream.Source) }()
	go func() { checks <- verifyCounter(ctx, lab.Central.API, source, stream.Remote) }()
	for range 2 {
		if err := <-checks; err != nil {
			return snapshot{}, err
		}
	}
	relay, supervisor, err := readProcesses(ctx, worker.PID, state.SupervisorPID)
	if err != nil {
		return snapshot{}, err
	}
	return snapshot{
		UpdatedAt: state.UpdatedAt, Profile: stream.Profile, RelayLink: stream.RelayLink,
		RelayStarts: worker.Starts, SourcePublisher: stream.Source.PublisherID, RemotePublisher: stream.Remote.PublisherID,
		SourceBytes: stream.Source.BytesReceived, RemoteBytes: stream.Remote.BytesReceived,
		Relay: relay, SupervisorPID: supervisor.PID, SupervisorStarted: supervisor.Started,
	}, nil
}

type sample struct {
	At             time.Time `json:"at"`
	ElapsedSeconds float64   `json:"elapsed_seconds"`
	ReadSeconds    float64   `json:"read_seconds"`
	Value          *snapshot `json:"value"`
	Unavailable    string    `json:"unavailable_reason,omitempty"`
	monotonic      time.Time
}

type measurements struct {
	WindowSeconds         float64 `json:"window_seconds"`
	RelayCPUPercent       float64 `json:"relay_cpu_percent_one_core_is_100"`
	RelayRSSMeanKiB       float64 `json:"relay_rss_sample_mean_kib"`
	RelayRSSMaxKiB        uint64  `json:"relay_rss_max_kib"`
	ForwardedPayloadKbps  float64 `json:"forwarded_received_payload_kbps"`
	ForwardedPayloadBytes uint64  `json:"forwarded_received_payload_bytes"`
}

func summarize(samples []sample) (*measurements, string) {
	if len(samples) < 2 {
		return nil, "fewer than two complete observations"
	}
	var first, previous *snapshot
	var previousAt time.Time
	var rss float64
	var maxRSS uint64
	for _, item := range samples {
		if item.Value == nil || item.Unavailable != "" {
			return nil, "one or more observations were unavailable"
		}
		v := item.Value
		if item.ReadSeconds < 0 || item.ReadSeconds > 2 || item.monotonic.IsZero() {
			return nil, "observation timing was unavailable"
		}
		if previous != nil {
			gap := item.monotonic.Sub(previousAt)
			if gap <= 0 || gap > 2500*time.Millisecond {
				return nil, "observations had a timing gap"
			}
			if v.Relay.PID != first.Relay.PID || v.Relay.Started != first.Relay.Started || v.RelayStarts != first.RelayStarts || v.SupervisorPID != first.SupervisorPID || v.SupervisorStarted != first.SupervisorStarted {
				return nil, "a process restarted during the observation window"
			}
			if v.Profile != first.Profile || v.RelayLink != first.RelayLink || v.SourcePublisher != first.SourcePublisher || v.RemotePublisher != first.RemotePublisher {
				return nil, "stream settings or publisher identity changed"
			}
			if v.Relay.CPUSeconds < previous.Relay.CPUSeconds || v.RemoteBytes < previous.RemoteBytes || v.SourceBytes < previous.SourceBytes {
				return nil, "a cumulative counter moved backwards"
			}
			if v.UpdatedAt.Before(previous.UpdatedAt) {
				return nil, "controller time moved backwards"
			}
		} else {
			first = v
		}
		age := item.At.Sub(v.UpdatedAt)
		if v.UpdatedAt.IsZero() || age < -time.Second || age > 5*time.Second {
			return nil, "controller status was stale"
		}
		rss += float64(v.Relay.RSSKiB)
		if v.Relay.RSSKiB > maxRSS {
			maxRSS = v.Relay.RSSKiB
		}
		previous, previousAt = v, item.monotonic
	}
	window := samples[len(samples)-1].monotonic.Sub(samples[0].monotonic).Seconds()
	bytes := previous.RemoteBytes - first.RemoteBytes
	return &measurements{WindowSeconds: window,
		RelayCPUPercent: 100 * (previous.Relay.CPUSeconds - first.Relay.CPUSeconds) / window,
		RelayRSSMeanKiB: rss / float64(len(samples)), RelayRSSMaxKiB: maxRSS,
		ForwardedPayloadKbps: float64(bytes) * 8 / window / 1000, ForwardedPayloadBytes: bytes,
	}, ""
}
