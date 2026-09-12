package lab

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func checksumOf(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func TestDownloadResumesInterruptedResponse(t *testing.T) {
	content := []byte("complete verified release archive")
	cut := 11
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			if got := r.Header.Get("Range"); got != "" {
				t.Errorf("initial request had a range: %q", got)
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			w.Write(content[:cut]) // A truncated body produces unexpected EOF in the client.
			return
		}
		if got, want := r.Header.Get("Range"), fmt.Sprintf("bytes=%d-", cut); got != want {
			t.Errorf("resume position = %q, want %q", got, want)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", cut, len(content)-1, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(content[cut:])
	}))
	defer server.Close()
	partial := filepath.Join(t.TempDir(), "release.part")
	if err := resumableDownload(context.Background(), server.Client(), server.URL, partial, checksumOf(content)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(partial)
	if err != nil || !bytes.Equal(got, content) || requests != 2 {
		t.Fatalf("resumed archive incorrect: data=%q requests=%d err=%v", got, requests, err)
	}
}

func TestDownloadRejectsWrongRangeWithoutChangingPrefix(t *testing.T) {
	for _, contentRange := range []string{"bytes 1-5/6", "bytes 3-2/6", "bytes 3-5/6 extra", "bytes 3-5/999999999999999999999999"} {
		t.Run(contentRange, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Range", contentRange)
				w.WriteHeader(http.StatusPartialContent)
				w.Write([]byte("def"))
			}))
			defer server.Close()
			partial := filepath.Join(t.TempDir(), "release.part")
			os.WriteFile(partial, []byte("abc"), 0600)
			if err := downloadAttempt(context.Background(), server.Client(), server.URL, partial); err == nil {
				t.Fatal("accepted an invalid range")
			}
			if data, _ := os.ReadFile(partial); string(data) != "abc" {
				t.Fatalf("modified saved prefix: %q", data)
			}
		})
	}
}

func TestDownloadReplacesWhenServerIgnoresRange(t *testing.T) {
	content := []byte("whole archive")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer server.Close()
	partial := filepath.Join(t.TempDir(), "release.part")
	os.WriteFile(partial, []byte("wrong prefix"), 0600)
	if err := downloadAttempt(context.Background(), server.Client(), server.URL, partial); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(partial); !bytes.Equal(data, content) {
		t.Fatalf("did not replace old bytes: %q", data)
	}
}

func TestDownloadQuarantinesWrongChecksum(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("corrupt release"))
	}))
	defer server.Close()
	partial := filepath.Join(t.TempDir(), "release.part")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := resumableDownload(ctx, server.Client(), server.URL, partial, checksumOf([]byte("expected release"))); err == nil {
		t.Fatal("accepted wrong checksum")
	}
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatal("bad bytes remained resumable")
	}
	if data, err := os.ReadFile(partial + ".invalid"); err != nil || string(data) != "corrupt release" {
		t.Fatalf("invalid archive was not quarantined: %q %v", data, err)
	}
}

func TestDownloadUsesVerifiedOfflineCopy(t *testing.T) {
	partial := filepath.Join(t.TempDir(), "release.part")
	content := []byte("verified release")
	os.WriteFile(partial, content, 0600)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := resumableDownload(ctx, http.DefaultClient, "http://127.0.0.1:1", partial, checksumOf(content)); err != nil {
		t.Fatalf("cached archive unexpectedly required network: %v", err)
	}
}

func TestDownloadRejectsOversizedAndSymlinkTargets(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	partial := filepath.Join(dir, "release.part")
	os.WriteFile(target, []byte("keep"), 0600)
	if err := os.Symlink(target, partial); err != nil {
		t.Fatal(err)
	}
	if err := downloadAttempt(context.Background(), http.DefaultClient, "http://127.0.0.1:1", partial); err == nil || !strings.Contains(err.Error(), "partial archive") {
		t.Fatalf("symlink was not rejected before HTTP: %v", err)
	}
	os.Remove(partial)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(maxArchiveBytes+1))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := downloadAttempt(context.Background(), server.Client(), server.URL, partial); err == nil {
		t.Fatal("oversized archive accepted")
	}
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatal("oversized archive created a partial file")
	}
}

type archiveEntry struct {
	name, content string
	kind          byte
}

