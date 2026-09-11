package lab

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var sourceSessionPattern = regexp.MustCompile(`^([a-z][a-z0-9-]{0,31})-[0-9]{8}-[0-9]{6}-[a-f0-9]{24}$`)
var legacySessionPattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[a-f0-9]{24}$`)

func recordingSource(directory string) (string, error) {
	name := filepath.Base(directory)
	match := sourceSessionPattern.FindStringSubmatch(name)
	identity := struct {
		SourceID string `json:"source_id"`
	}{}
	err := readJSON(filepath.Join(directory, ".source.json"), &identity)
	if os.IsNotExist(err) {
		if len(match) == 2 {
			return match[1], nil
		}
		if legacySessionPattern.MatchString(name) {
			return "camera-01", nil
		}
		return "", errors.New("source identity is missing")
	}
	if err != nil {
		return "", errors.New("source identity cannot be read")
	}
	if validateSourceName(identity.SourceID, identity.SourceID) != nil {
		return "", errors.New("source identity is invalid")
	}
	if len(match) == 2 && match[1] != identity.SourceID {
		return "", errors.New("source identity disagrees with its recording folder")
	}
	return identity.SourceID, nil
}

// Only files that FFmpeg has finalized in its CSV list enter the catalog.
// A partial CSV tail is retried on the next scan, and never treated as a file.
func scanRecordings(ctx context.Context, p Paths, known map[string]Segment, catalog *Catalog) (RecordingState, error) {
	result := RecordingState{}
	files, err := filepath.Glob(filepath.Join(p.Recordings, "*", "*.mp4"))
	if err != nil {
		return result, err
	}
	var allocated int64
	present := map[string]bool{}
	for _, name := range files {
		info, e := os.Lstat(name)
		if e != nil {
			if os.IsNotExist(e) {
				continue
			}
			return result, e
		}
		if !info.Mode().IsRegular() {
			continue
		}
		allocated += info.Size()
		if rel, e := filepath.Rel(p.Recordings, name); e == nil {
			present[rel] = true
		}
	}
	for rel := range known {
		if !present[rel] {
			delete(known, rel)
		}
	}
	lists, err := filepath.Glob(filepath.Join(p.Recordings, "*", "segments.csv"))
	if err != nil {
		return result, err
	}
	for _, list := range lists {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		parent, e := os.Lstat(filepath.Dir(list))
		if e != nil || !parent.IsDir() {
			continue
		}
		f, e := os.Open(list)
		if e != nil {
			result.Warnings = append(result.Warnings, filepath.Base(filepath.Dir(list))+": completed recording list cannot be read")
			continue
		}
		reader := bufio.NewReaderSize(f, 16<<10)
		sourceID, e := recordingSource(filepath.Dir(list))
		if e != nil {
			f.Close()
			result.Warnings = append(result.Warnings, filepath.Base(filepath.Dir(list))+": "+e.Error()+"; these files were kept but skipped")
			continue
		}
		warnRow := func(message string) {
			if len(result.Warnings) < 20 {
				result.Warnings = append(result.Warnings, filepath.Base(filepath.Dir(list))+": "+message)
			}
		}
		for {
			line, e := reader.ReadSlice('\n')
			if errors.Is(e, bufio.ErrBufferFull) {
				warnRow("oversized completed recording row was skipped")
				// Discard only this oversized row, in bounded chunks. Its tail
				// must never be interpreted as a fresh completed-file entry.
				for errors.Is(e, bufio.ErrBufferFull) {
					if contextErr := ctx.Err(); contextErr != nil {
						f.Close()
						return result, contextErr
					}
					_, e = reader.ReadSlice('\n')
				}
				if e != nil {
					break
				}
				continue
			}
			if e != nil {
				if !errors.Is(e, io.EOF) {
					warnRow("completed recording list could not be read fully")
				}
				break
			} // Never commit a partial final line, even if its fields parse.
			row, e := csv.NewReader(strings.NewReader(string(line))).Read()
			if e != nil {
				warnRow("malformed completed recording row was skipped")
				continue
			}
			if len(row) != 3 {
				warnRow("malformed completed recording row was skipped")
				continue
			}
			path := row[0]
			if !filepath.IsAbs(path) {
				path = filepath.Join(filepath.Dir(list), path)
			}
			path = filepath.Clean(path)
			// Finalized entries belong to the same session as the CSV file.
			if filepath.Dir(path) != filepath.Dir(list) || filepath.Ext(path) != ".mp4" {
				continue
			}
			rel, e := filepath.Rel(p.Recordings, path)
			if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				continue
			}
			if _, ok := known[rel]; ok {
				continue
			}
			begin, e1 := strconv.ParseFloat(row[1], 64)
			end, e2 := strconv.ParseFloat(row[2], 64)
			if e1 != nil || e2 != nil || math.IsNaN(begin) || math.IsNaN(end) || math.IsInf(begin, 0) || math.IsInf(end, 0) || end <= begin {
				continue
			}
			info, e := os.Lstat(path)
			if e != nil || !info.Mode().IsRegular() {
				continue
			}
			media, e := os.Open(path)
			if e != nil {
				continue
			}
			h := sha256.New()
			size, e := io.Copy(h, &recordingReader{ctx: ctx, reader: media})
			if e == nil {
				e = media.Sync()
			}
			media.Close()
			if e != nil {
				f.Close()
				return result, e
			}
			// Commit the file and its directory entry before the catalog claims it.
			for _, directory := range []string{filepath.Dir(path), p.Recordings} {
				dir, openErr := os.Open(directory)
				if openErr != nil {
					f.Close()
					return result, openErr
				}
				syncErr := dir.Sync()
				dir.Close()
				if syncErr != nil {
					f.Close()
					return result, syncErr
				}
			}
			segment := Segment{SourceID: sourceID, Path: rel, Session: filepath.Base(filepath.Dir(list)), Bytes: size, SHA256: hex.EncodeToString(h.Sum(nil)), Duration: end - begin}
			// Cache only a successful transaction, so a failed write is retried.
			if e := catalog.UpsertSegment(ctx, segment); e != nil {
				if errors.Is(e, ErrRecordingChanged) {
					result.Warnings = append(result.Warnings, rel+": finished recording changed; the original catalog entry was kept")
					continue
				}
				f.Close()
				return result, e
			}
			known[rel] = segment
		}
		f.Close()
	}
	if err := catalog.ReconcileMissing(ctx); err != nil {
		return result, err
	}
	summary, err := catalog.Summary(ctx)
	if err != nil {
		return result, err
	}
	result.Segments, result.Bytes = summary.LocalSegments, summary.LocalBytes
	free, err := freeBytes(p.Recordings)
	if err != nil {
		return result, err
	}
	reason := ""
	if allocated >= MaxRecordingBytes {
		reason = "The 2 GB recording limit has been reached. Move saved recordings elsewhere to continue."
	} else if free < MinFreeBytes {
		reason = "Less than 1 GB of disk space is free. Recording is paused."
	}
	if reason != "" {
		result.BlockedReason = &reason
	}
	return result, nil
}

type recordingReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *recordingReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (m *recordingMonitor) snapshotArchive() ArchiveState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.archive
}

func (m *recordingMonitor) snapshotSource(id string) RecordingState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state := m.sourceStates[id]
	state.BlockedReason = m.state.BlockedReason
	return state
}

func (m *recordingMonitor) run(ctx context.Context, p Paths) {
	defer close(m.done)
	var catalog *Catalog
	var archiver *Archiver
	var archiveDone chan struct{}
	var archiveStarted bool
	var archiveConfig *R2Config
	var archiveRestartAt, archiveRunStarted time.Time
	archiveRestartDelay := 2 * time.Second
	known := map[string]Segment{}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	defer func() {
		if archiveDone != nil {
			<-archiveDone
		}
		if catalog != nil {
			catalog.Close()
		}
	}()
	for {
		var err error
		if catalog == nil {
			catalog, err = OpenCatalog(p)
			if err == nil {
				for offset := 0; ; offset += 500 {
					items, e := catalog.ListSegments(ctx, 500, offset)
					if e != nil {
						err = e
						break
					}
					for _, item := range items {
						if !item.Missing {
							if _, e := os.Stat(filepath.Join(p.Recordings, item.Path)); e == nil {
								known[item.Path] = item.Segment
							}
						}
					}
					if len(items) < 500 {
						break
					}
				}
			}
		}
		var archiveState ArchiveState
		if catalog != nil && !archiveStarted {
			config, configErr := LoadR2Config(p)
			if configErr != nil {
				archiveState.LastError = configErr.Error()
			} else if config != nil {
				archiveState.Configured, archiveState.Enabled = true, config.Enabled
				if config.Enabled {
					archiveConfig = config
				}
			}
			m.mu.Lock()
			m.archive = archiveState
			m.mu.Unlock()
			archiveStarted = true
		}
		if archiveDone != nil {
			select {
			case <-archiveDone:
				archiveDone = nil
				if time.Since(archiveRunStarted) >= 30*time.Second {
					archiveRestartDelay = 2 * time.Second
				}
				archiveRestartAt = time.Now().Add(archiveRestartDelay)
				archiveRestartDelay *= 2
				if archiveRestartDelay > 30*time.Second {
					archiveRestartDelay = 30 * time.Second
				}
			default:
			}
		}
		if archiveConfig != nil && archiveDone == nil && ctx.Err() == nil && !time.Now().Before(archiveRestartAt) {
			candidate, startErr := NewArchiver(p, catalog, *archiveConfig)
			if startErr != nil {
				archiveState = m.snapshotArchive()
				archiveState.LastError = startErr.Error()
				m.mu.Lock()
				m.archive = archiveState
				m.mu.Unlock()
				archiveRestartAt = time.Now().Add(30 * time.Second)
			} else {
				archiver = candidate
				archiveRunStarted = time.Now()
				archiveDone = make(chan struct{})
				// Capture this attempt's values: a later retry must not change which
				// completion channel the finishing goroutine closes.
				go func(worker *Archiver, done chan struct{}) {
					defer close(done)
					_ = worker.Run(ctx)
				}(archiver, archiveDone)
			}
		}
		var state RecordingState
		if err == nil {
			scanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			state, err = scanRecordings(scanCtx, p, known, catalog)
			cancel()
		}
		if catalog != nil {
			queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			if summaries, e := catalog.SummariesBySource(queryCtx); e == nil {
				bySource := map[string]RecordingState{}
				for id, summary := range summaries {
					bySource[id] = RecordingState{Segments: summary.LocalSegments, Bytes: summary.LocalBytes}
				}
				m.mu.Lock()
				m.sourceStates = bySource
				m.mu.Unlock()
			}
			cancel()
		}
		if err != nil {
			state = m.snapshot()
			reason := "Recording storage is unavailable: " + err.Error()
			state.BlockedReason = &reason
		}
		if archiver != nil {
			archiveState = archiver.Snapshot()
		} else {
			archiveState = m.snapshotArchive()
		}
		if catalog != nil {
			queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			if summary, e := catalog.Summary(queryCtx); e == nil {
				archiveState.Summary = summary
			}
			cancel()
		}
		m.mu.Lock()
		m.state, m.archive = state, archiveState
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
