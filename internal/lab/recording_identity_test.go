package lab

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRecordingIdentityDamageStaysWithinItsSession(t *testing.T) {
	p, cat, _ := recordingFixture(t)
	for _, id := range []string{"camera-01", "camera-02"} {
		dir := filepath.Join(p.Recordings, id+"-20260911-120000-000000000000000000000000")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string]string{"clip.mp4": "saved footage", "segments.csv": "clip.mp4,0,5\n"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if id == "camera-01" {
			if err := os.WriteFile(filepath.Join(dir, ".source.json"), []byte("broken"), 0600); err != nil {
				t.Fatal(err)
			}
		} // camera-02 deliberately loses its sidecar; recover its ID from the validated folder name.
	}
	state, err := scanRecordings(context.Background(), p, map[string]Segment{}, cat)
	if err != nil || state.BlockedReason != nil || state.Segments != 1 || len(state.Warnings) != 1 {
		t.Fatalf("one corrupt session blocked healthy recording: %+v, %v", state, err)
	}
	items, err := cat.ListSegments(context.Background(), 10, 0)
	if err != nil || len(items) != 1 || items[0].SourceID != "camera-02" {
		t.Fatalf("recording was misattributed: %+v, %v", items, err)
	}
	unknown := filepath.Join(p.Recordings, "unrecognized-folder")
	if _, err := recordingSource(unknown); err == nil {
		t.Fatal("unknown missing identity was guessed as camera-01")
	}
}
