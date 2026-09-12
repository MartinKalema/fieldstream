package gstreamer

import (
	"context"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Result describes process lifetime and cleanup, not video presentation or age.
type Result struct {
	StartedAt    time.Time `json:"started_at"`
	EndedAt      time.Time `json:"ended_at"`
	Result       string    `json:"result"`
	ExitCode     *int      `json:"exit_code"`
	ForcedStop   bool      `json:"forced_stop"`
	ProcessError string    `json:"process_error,omitempty"`
}

// RunProcess starts cmd in its own process group and cleans up that group when
// it finishes. A zero duration waits until process exit or context cancellation.
// A positive duration bounds the run and then allows grace for SIGINT shutdown.
func RunProcess(ctx context.Context, cmd *exec.Cmd, duration, grace time.Duration, output io.Writer) (result Result) {
	result.StartedAt = time.Now().UTC()
	defer func() { result.EndedAt = time.Now().UTC() }()
	if ctx.Err() != nil {
		result.Result = "canceled"
		return result
	}
	if duration < 0 || grace < 0 {
		result.Result, result.ProcessError = "start_failed", "duration and grace cannot be negative"
		return result
	}
	cmd.Stdout, cmd.Stderr = output, output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		result.Result, result.ProcessError = "start_failed", err.Error()
		return result
	}
	// Every child belongs to this new group. Final cleanup also covers children
	// left behind after gst-launch exits, without signaling the normal lab.
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var deadline <-chan time.Time
	if duration > 0 {
		timer := time.NewTimer(duration)
		defer timer.Stop()
		deadline = timer.C
	}
	reason, waitErr, exited := waitForStop(ctx, done, deadline)
	if duration == 0 && reason == "exited_early" {
		reason = "exited"
	}
	result.Result = reason
	if !exited {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
		graceTimer := time.NewTimer(grace)
		select {
		case waitErr = <-done:
			graceTimer.Stop()
		case <-graceTimer.C:
			// A completed Wait can be ready alongside the grace timer.
			select {
			case waitErr = <-done:
			default:
				result.ForcedStop = true
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				waitErr = <-done
			}
		}
	}
	if cmd.ProcessState != nil {
		code := cmd.ProcessState.ExitCode()
		result.ExitCode = &code
	}
	if waitErr != nil {
		result.ProcessError = waitErr.Error()
	}
	return result
}

func waitForStop(ctx context.Context, done <-chan error, deadline <-chan time.Time) (reason string, waitErr error, exited bool) {
	select {
	case waitErr = <-done:
		reason, exited = "exited_early", true
	case <-ctx.Done():
		reason = "canceled"
	case <-deadline:
		reason = "duration_elapsed"
	}
	// Go chooses randomly when several select cases are ready. A pending exit
	// or cancellation must not become a completed observation merely because
	// the deadline case won that selection. Preserve whether Wait was consumed
	// separately, so cancellation never leads to waiting for the same exit twice.
	if !exited {
		select {
		case waitErr = <-done:
			reason, exited = "exited_early", true
		default:
		}
	}
	if ctx.Err() != nil {
		reason = "canceled"
	}
	return reason, waitErr, exited
}

// LimitedLog keeps draining after the cap so verbose process output cannot
// block. Snapshot reports dropped bytes and any underlying writer error.
type LimitedLog struct {
	mu      sync.Mutex
	writer  io.Writer
	limit   int64
	kept    int64
	dropped int64
	err     error
}

// NewLimitedLog limits retained bytes, treating a negative limit as zero.
func NewLimitedLog(writer io.Writer, limit int64) *LimitedLog {
	if writer == nil {
		writer = io.Discard
	}
	return &LimitedLog{writer: writer, limit: max(0, limit)}
}

// Snapshot can be called while process output is still arriving.
func (log *LimitedLog) Snapshot() (kept, dropped int64, err error) {
	log.mu.Lock()
	defer log.mu.Unlock()
	return log.kept, log.dropped, log.err
}

func (log *LimitedLog) Write(p []byte) (int, error) {
	log.mu.Lock()
	defer log.mu.Unlock()
	n := len(p)
	keep := min(int64(n), max(0, log.limit-log.kept))
	if keep > 0 && log.err == nil {
		written, err := log.writer.Write(p[:keep])
		log.kept += int64(written)
		log.dropped += int64(n - written)
		if err == nil && int64(written) != keep {
			err = io.ErrShortWrite
		}
		log.err = err
	} else {
		log.dropped += int64(n)
	}
	return n, nil
}
