package lab

import (
	"context"
	"fmt"
	"time"
)

func (r *integrationRun) deliveryProfileCheck(ctx context.Context) error {
	fmt.Println("Checking detail and small delivery settings while both original recordings continue.")
	remoteURL := integrationVideoURL(Central, integrationFirstSource)
	localURL := integrationVideoURL(Field, integrationFirstSource)
	otherRemoteURL := integrationVideoURL(Central, integrationSecondSource)
	// relayLinkCheck leaves camera-01 on copy/local. Exercise both transport
	// paths with detail, then restore copy/local for recording persistence.
	for _, trial := range []struct {
		profile, link string
		width, height int
		fps           float64
	}{
		{"small", "local", 640, 360, 20},
		{"detail", "local", 1280, 720, 20},
		{"detail", "srt", 1280, 720, 20},
		{"copy", "srt", 1280, 720, 30},
		{"copy", "local", 1280, 720, 30},
	} {
		before, err := r.status(ctx)
		if err != nil {
			return err
		}
		current := integrationSelectSource(before, integrationFirstSource)
		if current.Profile != trial.profile {
			if _, err := r.sourceCommand(ctx, 5*time.Second, integrationFirstSource, "profile", trial.profile); err != nil {
				return err
			}
		}
		if current.RelayLink != trial.link {
			if _, err := r.sourceCommand(ctx, 5*time.Second, integrationFirstSource, "relay-link", trial.link); err != nil {
				return err
			}
		}
		name := fmt.Sprintf("camera-01: %s/%s", trial.profile, trial.link)
		if _, err := r.waitVideo(ctx, fmt.Sprintf("%s decodes at %d × %d and %.0f fps", name, trial.width, trial.height, trial.fps), remoteURL, trial.width, trial.height, trial.fps, 25*time.Second); err != nil {
			return err
		}
		if err := r.waitRelayLink(ctx, name+" uses its selected forwarding connection", trial.link); err != nil {
			return err
		}
		if err := r.relayLinkIsolation(ctx, name+" preserves camera-01 capture and all camera-02 workers and delivery", trial.link, before); err != nil {
			return err
		}
		if trial.profile != "copy" {
			if _, err := r.waitVideo(ctx, name+": original camera-01 source stays at 1280 × 720 and 30 fps", localURL, 1280, 720, 30, 12*time.Second); err != nil {
				return err
			}
			if _, err := r.waitVideo(ctx, name+": camera-02 delivery stays at 1280 × 720 and 30 fps", otherRemoteURL, 1280, 720, 30, 12*time.Second); err != nil {
				return err
			}
		}
	}
	return nil
}
