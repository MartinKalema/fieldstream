package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

func negotiatedPublishWait(ctx context.Context, ports Ports, sourceID string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/v3/srtconns/list", ports.API), nil)
	if err != nil {
		return 0, err
	}
	response, err := apiClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, errors.New("connection statistics unavailable")
	}
	// Decode only these fields; connection query strings contain credentials.
	var payload struct {
		Items []struct {
			Path  string `json:"path"`
			State string `json:"state"`
			Wait  int    `json:"msReceiveTsbPdDelay"`
		} `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return 0, err
	}
	for _, item := range payload.Items {
		if item.Path == sourceID && item.State == "publish" {
			return item.Wait, nil
		}
	}
	return 0, errors.New("publisher not connected")
}

func (r *integrationRun) relayWaitCheck(ctx context.Context, remoteURL string) error {
	fmt.Println("Changing only camera-01's forwarding recovery wait and checking the negotiated connection.")
	before, err := r.status(ctx)
	if err != nil {
		return err
	}
	for _, requested := range []int{120, 300} {
		if _, err := r.sourceCommand(ctx, 5*time.Second, integrationFirstSource, "relay-wait", strconv.Itoa(requested)); err != nil {
			return err
		}
		deadline, cancel := context.WithTimeout(ctx, 12*time.Second)
		observed := 0
		for deadline.Err() == nil {
			observed, _ = negotiatedPublishWait(deadline, Central, integrationFirstSource)
			if observed == requested {
				break
			}
			if integrationSleep(deadline, 200*time.Millisecond) != nil {
				break
			}
		}
		cancel()
		if err := r.check(fmt.Sprintf("camera-01: receiver negotiates %d ms forwarding wait", requested), observed == requested, map[string]any{"requested_ms": requested, "negotiated_ms": observed}); err != nil {
			return err
		}
		if _, err := r.waitVideo(ctx, fmt.Sprintf("camera-01: %d ms forwarding still decodes at 1280 × 720", requested), remoteURL, 1280, 720, 30, 12*time.Second); err != nil {
			return err
		}
		after, err := r.status(ctx)
		if err != nil {
			return err
		}
		a, b := before.Sources[integrationFirstSource], after.Sources[integrationFirstSource]
		otherBefore, otherAfter := integrationSelectSource(before, integrationSecondSource), integrationSelectSource(after, integrationSecondSource)
		inputWait, _ := negotiatedPublishWait(ctx, Field, integrationFirstSource)
		otherWait, _ := negotiatedPublishWait(ctx, Central, integrationSecondSource)
		unchanged := a.Workers["demo"].PID == b.Workers["demo"].PID && a.Workers["recorder"].PID == b.Workers["recorder"].PID && b.Recording.Running && integrationSameWorkers(otherBefore, otherAfter) && otherAfter.Recording.Running && inputWait == 300 && otherWait == 300
		if err := r.check(fmt.Sprintf("%d ms relay wait preserves camera input, both recorders and camera-02 workers", requested), unchanged, map[string]any{"camera_input_wait_ms": inputWait, "camera_02_forwarding_wait_ms": otherWait}); err != nil {
			return err
		}
	}
	return nil
}
