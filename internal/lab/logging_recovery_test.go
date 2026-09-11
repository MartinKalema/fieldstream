package lab

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func makeLogRetryDue(l *rollingLog) {
	l.mu.Lock()
	l.nextOpen = time.Now().Add(-time.Second)
	l.mu.Unlock()
}

func TestMediaLogRecoversAfterInitialOpenFailure(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "temporarily-missing")
	name := filepath.Join(directory, "media.log")
	log := mediaLog(name)
	defer log.Close()
	if log.errorText() == "" {
		t.Fatal("initial open failure was hidden")
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if n, err := log.Write([]byte("discard during backoff")); n != 22 || err != nil {
		t.Fatalf("output was not drained: %d %v", n, err)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("logger retried before its backoff elapsed")
	}
	makeLogRetryDue(log)
	if _, err := log.Write([]byte("recovered")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(name)
	if err != nil || string(data) != "recovered" {
		t.Fatalf("logging did not recover: %q %v", data, err)
	}
	if log.errorText() != "" {
		t.Fatal("successful logging retained an obsolete error")
	}
}

func TestMediaLogRecoversAfterWriteFailureAndHonorsClose(t *testing.T) {
	name := filepath.Join(t.TempDir(), "media.log")
	log, err := newRollingLog(name, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if _, err = log.Write([]byte("before")); err != nil {
		t.Fatal(err)
	}
	if err = log.file.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := log.Write([]byte("dropped")); n != 7 || err != nil {
		t.Fatalf("failed destination blocked output: %d %v", n, err)
	}
	if log.errorText() == "" || log.file != nil {
		t.Fatal("failed handle was retained")
	}
	makeLogRetryDue(log)
	if _, err = log.Write([]byte("after")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(name)
	if err != nil || string(data) != "beforeafter" {
		t.Fatalf("reopen lost or failed to append logs: %q %v", data, err)
	}
	if log.errorText() != "" {
		t.Fatal("write recovery left a stale error")
	}
	if err = log.Close(); err != nil {
		t.Fatal(err)
	}
	makeLogRetryDue(log)
	if _, err = log.Write([]byte("after close")); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(name)
	if err != nil || string(data) != "beforeafter" {
		t.Fatal("closed logger reopened")
	}
}
