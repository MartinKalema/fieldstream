package lab

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ANNOUNCE checks publication permission before either test path is occupied.
// The successful control uses the identical SDP and closes before any media is
// sent. Do not expose request headers, which contain test credentials.
func integrationRTSPAnnounce(ctx context.Context, user, password string) (int, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	address := fmt.Sprintf("127.0.0.1:%d", Central.RTSP)
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return 0, false, errors.New("could not connect to the private RTSP receiver")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	sdp := "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=Private integration check\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=fmtp:96 packetization-mode=1\r\na=control:trackID=0\r\n"
	authorization := ""
	if user != "" {
		authorization = "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password)) + "\r\n"
	}
	request := fmt.Sprintf("ANNOUNCE rtsp://%s/%s RTSP/1.0\r\nCSeq: 1\r\nUser-Agent: field-video-integration\r\n%sContent-Type: application/sdp\r\nContent-Length: %d\r\n\r\n%s", address, integrationFirstSource, authorization, len(sdp), sdp)
	if _, err := io.WriteString(conn, request); err != nil {
		return 0, false, errors.New("could not send the private RTSP permission check")
	}
	reader := textproto.NewReader(bufio.NewReader(io.LimitReader(conn, 8192)))
	line, err := reader.ReadLine()
	if err != nil {
		return 0, false, errors.New("RTSP permission check did not return a response")
	}
	parts := strings.Fields(line)
	if len(parts) < 2 || parts[0] != "RTSP/1.0" {
		return 0, false, errors.New("RTSP permission check returned an invalid status")
	}
	status, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, false, errors.New("RTSP permission check returned an invalid code")
	}
	headers, err := reader.ReadMIMEHeader()
	if err != nil {
		return 0, false, errors.New("RTSP permission check returned invalid headers")
	}
	return status, headers.Get("Www-Authenticate") != "", nil
}

func (r *integrationRun) rejectUnauthorizedRTSPPublisher(ctx context.Context) error {
	for _, attempt := range []struct {
		name, user, password string
		status               int
	}{
		{"Anonymous RTSP publication receives an authentication challenge on an unused path", "", "", 401},
		{"Wrong RTSP publishing password is rejected on an unused path", r.settings.RelayUser, "incorrect-" + r.settings.RelayPassword, 401},
		{"Correct RTSP publishing credentials accept the same ANNOUNCE request", r.settings.RelayUser, r.settings.RelayPassword, 200},
	} {
		status, challenge, err := integrationRTSPAnnounce(ctx, attempt.user, attempt.password)
		// An anonymous request must receive a login challenge. A supplied but
		// wrong password is rejected with 401 without necessarily repeating it.
		valid := err == nil && status == attempt.status && (attempt.user != "" || challenge)
		evidence := map[string]any{"status_code": status, "expected_status_code": attempt.status, "authentication_challenge": challenge}
		if err != nil {
			evidence["detail"] = err.Error()
		}
		if err := r.check(attempt.name, valid, evidence); err != nil {
			return err
		}
	}
	return nil
}

type integrationPublishKind struct {
	RTSPPublishers int  `json:"rtsp_publishers"`
	RTSPUsesTCP    bool `json:"rtsp_uses_tcp"`
	SRTPublishers  int  `json:"srt_publishers"`
	SRTWaitMS      int  `json:"srt_negotiated_wait_ms"`
}

