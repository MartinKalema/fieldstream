package lab

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordingScannerSkipsMalformedAndOversizedCompleteRows(t *testing.T) {
	p, c, directory := recordingFixture(t)
	for _, name := range []string{"first.mp4", "second.mp4", "third.mp4", "tail.mp4", "active.mp4"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("finished fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	data := "first.mp4,0,5\n\"unterminated quote\nsecond.mp4,5,10\n" +
		strings.Repeat("x", 16<<10) + "tail.mp4,0,5\nthird.mp4,10,15\nactive.mp4,15,20"
	if err := os.WriteFile(filepath.Join(directory, "segments.csv"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	known := map[string]Segment{}
	state, err := scanRecordings(context.Background(), p, known, c)
	if err != nil {
		t.Fatal(err)
	}
	if state.Segments != 3 || len(known) != 3 {
		t.Fatalf("valid rows after corruption were lost or a row tail was accepted: %+v", state)
	}
	warnings := strings.Join(state.Warnings, "\n")
	if !strings.Contains(warnings, "malformed") || !strings.Contains(warnings, "oversized") {
		t.Fatalf("row corruption was hidden: %s", warnings)
	}
	if state.BlockedReason != nil {
		t.Fatalf("one malformed session blocked recording globally: %s", *state.BlockedReason)
	}
}

func TestRecordingScannerBoundsOversizedPartialRowAndWarnings(t *testing.T) {
	p, c, directory := recordingFixture(t)
	data := strings.Repeat("\"bad quote\n", 100) + strings.Repeat("x", 40<<10)
	if err := os.WriteFile(filepath.Join(directory, "segments.csv"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := scanRecordings(context.Background(), p, map[string]Segment{}, c)
	if err != nil {
		t.Fatal(err)
	}
	if state.Segments != 0 || len(state.Warnings) != 20 {
		t.Fatalf("corrupt list was accepted or warnings grew without a bound: %+v", state)
	}
}