func makeTestArchive(t *testing.T, entries []archiveEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "release.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		size := int64(len(entry.content))
		if entry.kind == tar.TypeSymlink {
			size = 0
		}
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Size: size, Mode: 0755, Typeflag: entry.kind, Linkname: entry.content}); err != nil {
			t.Fatal(err)
		}
		if size > 0 {
			if _, err := tw.Write([]byte(entry.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, close := range []func() error{tw.Close, gz.Close, f.Close} {
		if err := close(); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestExtractionOnlyWritesApprovedFiles(t *testing.T) {
	archive := makeTestArchive(t, []archiveEntry{{"../escape", "escape", tar.TypeReg}, {"mediamtx.yml", "unsafe default configuration", tar.TypeReg}, {"./mediamtx", "binary", tar.TypeReg}, {"LICENSE", "license", tar.TypeReg}})
	dir := t.TempDir()
	destination := filepath.Join(dir, "installed")
	if err := extractMediaMTX(archive, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "escape")); !os.IsNotExist(err) {
		t.Fatal("archive escaped extraction directory")
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 2 {
		t.Fatalf("unexpected extracted files: %v %v", entries, err)
	}
	info, err := os.Stat(filepath.Join(destination, "mediamtx"))
	if err != nil || info.Mode().Perm()&0111 == 0 {
		t.Fatal("installed binary is not executable")
	}
}

func TestInvalidExtractionPreservesExistingBinary(t *testing.T) {
	for name, entries := range map[string][]archiveEntry{
		"symlink":         {{"mediamtx", "/tmp/escape", tar.TypeSymlink}, {"LICENSE", "license", tar.TypeReg}},
		"duplicate":       {{"mediamtx", "first", tar.TypeReg}, {"mediamtx", "second", tar.TypeReg}, {"LICENSE", "license", tar.TypeReg}},
		"missing license": {{"mediamtx", "replacement", tar.TypeReg}},
	} {
		t.Run(name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "installed")
			os.Mkdir(destination, 0700)
			os.WriteFile(filepath.Join(destination, "mediamtx"), []byte("keep working binary"), 0700)
			if err := extractMediaMTX(makeTestArchive(t, entries), destination); err == nil {
				t.Fatal("accepted invalid archive")
			}
			if data, _ := os.ReadFile(filepath.Join(destination, "mediamtx")); string(data) != "keep working binary" {
				t.Fatal("invalid archive replaced existing installation")
			}
		})
	}
}

func validTestSettings() Settings {
	return Settings{Host: "192.168.1.20", PublisherUser: "ipad", PublisherPassword: strings.Repeat("a", 32), PublishPassphrase: strings.Repeat("b", 32), RelayUser: "relay", RelayPassword: strings.Repeat("c", 32), CentralPassphrase: strings.Repeat("d", 32), Version: mediaMTXVersion, FFmpeg: "/test/ffmpeg", FFprobe: "/test/ffprobe"}
}

func TestSettingsRejectMissingOrUnsafeValues(t *testing.T) {
	if err := validTestSettings().validate(); err != nil {
		t.Fatal(err)
	}
	for name, alter := range map[string]func(*Settings){
		"unspecified bind":    func(s *Settings) { s.Host = "0.0.0.0" },
		"multicast bind":      func(s *Settings) { s.Host = "224.0.0.1" },
		"unknown version":     func(s *Settings) { s.Version = "latest" },
		"anonymous publisher": func(s *Settings) { s.PublisherUser = "any" },
		"URL injection":       func(s *Settings) { s.RelayPassword = "secret&mode=listener" },
		"missing passphrase":  func(s *Settings) { s.PublishPassphrase = "" },
		"relative command":    func(s *Settings) { s.FFmpeg = "ffmpeg" },
		"stream ID injection": func(s *Settings) { s.PublisherUser = "a:b" },
	} {
		t.Run(name, func(t *testing.T) {
			s := validTestSettings()
			alter(&s)
			if err := s.validate(); err == nil {
				t.Fatal("accepted unsafe settings")
			}
		})
	}
}

func TestPrivateSettingsAndAtomicReplacement(t *testing.T) {
	p := NewPaths(t.TempDir())
	path := filepath.Join(p.Local, "settings.json")
	settings := validTestSettings()
	if err := AtomicJSON(path, settings); err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{p.Local, path} {
		info, err := os.Stat(item)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("not private: %s %v", item, err)
		}
	}
	expected := settings
	expected.Sources = GetSources(settings)
	if got, err := p.LoadSettings(); err != nil || !reflect.DeepEqual(got, expected) {
		t.Fatalf("settings round trip failed: %v", err)
	}
	if err := AtomicJSON(path, make(chan bool)); err == nil {
		t.Fatal("expected failed serialization")
	}
	if got, err := p.LoadSettings(); err != nil || !reflect.DeepEqual(got, expected) {
		t.Fatal("failed replacement damaged previous settings")
	}
	os.Chmod(path, 0644)
	if _, err := p.LoadSettings(); err == nil {
		t.Fatal("accepted publicly readable passwords")
	}
}

func TestConfigurationLimitsNetworkAccess(t *testing.T) {
	s := validTestSettings()
	for _, central := range []bool{false, true} {
		config := serverConfig(s, central)
		for _, key := range []string{"apiAddress", "metricsAddress", "rtspAddress", "webrtcAddress", "webrtcLocalUDPAddress"} {
			host, _, err := net.SplitHostPort(config[key].(string))
			if err != nil || !net.ParseIP(host).IsLoopback() {
				t.Errorf("%s exposed beyond loopback: %v", key, config[key])
			}
		}
		users := config["authInternalUsers"].([]any)
		for _, entry := range users {
			user := entry.(map[string]any)
			for _, item := range user["permissions"].([]any) {
				permission := item.(map[string]any)
				if permission["action"] == "publish" && (user["user"] == "any" || permission["path"] != "camera-01" || user["pass"] == "") {
					t.Fatal("unrestricted or anonymous publishing enabled")
				}
			}
			if user["user"] == "any" && len(user["ips"].([]string)) == 0 {
				t.Fatal("anonymous viewer or admin exposed to all networks")
			}
		}
		if config["pathDefaults"].(map[string]any)["overridePublisher"] != false {
			t.Fatal("second publisher can replace a running camera")
		}
	}
}

func TestSourceSetupRendersCurrentSettings(t *testing.T) {
	// Rendering must also work when the installed executable has no source tree.
	t.Chdir(t.TempDir())
	settings := validTestSettings()
	settings.Host = "192.0.2.10"
	settings.PublisherUser = "camera_2"
	settings.PublisherPassword = strings.Repeat("e", 32)
	settings.PublishPassphrase = strings.Repeat("f", 32)
	source := GetSources(settings)[0]
	source.ID, source.Label = "hall-camera", "Hall camera"
	guide, err := renderSourceSetup(settings, source)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		fmt.Sprintf("Connection URL:\nsrt://%s:%d\n", settings.Host, Field.SRT),
		"Host / address: " + settings.Host + "\n",
		fmt.Sprintf("Port: %d\n", Field.SRT),
		"Stream ID: publish:" + source.ID + ":" + source.PublisherUser + ":" + source.PublisherPassword + "\n",
		"Encryption passphrase: " + settings.PublishPassphrase + "\n",
		"Local picture: ./lab --source " + source.ID + " view local\n",
		"Forwarded picture: ./lab --source " + source.ID + " view forwarded\n",
		fmt.Sprintf("Local: http://127.0.0.1:%d/%s\n", Field.Web, source.ID),
		fmt.Sprintf("Forwarded: http://127.0.0.1:%d/%s\n", Central.Web, source.ID),
	} {
		if !strings.Contains(guide, expected) {
			t.Error("a connection field did not match the supplied settings")
		}
	}
	for _, unexpected := range []string{"{{", "}}", "%!", "%s", "%d", "<no value>", settings.RelayPassword, settings.CentralPassphrase} {
		if strings.Contains(guide, unexpected) {
			t.Error("guide contains an unresolved placeholder or unrelated relay secret")
		}
	}
	path := filepath.Join(t.TempDir(), "source-setup.txt")
	if err := privateWrite(path, []byte(guide)); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("rendered credentials were not saved privately")
	}
}