func integrationPublisherKind(ctx context.Context, sourceID string) (integrationPublishKind, error) {
	var result integrationPublishKind
	for _, protocol := range []string{"rtsp", "srt"} {
		endpoint := "rtspsessions"
		if protocol == "srt" {
			endpoint = "srtconns"
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/v3/%s/list", Central.API, endpoint), nil)
		if err != nil {
			return result, errors.New("could not prepare the publishing connection check")
		}
		response, err := apiClient.Do(request)
		if err != nil {
			return result, errors.New("publishing connection status is unavailable")
		}
		// Whitelist fields: API connection query strings can contain passwords.
		var payload struct {
			Items []struct {
				Path      string `json:"path"`
				State     string `json:"state"`
				Transport string `json:"transport"`
				WaitMS    int    `json:"msReceiveTsbPdDelay"`
			} `json:"items"`
		}
		err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || err != nil {
			return result, errors.New("publishing connection status is invalid")
		}
		for _, item := range payload.Items {
			if item.Path != sourceID || item.State != "publish" {
				continue
			}
			if protocol == "rtsp" {
				result.RTSPPublishers++
				result.RTSPUsesTCP = strings.EqualFold(item.Transport, "tcp")
			} else {
				result.SRTPublishers++
				result.SRTWaitMS = item.WaitMS
			}
		}
	}
	return result, nil
}

func (r *integrationRun) waitRelayLink(ctx context.Context, name, link string) error {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	var observed integrationPublishKind
	for ctx.Err() == nil {
		current, err := integrationPublisherKind(ctx, integrationFirstSource)
		observed = current
		matches := current.RTSPPublishers == 1 && current.RTSPUsesTCP && current.SRTPublishers == 0
		if link == "srt" {
			matches = current.SRTPublishers == 1 && current.SRTWaitMS == 300 && current.RTSPPublishers == 0
		}
		if err == nil && matches {
			return r.check(name, true, map[string]any{"connections": observed})
		}
		if integrationSleep(ctx, 200*time.Millisecond) != nil {
			break
		}
	}
	return r.check(name, false, map[string]any{"connections": observed, "requested_link": link})
}

func integrationCaptureWorkersUnchanged(before, after State) bool {
	for _, name := range []string{"demo", "recorder"} {
		a, b := before.Workers[name], after.Workers[name]
		if !a.Running || !b.Running || a.PID <= 1 || a.PID != b.PID || a.Starts != b.Starts {
			return false
		}
	}
	return after.Source.Observed && after.Source.Ready && after.Recording.Running && after.Source.BytesReceived > before.Source.BytesReceived && after.Recording.Bytes >= before.Recording.Bytes
}

func (r *integrationRun) relayLinkIsolation(ctx context.Context, name, expectedLink string, before State) error {
	after, err := r.status(ctx)
	if err != nil {
		return err
	}
	a, b := integrationSelectSource(before, integrationFirstSource), integrationSelectSource(after, integrationFirstSource)
	x, y := integrationSelectSource(before, integrationSecondSource), integrationSelectSource(after, integrationSecondSource)
	valid := b.RelayLink == expectedLink && integrationCaptureWorkersUnchanged(a, b) && integrationSameWorkers(x, y) && integrationCaptureWorkersUnchanged(x, y) && y.RelayLink == "srt" && y.Remote.Observed && y.Remote.Ready && y.Remote.BytesReceived > x.Remote.BytesReceived
	return r.check(name, valid, map[string]any{
		"camera_01_before": integrationStateEvidence(a), "camera_01_after": integrationStateEvidence(b),
		"camera_02_before": integrationStateEvidence(x), "camera_02_after": integrationStateEvidence(y),
		"camera_01_capture_pids_unchanged": integrationCaptureWorkersUnchanged(a, b), "camera_02_worker_pids_unchanged": integrationSameWorkers(x, y),
	})
}

func (r *integrationRun) localRelayProcessOwned(command, sourceID string) bool {
	if r.settings.RelayUser == "" || r.settings.RelayPassword == "" {
		return false
	}
	target := url.URL{Scheme: "rtsp", User: url.UserPassword(r.settings.RelayUser, r.settings.RelayPassword), Host: fmt.Sprintf("127.0.0.1:%d", Central.RTSP), Path: "/" + sourceID}
	decodedTarget, err := url.QueryUnescape(target.String())
	return err == nil && strings.Contains(command, decodedTarget) && strings.Contains(command, integrationVideoURL(Field, sourceID)) && strings.Contains(command, "-f rtsp -rtsp_transport tcp")
}

func (r *integrationRun) localRelayCrashCheck(ctx context.Context, remoteURL string) error {
	before, err := r.status(ctx)
	if err != nil {
		return err
	}
	stream := integrationSelectSource(before, integrationFirstSource)
	worker := stream.Workers["relay"]
	if stream.RelayLink != "local" || !worker.Running || !r.processOwned(ctx, "relay/"+integrationFirstSource, worker.PID) {
		return errors.New("refusing to kill a local forwarder without verified test ownership")
	}
	process, err := os.FindProcess(worker.PID)
	if err != nil {
		return errors.New("could not locate the private local forwarder")
	}
	started := time.Now()
	if process.Kill() != nil {
		return errors.New("could not kill the private local forwarder")
	}
	recovery, cancel := context.WithTimeout(ctx, integrationRecoveryTarget)
	defer cancel()
	if _, err := r.waitState(recovery, "camera-01: supervisor replaces a killed local RTSP forwarder", integrationRecoveryTarget, func(s State) bool {
		current := s.Workers["relay"]
		return s.RelayLink == "local" && current.Running && current.PID != worker.PID && current.Starts > worker.Starts && s.Source.Ready && s.Recording.Running
	}, integrationFirstSource); err != nil {
		return err
	}
	if _, err := r.waitVideo(recovery, "camera-01: local RTSP video decodes within 15 seconds of a relay crash", remoteURL, 1280, 720, 30, integrationRecoveryTarget); err != nil {
		return err
	}
	elapsed := time.Since(started)
	r.report.Timings["local_relay_kill_to_decoded_probe"] = integrationSeconds(elapsed)
	if err := r.check("Local RTSP relay crash recovery meets the provisional 15-second target", elapsed <= integrationRecoveryTarget, map[string]any{"measured_seconds": integrationSeconds(elapsed), "target_seconds": integrationRecoveryTarget.Seconds()}); err != nil {
		return err
	}
	if err := r.waitRelayLink(ctx, "Recovered camera-01 local forwarder uses RTSP/TCP with no SRT publisher", "local"); err != nil {
		return err
	}
	return r.relayLinkIsolation(ctx, "Local relay crash preserves both camera inputs, both recorders and camera-02 delivery", "local", before)
}

// A stopped receiver holds its TCP connections open but cannot consume video.
// This differs from killing a relay or closing its destination connection.
func (r *integrationRun) stalledLocalReceiverCheck(ctx context.Context) (result error) {
	before, err := r.status(ctx)
	if err != nil {
		return err
	}
	central := before.Workers["central"]
	local := integrationSelectSource(before, integrationFirstSource)
	previousRelay := local.Workers["relay"]
	if !central.Running || !previousRelay.Running || local.RelayLink != "local" || !r.processOwned(ctx, "central", central.PID) {
		return errors.New("refusing to pause a receiver without verified test ownership and an active local relay")
	}
	process, err := os.FindProcess(central.PID)
	if err != nil {
		return errors.New("could not locate the private receiver")
	}
	frozen := false
	var pausedAt, resumedAt time.Time
	resume := func() error {
		if !frozen {
			return nil
		}
		// Resuming must work even when a test deadline or user cancellation fired.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if !r.processOwned(cleanupCtx, "central", central.PID) || process.Signal(syscall.SIGCONT) != nil {
			return errors.New("could not resume the verified private receiver after its pause")
		}
		frozen = false
		resumedAt = time.Now()
		r.report.Timings["central_receiver_pause"] = integrationSeconds(resumedAt.Sub(pausedAt))
		return nil
	}
	defer func() { result = errors.Join(result, resume()) }()
	if process.Signal(syscall.SIGSTOP) != nil {
		return errors.New("could not pause the private receiver")
	}
	frozen = true
	pausedAt = time.Now()
	fmt.Println("Pausing only the test receiver; allowing up to 45 seconds for a blocked local forwarder to be replaced.")
	pauseCtx, cancelPause := context.WithTimeout(ctx, 45*time.Second)
	defer cancelPause()
	after := before
	replacement := WorkerState{}
	var samples []map[string]any
	for pauseCtx.Err() == nil {
		current, statusErr := r.status(pauseCtx)
		if statusErr != nil {
			samples = append(samples, map[string]any{"seconds_after_pause": integrationSeconds(time.Since(pausedAt)), "status_available": false})
			if integrationSleep(pauseCtx, 400*time.Millisecond) != nil {
				break
			}
			continue
		}
		after = current
		sample := map[string]any{"seconds_after_pause": integrationSeconds(time.Since(pausedAt)), "status_available": true}
		for _, id := range integrationSourceIDs {
			a, b := integrationSelectSource(before, id), integrationSelectSource(after, id)
			evidence := integrationStateEvidence(b)
			evidence["demo_pid"] = b.Workers["demo"].PID
			evidence["recorder_pid"] = b.Workers["recorder"].PID
			evidence["relay_pid"] = b.Workers["relay"].PID
			evidence["relay_starts"] = b.Workers["relay"].Starts
			sample[id] = evidence
			stable := b.Source.Observed && b.Source.Ready && b.Recording.Running && b.Source.BytesReceived >= a.Source.BytesReceived && b.Recording.Bytes >= a.Recording.Bytes
			for _, role := range []string{"demo", "recorder"} {
				old, active := a.Workers[role], b.Workers[role]
				stable = stable && old.Running && active.Running && old.PID > 1 && old.PID == active.PID && old.Starts == active.Starts
			}
			if !stable {
				samples = append(samples, sample)
				return r.check("Both camera inputs and recording workers stay active throughout a receiver stall", false, map[string]any{"samples": samples, "failed_source": id})
			}
		}
		samples = append(samples, sample)
		worker := integrationSelectSource(after, integrationFirstSource).Workers["relay"]
		if replacement.PID == 0 && worker.Running && worker.PID > 1 && worker.PID != previousRelay.PID && worker.Starts > previousRelay.Starts {
			replacement = worker
			r.report.Timings["receiver_pause_to_local_relay_replacement"] = integrationSeconds(time.Since(pausedAt))
		}
		// Hold the pause for at least 18 seconds even if the socket write timeout
		// acts sooner, so both independent recorders can finish multiple clips.
		if replacement.PID > 1 && time.Since(pausedAt) >= 18*time.Second {
			break
		}
		if integrationSleep(pauseCtx, 400*time.Millisecond) != nil {
			break
		}
	}
	if err := r.check("A frozen receiver causes the blocked local forwarder to be replaced within 45 seconds", replacement.PID > 1, map[string]any{
		"previous_relay_pid": previousRelay.PID, "replacement_relay_pid": replacement.PID,
		"starts_before": previousRelay.Starts, "starts_after": replacement.Starts,
		"replacement_observed_seconds": r.report.Timings["receiver_pause_to_local_relay_replacement"], "limit_seconds": 45,
	}); err != nil {
		return err
	}
	if err := r.check("Both camera inputs and recording workers stay active throughout a receiver stall", true, map[string]any{"samples": samples}); err != nil {
		return err
	}
	for _, id := range integrationSourceIDs {
		a, b := integrationSelectSource(before, id), integrationSelectSource(after, id)
		if err := r.check(id+": input and completed recordings grow while the receiver cannot read", integrationCaptureWorkersUnchanged(a, b) && b.Recording.Segments > a.Recording.Segments && b.Recording.Bytes > a.Recording.Bytes,
			map[string]any{"before": integrationStateEvidence(a), "after": integrationStateEvidence(b)}); err != nil {
			return err
		}
	}
	if err := resume(); err != nil {
		return err
	}
	if err := r.recoverBothReceiverVideos(ctx, resumedAt); err != nil {
		return err
	}
	if err := r.waitRelayLink(ctx, "After receiver recovery camera-01 publishes over RTSP/TCP with no SRT publisher", "local"); err != nil {
		return err
	}
	recovered, err := r.status(ctx)
	if err != nil {
		return err
	}
	for _, id := range integrationSourceIDs {
		a, b := integrationSelectSource(before, id), integrationSelectSource(recovered, id)
		if err := r.check(id+": receiver pause and recovery preserve its camera and recorder processes", integrationCaptureWorkersUnchanged(a, b), nil); err != nil {
			return err
		}
	}
	return nil
}

