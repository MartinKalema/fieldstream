package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type process struct {
	PID        int     `json:"pid"`
	Started    string  `json:"started"`
	CPUSeconds float64 `json:"cpu_seconds"`
	RSSKiB     uint64  `json:"rss_kib"`
}

var cpuPattern = regexp.MustCompile(`^(?:(\d+)-)?(?:(\d+):)?(\d+):(\d{2})(?:\.(\d{1,9}))?$`)

// ps uses minutes:seconds[.fraction], hours:minutes:seconds, or
// days-hours:minutes:seconds. This is cumulative CPU, not wall-clock age.
func parseCPU(value string) (time.Duration, error) {
	m := cpuPattern.FindStringSubmatch(value)
	if m == nil || (m[1] != "" && m[2] == "") {
		return 0, errors.New("invalid cumulative CPU time")
	}
	n := [4]uint64{}
	for i := range n {
		if m[i+1] == "" {
			continue
		}
		v, err := strconv.ParseUint(m[i+1], 10, 32)
		if err != nil {
			return 0, errors.New("invalid cumulative CPU time")
		}
		n[i] = v
	}
	if n[3] >= 60 || (m[2] != "" && n[2] >= 60) || (m[1] != "" && n[1] >= 24) {
		return 0, errors.New("invalid cumulative CPU time")
	}
	seconds := n[0]*86400 + n[1]*3600 + n[2]*60 + n[3]
	// A generous ten-year bound also keeps duration arithmetic away from overflow.
	if seconds > 10*366*86400 {
		return 0, errors.New("cumulative CPU time is too large")
	}
	fraction := uint64(0)
	if m[5] != "" {
		fraction, _ = strconv.ParseUint(m[5]+strings.Repeat("0", 9-len(m[5])), 10, 32)
	}
	return time.Duration(seconds)*time.Second + time.Duration(fraction), nil
}

func parseProcesses(data []byte, wanted []int) (map[int]process, error) {
	result := map[int]process{}
	allowed := map[int]bool{}
	for _, pid := range wanted {
		if pid <= 0 {
			return nil, errors.New("invalid process ID")
		}
		allowed[pid] = true
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 8 {
			return nil, errors.New("process counters unavailable")
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || !allowed[pid] {
			return nil, errors.New("unexpected process identity")
		}
		if _, ok := result[pid]; ok {
			return nil, errors.New("duplicate process identity")
		}
		started := strings.Join(fields[1:6], " ")
		if _, err := time.Parse("Mon Jan 2 15:04:05 2006", started); err != nil {
			return nil, errors.New("process start time unavailable")
		}
		cpu, err := parseCPU(fields[6])
		if err != nil {
			return nil, err
		}
		rss, err := strconv.ParseUint(fields[7], 10, 64)
		if err != nil {
			return nil, errors.New("process memory unavailable")
		}
		result[pid] = process{pid, started, cpu.Seconds(), rss}
	}
	if len(result) != len(allowed) {
		return nil, errors.New("a process disappeared")
	}
	return result, nil
}

type boundedOutput struct {
	bytes.Buffer
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("diagnostic output exceeded its bound")
	}
	return b.Buffer.Write(p)
}

func childOutput(ctx context.Context, executable string, args ...string) ([]byte, error) {
	return runChild(ctx, executable, true, args...)
}

// Only the outer status sampler owns a new group. A nested ps inherits that
// group, so cancelling the sampler also kills ps if its own watchdog dies.
func runChild(ctx context.Context, executable string, ownGroup bool, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if ownGroup {
		// This is the diagnostic's private group, never the measured service's.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
	}
	// Otherwise CommandContext's default Cancel kills this child alone when its
	// own deadline expires; it must not kill the sampler or its siblings.
	cmd.WaitDelay = 200 * time.Millisecond
	var output boundedOutput
	output.limit = 16 << 10
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if err := cmd.Run(); err != nil {
		return nil, errors.New("bounded diagnostic child did not complete")
	}
	return output.Bytes(), nil
}

func readProcesses(ctx context.Context, relayPID, supervisorPID int) (process, process, error) {
	if relayPID <= 0 || supervisorPID <= 0 || relayPID == supervisorPID {
		return process{}, process{}, errors.New("invalid process identities")
	}
	psContext, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	data, err := runChild(psContext, "/bin/ps", false, "-p", fmt.Sprintf("%d,%d", relayPID, supervisorPID), "-o", "pid=", "-o", "lstart=", "-o", "time=", "-o", "rss=")
	if err != nil {
		return process{}, process{}, err
	}
	processes, err := parseProcesses(data, []int{relayPID, supervisorPID})
	if err != nil {
		return process{}, process{}, err
	}
	return processes[relayPID], processes[supervisorPID], nil
}
