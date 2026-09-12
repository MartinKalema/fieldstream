package gstreamer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"testing"
	"time"
)

func helperCommand(mode string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	// Race instrumentation otherwise sleeps for one second at process exit,
	// longer than the intentionally short graceful-stop window in these tests.
	cmd.Env = append(os.Environ(), "FIELDSTREAM_GST_TEST_HELPER="+mode,
		"GORACE="+os.Getenv("GORACE")+" atexit_sleep_ms=0")
	return cmd
}

func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("FIELDSTREAM_GST_TEST_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "exit-zero":
		os.Exit(0)
	case "exit-error":
		os.Exit(7)
	case "ignore":
		signal.Ignore(os.Interrupt)
		fmt.Fprintln(os.Stdout, "ready")
		time.Sleep(30 * time.Second)
	case "graceful":
		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, os.Interrupt)
		fmt.Fprintln(os.Stdout, "ready")
		<-interrupt
		os.Exit(0)
	default:
		os.Exit(9)
	}
	os.Exit(0)
}

func TestProcessEarlyExitIsNotACompletedObservation(t *testing.T) {
	for _, tc := range []struct {
		mode string
		code int
	}{{"exit-zero", 0}, {"exit-error", 7}} {
		result := RunProcess(context.Background(), helperCommand(tc.mode), 5*time.Second, 100*time.Millisecond, io.Discard)
		if result.Result != "exited_early" || result.ExitCode == nil || *result.ExitCode != tc.code || result.ForcedStop {
			t.Fatalf("unexpected early-exit result: %+v", result)
		}
	}
}

func TestProcessWithNoDurationReportsOrdinaryExit(t *testing.T) {
	for _, tc := range []struct {
		mode string
		code int
	}{{"exit-zero", 0}, {"exit-error", 7}} {
		result := RunProcess(context.Background(), helperCommand(tc.mode), 0, 100*time.Millisecond, io.Discard)
		if result.Result != "exited" || result.ExitCode == nil || *result.ExitCode != tc.code || result.ForcedStop {
			t.Fatalf("unexpected ordinary exit: %+v", result)
		}
	}
}

func TestAlreadyReadyExitAndCancellationTakePrecedenceOverDeadline(t *testing.T) {
	deadline := make(chan time.Time)
	close(deadline)
	exitErr := errors.New("child failed")
	for _, canceled := range []bool{false, true} {
		for range 100 {
			ctx, cancel := context.WithCancel(context.Background())
			if canceled {
				cancel()
			}
			done := make(chan error, 1)
			done <- exitErr
			reason, err, exited := waitForStop(ctx, done, deadline)
			cancel()
			want := "exited_early"
			if canceled {
				want = "canceled"
			}
			if reason != want || !exited || !errors.Is(err, exitErr) || len(done) != 0 {
				t.Fatalf("ready events classified as %q, %v, exited=%v; want %s with consumed exit", reason, err, exited, want)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reason, _, exited := waitForStop(ctx, make(chan error), deadline)
	if reason != "canceled" || exited {
		t.Fatal("pending cancellation lost to the ready deadline")
	}
}

func TestProcessDurationAllowsGracefulStopAndBoundsUnresponsiveChild(t *testing.T) {
	for _, mode := range []string{"graceful", "ignore"} {
		var output bytes.Buffer
		started := time.Now()
		result := RunProcess(context.Background(), helperCommand(mode), 500*time.Millisecond, 100*time.Millisecond, &output)
		if result.Result != "duration_elapsed" || result.ForcedStop != (mode == "ignore") || !strings.Contains(output.String(), "ready") {
			t.Fatalf("unexpected %s result: %+v output=%q", mode, result, output.String())
		}
		if time.Since(started) > 3*time.Second || result.EndedAt.Before(result.StartedAt) {
			t.Fatalf("run exceeded its bounded stop: %+v", result)
		}
	}
}

type readyWriter struct {
	once  sync.Once
	ready chan struct{}
}

func (w *readyWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.ready) })
	return len(p), nil
}

func TestProcessExternalCancellationRemainsDistinct(t *testing.T) {
	for _, duration := range []time.Duration{0, 10 * time.Second} {
		t.Run(duration.String(), func(t *testing.T) { testProcessCancellation(t, duration) })
	}
}

func testProcessCancellation(t *testing.T, duration time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := &readyWriter{ready: make(chan struct{})}
	done := make(chan Result, 1)
	go func() {
		done <- RunProcess(ctx, helperCommand("graceful"), duration, 100*time.Millisecond, writer)
	}()
	select {
	case <-writer.ready:
		cancel()
	case <-time.After(3 * time.Second):
		t.Fatal("child did not become ready")
	}
	select {
	case result := <-done:
		if result.Result != "canceled" || result.ForcedStop || result.ExitCode == nil || *result.ExitCode != 0 {
			t.Fatalf("unexpected cancellation: %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not stop the child")
	}
}

func TestLogCapStillDrainsProcessOutput(t *testing.T) {
	var buffer bytes.Buffer
	log := NewLimitedLog(&buffer, 10)
	for _, input := range []string{"123456", "789abc", "defghi"} {
		n, err := log.Write([]byte(input))
		if n != len(input) || err != nil {
			t.Fatalf("did not drain input: %d %v", n, err)
		}
	}
	kept, dropped, err := log.Snapshot()
	if buffer.String() != "123456789a" || kept != 10 || dropped != 8 || err != nil {
		t.Fatalf("incorrect cap: %q kept=%d dropped=%d err=%v", buffer.String(), kept, dropped, err)
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestLogSnapshotReportsWriteFailureWhileDraining(t *testing.T) {
	log := NewLimitedLog(brokenWriter{}, 10)
	for range 2 {
		if n, err := log.Write([]byte("123456")); n != 6 || err != nil {
			t.Fatalf("stopped draining after log write failure: %d %v", n, err)
		}
	}
	kept, dropped, err := log.Snapshot()
	if kept != 0 || dropped != 12 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("incorrect failure snapshot: %d %d %v", kept, dropped, err)
	}
}

func TestProcessCanceledBeforeStartAndMissingExecutable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := helperCommand("ignore")
	result := RunProcess(ctx, cmd, time.Second, time.Millisecond, io.Discard)
	if result.Result != "canceled" || cmd.Process != nil {
		t.Fatal("started a canceled operation")
	}
	result = RunProcess(context.Background(), exec.Command("/no-such-fieldstream-executable"), time.Second, time.Millisecond, io.Discard)
	if result.Result != "start_failed" || result.ExitCode != nil || result.ProcessError == "" {
		t.Fatalf("unexpected launch failure: %+v", result)
	}
}