// Independent probes share one recovery deadline. Only this goroutine writes
// assertions; concurrent FFmpeg probes never mutate the report or owned PIDs.
func (r *integrationRun) recoverBothReceiverVideos(ctx context.Context, resumedAt time.Time) error {
	ctx, cancel := context.WithDeadline(ctx, resumedAt.Add(integrationRecoveryTarget))
	defer cancel()
	type outcome struct {
		id      string
		media   integrationMedia
		elapsed time.Duration
		passed  bool
	}
	results := make(chan outcome, len(integrationSourceIDs))
	for _, id := range integrationSourceIDs {
		go func(id string) {
			result := outcome{id: id}
			for ctx.Err() == nil {
				media, err := r.probe(ctx, integrationVideoURL(Central, id))
				result.media = media
				if err == nil && media.Codec == "h264" && media.Width == 1280 && media.Height == 720 && math.Abs(media.FrameRate-30) < 0.1 && math.Abs(media.ObservedRate-30) < 0.1 {
					result.elapsed = time.Since(resumedAt)
					result.passed = result.elapsed <= integrationRecoveryTarget
					break
				}
				if integrationSleep(ctx, 300*time.Millisecond) != nil {
					break
				}
			}
			if result.elapsed == 0 {
				result.elapsed = time.Since(resumedAt)
			}
			results <- result
		}(id)
	}
	for range integrationSourceIDs {
		result := <-results
		if err := r.check(result.id+": forwarded video decodes within 15 seconds of resuming the receiver", result.passed, map[string]any{"media": result.media, "observed_seconds": integrationSeconds(result.elapsed), "limit_seconds": integrationRecoveryTarget.Seconds()}); err != nil {
			return err
		}
		r.report.Timings["receiver_resume_to_"+result.id+"_decoded_probe"] = integrationSeconds(result.elapsed)
	}
	return nil
}

