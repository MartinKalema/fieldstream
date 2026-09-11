package lab

import (
	"io"
	"os"
	"sync"
	"time"
)

// Drain child output even if logging fails. A full disk must not back up the
// stdout pipe and freeze a media process. Keep at most two bounded log files.
type rollingLog struct {
	mu          sync.Mutex
	file        *os.File
	path        string
	limit, size int64
	lastError   error
	nextOpen    time.Time
	closed      bool
}

func newRollingLog(path string, limit int64) (*rollingLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &rollingLog{file: f, path: path, limit: limit, size: info.Size()}, nil
}

func mediaLog(path string) *rollingLog {
	l, err := newRollingLog(path, 5_000_000)
	if err != nil {
		return &rollingLog{path: path, limit: 5_000_000, lastError: err, nextOpen: time.Now().Add(2 * time.Second)}
	}
	return l
}

// Called with the mutex held. A failed destination is retried on a later write;
// a stream of log messages must not turn a disk failure into a tight retry loop.
func (l *rollingLog) failed(err error) {
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
	l.lastError = err
	l.nextOpen = time.Now().Add(2 * time.Second)
}

func (l *rollingLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	wanted := len(data)
	if l.closed || wanted == 0 {
		return wanted, nil
	}
	if l.file == nil {
		if time.Now().Before(l.nextOpen) {
			return wanted, nil
		}
		file, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			l.failed(err)
			return wanted, nil
		}
		l.file = file
		info, err := file.Stat()
		if err != nil {
			l.failed(err)
			return wanted, nil
		}
		l.size = info.Size()
	}
	if int64(len(data)) > l.limit {
		data = data[len(data)-int(l.limit):]
	}
	if l.size+int64(len(data)) > l.limit {
		if l.file != nil {
			l.file.Close()
			l.file = nil
		}
		if err := os.Rename(l.path, l.path+".previous"); err != nil && !os.IsNotExist(err) {
			l.failed(err)
			return wanted, nil
		}
		f, err := os.OpenFile(l.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			l.failed(err)
			return wanted, nil
		}
		l.file = f
		l.size = 0
	}
	if l.file == nil {
		return wanted, nil
	}
	n, err := l.file.Write(data)
	l.size += int64(n)
	if n < len(data) && err == nil {
		err = io.ErrShortWrite
	}
	if err != nil {
		l.failed(err)
	} else {
		l.lastError = nil
		l.nextOpen = time.Time{}
	}
	return wanted, nil
}

func (l *rollingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func (l *rollingLog) errorText() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastError == nil {
		return ""
	}
	return l.lastError.Error()
}
