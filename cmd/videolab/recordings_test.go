package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"fieldvideolab/internal/lab"
)

func TestRecordingsHealthEmptyAndInvalidCommands(t *testing.T) {
	p := lab.NewPaths(t.TempDir())
	var output bytes.Buffer
	if err := recordingsCommand(p, "", []string{"health", "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	var report struct {
		lab.VideoHealthReport
		Checked int `json:"checked_this_run"`
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil || report.Summary.Total != 0 || report.Checked != 0 {
		t.Fatalf("empty inventory must be valid JSON with zero checked: %s %v", output.String(), err)
	}
	for _, args := range [][]string{nil, {"delete"}, {"health", "--recheck"}, {"check", "--limit", "bad"}, {"health", "extra"}} {
		output.Reset()
		if err := recordingsCommand(p, "", args, &output); err == nil {
			t.Fatalf("invalid command accepted: %v", args)
		}
	}
	output.Reset()
	if err := recordingsCommand(p, "", []string{"check", "--help"}, &output); err != nil || !strings.Contains(output.String(), "-path") {
		t.Fatalf("help should succeed without loading settings: %q %v", output.String(), err)
	}
	output.Reset()
	if err := recordingsCommand(p, "", []string{"health"}, &output); err != nil || !strings.Contains(output.String(), "0 unchecked") || !strings.Contains(output.String(), "dated decode results") {
		t.Fatalf("plain health summary missing limits: %q %v", output.String(), err)
	}
}
