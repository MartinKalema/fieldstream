package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseCPU(t *testing.T) {
	for _, test := range []struct {
		value string
		want  time.Duration
	}{
		{"0:00.00", 0}, {"12:34.56", 754560 * time.Millisecond},
		{"01:02:03", 3723 * time.Second}, {"120:01.123456789", 7201*time.Second + 123456789*time.Nanosecond},
		{"2-03:04:05.25", (2*86400+3*3600+4*60+5)*time.Second + 250*time.Millisecond},
	} {
		t.Run(test.value, func(t *testing.T) {
			got, err := parseCPU(test.value)
			if err != nil || got != test.want {
				t.Fatalf("got %v, %v; want %v", got, err, test.want)
			}
		})
	}
	for _, value := range []string{"", "-1:00", "00:60", "1:60:00", "1-24:00:00", "1-02:03", "1:01.1234567890", "999999999999:00", "1.2", "1:02 extra"} {
		if _, err := parseCPU(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
}

func TestParseProcesses(t *testing.T) {
	data := []byte(" 12 Sat Sep 12 19:20:30 2026 0:00.00 1234\n 31 Sat Sep  5 01:02:03 2026 2-03:04:05 8000\n")
	got, err := parseProcesses(data, []int{12, 31})
	if err != nil || got[12].CPUSeconds != 0 || got[12].RSSKiB != 1234 || got[31].Started != "Sat Sep 5 01:02:03 2026" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, bad := range []string{
		"12 Sat Sep 12 19:20:30 2026 0:00.00 1234\n",
		"12 Sat Sep 12 19:20:30 2026 0:00.00 1234\n12 Sat Sep 12 19:20:30 2026 0:00.00 1234\n",
		"99 Sat Sep 12 19:20:30 2026 0:00.00 1234\n",
		"12 Sat Bad 12 19:20:30 2026 0:00.00 1234\n",
		"12 Sat Sep 12 19:20:30 2026 0:00.00 -1\n",
		"12 Sat Sep 12 19:20:30 2026 0:00.00\n",
	} {
		if _, err := parseProcesses([]byte(bad), []int{12, 31}); err == nil {
			t.Errorf("accepted malformed or incomplete process result")
		}
	}
}

func TestBoundedChild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slow")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec /bin/sleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := childOutput(ctx, path); err == nil {
		t.Fatal("timeout was accepted")
	}
	if time.Since(start) > time.Second {
		t.Fatal("child cancellation was not bounded")
	}
}

func TestOutputBound(t *testing.T) {
	var output boundedOutput
	output.limit = 4
	if _, err := output.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte("5")); err == nil || output.Len() != 4 {
		t.Fatal("output bound not enforced")
	}
}

type childIdentity struct {
	PID, Parent, Group int
}

// The outer helper stands in for the status sampler; the leaf stands in for a
// stuck ps. Its own 30-second watchdog cannot explain a prompt cancellation.
func TestNestedChildHelper(t *testing.T) {
	var args []string
	for i, arg := range os.Args {
		if arg == "--relaycheck-child-helper" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) != 2 {
		return
	}
	if args[0] == "leaf" {
		group, err := syscall.Getpgid(0)
		if err != nil {
			os.Exit(2)
		}
		data, _ := json.Marshal(childIdentity{PID: os.Getpid(), Parent: os.Getppid(), Group: group})
		if os.WriteFile(args[1], data, 0600) != nil {
			os.Exit(2)
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	if args[0] != "outer" && args[0] != "deadline" {
		os.Exit(2)
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(2)
	}
	duration := 30 * time.Second
	if args[0] == "deadline" {
		duration = 150 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	_, err = runChild(ctx, executable, false, "-test.run=^TestNestedChildHelper$", "--", "--relaycheck-child-helper", "leaf", args[1])
	if args[0] == "deadline" {
		if err == nil {
			os.Exit(2)
		}
		_, _ = os.Stdout.WriteString("nested deadline completed")
	}
	os.Exit(0)
}

func TestNestedDeadlinePreservesSampler(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	data, err := childOutput(ctx, executable, "-test.run=^TestNestedChildHelper$", "--", "--relaycheck-child-helper", "deadline", filepath.Join(t.TempDir(), "nested-child.json"))
	if err != nil || string(data) != "nested deadline completed" {
		t.Fatalf("nested deadline killed the sampler: output=%q error=%v", data, err)
	}
}

func TestOuterCancellationKillsNestedChild(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "nested-child.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := childOutput(ctx, executable, "-test.run=^TestNestedChildHelper$", "--", "--relaycheck-child-helper", "outer", path)
		finished <- err
	}()
	var identity childIdentity
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && json.Unmarshal(data, &identity) == nil && identity.PID > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if identity.PID <= 0 {
		t.Fatal("nested child did not start")
	}
	// Cleanup also stops the leaf if this test catches a future regression.
	defer syscall.Kill(identity.PID, syscall.SIGKILL)
	if identity.Group != identity.Parent || identity.Group == identity.PID {
		t.Fatalf("nested child escaped the sampler's process group: %+v", identity)
	}
	cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("cancelled sampler unexpectedly completed")
		}
	case <-time.After(time.Second):
		t.Fatal("sampler did not stop promptly")
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(identity.PID, 0), syscall.ESRCH) {
			return
		}
		// A child killed with its parent can briefly be a zombie until reaped.
		// That is terminated, unlike a still-running orphan with no watchdog.
		probe, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
		data, _ := exec.CommandContext(probe, "/bin/ps", "-p", strconv.Itoa(identity.PID), "-o", "stat=").Output()
		stop()
		if strings.HasPrefix(strings.TrimSpace(string(data)), "Z") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("nested child survived cancellation of its sampler")
}
