package main

import (
	"context"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type processResult struct {
	StartedAt    time.Time `json:"started_at"`
	EndedAt      time.Time `json:"ended_at"`
	Result       string    `json:"result"`
	ExitCode     *int      `json:"exit_code"`
	ForcedStop   bool      `json:"forced_stop"`
	ProcessError string    `json:"process_error,omitempty"`
}

func runProcess(ctx context.Context, cmd *exec.Cmd, duration, grace time.Duration, output io.Writer) (result processResult) {
	result.StartedAt = time.Now().UTC()
	defer func() { result.EndedAt = time.Now().UTC() }()
	if ctx.Err() != nil {
		result.Result = "canceled"
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
	timer := time.NewTimer(duration)
	defer timer.Stop()
	reason, waitErr, exited := waitForStop(ctx, done, timer.C)
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

// Keep draining after the cap so a verbose process cannot block on its output.
type limitedLog struct {
	mu      sync.Mutex
	writer  io.Writer
	limit   int64
	kept    int64
	dropped int64
	err     error
}

func (log *limitedLog) Write(p []byte) (int, error) {
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
