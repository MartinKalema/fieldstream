package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testReport() []byte {
	return []byte(`{"schemaVersion":1,"completedAt":"2026-09-11T23:05:52.306Z","browser":"test","route":"local","zeroTargetPosition":"right","method":"test","measuredSeconds":30,"comparisonUsable":false,"issues":[],"limits":[],"lanes":[{"position":"left","mode":"browser-default","controls":{},"connectionErrors":0,"summary":{},"frameCallbacks":{},"intervals":[]},{"position":"right","mode":"zero-request","controls":{},"connectionErrors":0,"summary":{},"frameCallbacks":{},"intervals":[]}]}`)
}

func sendReport(h http.Handler, body []byte, contentType, origin, query string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://127.0.0.1:19081/report"+query, bytes.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("Origin", origin)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestReportSavedPrivatelyWithServerChosenName(t *testing.T) {
	root := t.TempDir()
	h := handler(19081, []string{"camera-01"}, root)
	w := sendReport(h, testReport(), "application/json", "http://127.0.0.1:19081", "")
	if w.Code != 201 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		StoredPath string `json:"storedPath"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(response.StoredPath) != filepath.Join(root, "reports") || !strings.HasPrefix(filepath.Base(response.StoredPath), "browser-buffer-") || filepath.Ext(response.StoredPath) != ".json" {
		t.Fatal("server chose an unexpected destination")
	}
	info, err := os.Stat(response.StoredPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("report was not created privately")
	}
	data, err := os.ReadFile(response.StoredPath)
	if err != nil || !bytes.Equal(bytes.TrimSpace(data), testReport()) {
		t.Fatal("full report contents were not preserved")
	}
}

func TestReportRejectsOriginTypeSizeAndClientDestination(t *testing.T) {
	root := t.TempDir()
	h := handler(19081, []string{"camera-01"}, root)
	for _, tc := range []struct {
		body                       []byte
		contentType, origin, query string
		code                       int
	}{
		{testReport(), "application/json", "http://foreign.invalid", "", 403},
		{testReport(), "application/json", "", "", 403},
		{testReport(), "text/plain", "http://127.0.0.1:19081", "", 415},
		{testReport(), "application/json", "http://127.0.0.1:19081", "?path=elsewhere", 400},
		{append(testReport(), bytes.Repeat([]byte(" "), reportSizeLimit)...), "application/json", "http://127.0.0.1:19081", "", 413},
		{[]byte(`{"path":"elsewhere"}`), "application/json", "http://127.0.0.1:19081", "", 400},
		{append(testReport(), []byte(` {}`)...), "application/json", "http://127.0.0.1:19081", "", 400},
	} {
		w := sendReport(h, tc.body, tc.contentType, tc.origin, tc.query)
		if w.Code != tc.code {
			t.Fatalf("rejection: got %d, want %d", w.Code, tc.code)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "reports")); !os.IsNotExist(err) {
		t.Fatal("rejected requests created a reports directory")
	}
}

func TestReportRejectsEscapingReportsSymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "reports")); err != nil {
		t.Fatal(err)
	}
	w := sendReport(handler(19081, []string{"camera-01"}, root), testReport(), "application/json", "http://127.0.0.1:19081", "")
	if w.Code != 500 {
		t.Fatalf("symlink save: %d", w.Code)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("report escaped the project")
	}
}
