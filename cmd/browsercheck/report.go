package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const reportSizeLimit = 1 << 20

type browserReport struct {
	SchemaVersion      int           `json:"schemaVersion"`
	CompletedAt        string        `json:"completedAt"`
	Browser            string        `json:"browser"`
	Route              string        `json:"route"`
	ZeroTargetPosition string        `json:"zeroTargetPosition"`
	Method             string        `json:"method"`
	MeasuredSeconds    float64       `json:"measuredSeconds"`
	ComparisonUsable   *bool         `json:"comparisonUsable"`
	Issues             []string      `json:"issues"`
	Limits             []string      `json:"limits"`
	Lanes              []browserLane `json:"lanes"`
}

type browserLane struct {
	Position                   string                       `json:"position"`
	Mode                       string                       `json:"mode"`
	Controls                   map[string]json.RawMessage   `json:"controls"`
	ConnectionErrors           int                          `json:"connectionErrors"`
	Summary                    map[string]*float64          `json:"summary"`
	FrameCallbacks             map[string]json.RawMessage   `json:"frameCallbacks"`
	Intervals                  []map[string]json.RawMessage `json:"intervals"`
	LastDiagnosticsBeforeClose map[string]json.RawMessage   `json:"lastDiagnosticsBeforeClose"`
	DiagnosticSamples          []map[string]json.RawMessage `json:"diagnosticSamples"`
}

func validateReport(data []byte) error {
	var report browserReport
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&report); err != nil {
		return errors.New("invalid report schema")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("expected one JSON report")
	}
	if report.SchemaVersion != 1 || report.ComparisonUsable == nil || report.MeasuredSeconds < 0 || report.MeasuredSeconds > 120 || math.IsNaN(report.MeasuredSeconds) || math.IsInf(report.MeasuredSeconds, 0) || (report.Route != "local" && report.Route != "forwarded") || (report.ZeroTargetPosition != "left" && report.ZeroTargetPosition != "right") || len(report.Lanes) != 2 {
		return errors.New("invalid report fields")
	}
	if _, err := time.Parse(time.RFC3339Nano, report.CompletedAt); err != nil {
		return errors.New("invalid completion time")
	}
	if len(report.Browser) > 1024 || len(report.Method) > 2048 || len(report.Issues) > 100 || len(report.Limits) > 20 {
		return errors.New("report field limit exceeded")
	}
	positions, modes := map[string]bool{}, map[string]bool{}
	for _, lane := range report.Lanes {
		if (lane.Position != "left" && lane.Position != "right") || positions[lane.Position] || (lane.Mode != "browser-default" && lane.Mode != "zero-request") || modes[lane.Mode] || lane.Summary == nil || lane.ConnectionErrors < 0 || len(lane.Intervals) > 100 || len(lane.DiagnosticSamples) > 100 {
			return errors.New("invalid report lane")
		}
		positions[lane.Position], modes[lane.Mode] = true, true
		if lane.Mode == "zero-request" && lane.Position != report.ZeroTargetPosition {
			return errors.New("inconsistent zero-target position")
		}
		for kind := range lane.Controls {
			if kind != "video" && kind != "audio" {
				return errors.New("invalid receiver kind")
			}
		}
	}
	// Bound nested diagnostic objects even when future browser versions expose
	// different numeric counters. These objects cannot choose a destination path.
	var tree any
	if json.Unmarshal(data, &tree) != nil {
		return errors.New("invalid JSON report")
	}
	return boundedReportValue(tree, 0)
}

func boundedReportValue(value any, depth int) error {
	if depth > 10 {
		return errors.New("report nesting limit exceeded")
	}
	switch v := value.(type) {
	case map[string]any:
		if len(v) > 100 {
			return errors.New("too many report fields")
		}
		for key, child := range v {
			if len(key) > 80 {
				return errors.New("report key too long")
			}
			if err := boundedReportValue(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if len(v) > 100 {
			return errors.New("too many report samples")
		}
		for _, child := range v {
			if err := boundedReportValue(child, depth+1); err != nil {
				return err
			}
		}
	case string:
		if len(v) > 4096 {
			return errors.New("report text too long")
		}
	}
	return nil
}

func writePrivateReport(projectRoot string, data []byte) (string, error) {
	project, err := os.OpenRoot(projectRoot)
	if err != nil {
		return "", err
	}
	defer project.Close()
	if err := project.Mkdir("reports", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := project.Lstat("reports")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("reports must be a real directory")
	}
	directory, err := project.OpenRoot("reports")
	if err != nil {
		return "", err
	}
	defer directory.Close()
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	name := "browser-buffer-" + time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(token[:]) + ".json"
	file, err := directory.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	succeeded := false
	defer func() {
		_ = file.Close()
		if !succeeded {
			_ = directory.Remove(name)
		}
	}()
	if _, err = file.Write(data); err != nil {
		return "", err
	}
	if _, err = file.Write([]byte("\n")); err != nil {
		return "", err
	}
	if err = file.Sync(); err != nil {
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	succeeded = true
	return filepath.Join(projectRoot, "reports", name), nil
}

func reportSaver(projectRoot string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.RawQuery != "" {
			http.Error(w, "Report destination cannot be supplied.", http.StatusBadRequest)
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" || r.Header.Get("Content-Encoding") != "" {
			http.Error(w, "Send an uncompressed application/json report.", http.StatusUnsupportedMediaType)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, reportSizeLimit))
		if err != nil {
			http.Error(w, "Report exceeds the 1 MiB limit or could not be read.", http.StatusRequestEntityTooLarge)
			return
		}
		if err := validateReport(data); err != nil {
			http.Error(w, fmt.Sprintf("Report rejected: %s.", err), http.StatusBadRequest)
			return
		}
		path, err := writePrivateReport(projectRoot, data)
		if err != nil {
			http.Error(w, "Could not save the private report in the project reports directory.", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"storedPath": path})
	})
}