func TestAddSourceMigratesCredentialsAndWritesSeparatePrivateGuides(t *testing.T) {
	p := NewPaths(t.TempDir())
	legacy := validTestSettings()
	if err := AtomicJSON(filepath.Join(p.Local, "settings.json"), legacy); err != nil {
		t.Fatal(err)
	}
	if err := p.AddSource("camera-02", "Second camera"); err != nil {
		t.Fatal(err)
	}
	settings, err := p.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Sources) != 2 || settings.Sources[0].ID != "camera-01" || settings.Sources[0].PublisherPassword != legacy.PublisherPassword || settings.Sources[0].PublishPassphrase != legacy.PublishPassphrase {
		t.Fatal("migration changed the existing source identity or credentials")
	}
	first, second := settings.Sources[0], settings.Sources[1]
	if first.PublisherUser == second.PublisherUser || first.PublisherPassword == second.PublisherPassword || first.PublishPassphrase == second.PublishPassphrase {
		t.Fatal("sources share their publishing credentials")
	}
	for i, source := range settings.Sources {
		guidePath := filepath.Join(p.Local, "connections", source.ID+".txt")
		guide, err := os.ReadFile(guidePath)
		if err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(guidePath); err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("source guide is not private")
		}
		other := settings.Sources[1-i]
		if !strings.Contains(string(guide), "publish:"+source.ID+":"+source.PublisherUser+":"+source.PublisherPassword) || strings.Contains(string(guide), other.PublisherPassword) || strings.Contains(string(guide), other.PublishPassphrase) {
			t.Fatal("source guide has missing or mixed credentials")
		}
	}
	index, err := os.ReadFile(filepath.Join(p.Root, "SOURCES.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range settings.Sources {
		if !strings.Contains(string(index), source.ID+".txt") || strings.Contains(string(index), source.PublisherPassword) || strings.Contains(string(index), source.PublishPassphrase) {
			t.Fatal("source index is missing a guide or contains credentials")
		}
	}
	for _, sourceID := range []string{"camera-03", "camera-04"} {
		if err := p.AddSource(sourceID, sourceID); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(filepath.Join(p.Local, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.AddSource("camera-05", "Fifth camera"); err == nil {
		t.Fatal("source limit was not enforced")
	}
	after, _ := os.ReadFile(filepath.Join(p.Local, "settings.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("rejected registration changed saved sources")
	}
}

func TestSourcesCannotPublishToEachOthersPaths(t *testing.T) {
	s := validTestSettings()
	s.Sources = GetSources(s)
	second, err := newSource("camera-02", "Second camera")
	if err != nil {
		t.Fatal(err)
	}
	s.Sources = append(s.Sources, second)
	for _, central := range []bool{false, true} {
		config := serverConfig(s, central)
		paths := config["paths"].(map[string]any)
		if len(paths) != 2 {
			t.Fatal("configured source was omitted from server paths")
		}
		for _, source := range s.Sources {
			path, ok := paths[source.ID].(map[string]any)
			phrase := source.PublishPassphrase
			if central {
				phrase = s.CentralPassphrase
			}
			if !ok || path["srtPublishPassphrase"] != phrase {
				t.Fatal("path has the wrong encryption passphrase")
			}
		}
		for _, entry := range config["authInternalUsers"].([]any) {
			user := entry.(map[string]any)
			if user["user"] == "any" {
				continue
			}
			permissions := user["permissions"].([]any)
			if central {
				if user["user"] != s.RelayUser || len(permissions) != 2 || len(user["ips"].([]string)) == 0 {
					t.Fatal("central publisher does not restrict relay access to registered paths and localhost")
				}
				continue
			}
			if len(permissions) != 1 {
				t.Fatal("source publisher can access multiple paths")
			}
			permission := permissions[0].(map[string]any)
			found := false
			for _, source := range s.Sources {
				if user["user"] == source.PublisherUser {
					found = true
					if permission["action"] != "publish" || permission["path"] != source.ID || user["pass"] != source.PublisherPassword {
						t.Fatal("source credentials authorize a different path")
					}
				}
			}
			if !found {
				t.Fatal("unexpected publisher authorized")
			}
		}
	}
}

func TestSourceRegistryRejectsDuplicateAndUnsafeNames(t *testing.T) {
	for _, item := range []struct{ id, label string }{
		{"../other", "Camera"}, {"Camera", "Camera"}, {"camera_1", "Camera"}, {"camera-01", "Line one\nLine two"}, {"camera-01", strings.Repeat("x", 81)},
	} {
		if err := validateSourceName(item.id, item.label); err == nil {
			t.Error("accepted an unsafe source ID or label")
		}
	}
	s := validTestSettings()
	s.Sources = GetSources(s)
	s.Sources = append(s.Sources, s.Sources[0])
	if err := s.validate(); err == nil {
		t.Fatal("duplicate source registry accepted")
	}
	s.Sources[1].ID = "camera-02"
	if err := s.validate(); err == nil {
		t.Fatal("duplicate publisher identity accepted")
	}
}