func (r *integrationRun) relayLinkCheck(ctx context.Context, remoteURL string) error {
	fmt.Println("Checking local RTSP forwarding, its crash recovery, and restoration of the SRT connection.")
	for _, link := range []string{"local", "srt", "local"} {
		before, err := r.status(ctx)
		if err != nil {
			return err
		}
		if _, err := r.sourceCommand(ctx, 5*time.Second, integrationFirstSource, "relay-link", link); err != nil {
			return err
		}
		name := "camera-01: local relay publishes over RTSP/TCP without an SRT publisher"
		if link == "srt" {
			name = "camera-01: SRT restoration removes RTSP publishing and negotiates 300 ms"
		}
		if err := r.waitRelayLink(ctx, name, link); err != nil {
			return err
		}
		if _, err := r.waitVideo(ctx, "camera-01: "+link+" relay copy profile decodes at 1280 × 720 and 30 fps", remoteURL, 1280, 720, 30, 15*time.Second); err != nil {
			return err
		}
		if err := r.relayLinkIsolation(ctx, "Switching camera-01 to "+link+" preserves capture and camera-02 workers and delivery", link, before); err != nil {
			return err
		}
		if link == "local" && r.report.Timings["local_relay_kill_to_decoded_probe"] == 0 {
			if err := r.localRelayCrashCheck(ctx, remoteURL); err != nil {
				return err
			}
		}
	}
	if err := r.stalledLocalReceiverCheck(ctx); err != nil {
		return err
	}
	return r.motion(ctx, "camera-02: SRT-delivered pictures keep moving after camera-01 link changes", integrationVideoURL(Central, integrationSecondSource))
}
