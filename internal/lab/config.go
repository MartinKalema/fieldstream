package lab

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"text/template"
	"time"
	"unicode"
	"unicode/utf8"
)

const mediaMTXVersion = "1.21.0"
const maxArchiveBytes int64 = 100 << 20
const MaxSources = 4

// Source: the official v1.21.0 release's checksums.sha256, verified 2026-09-11.
var releaseChecksums = map[string]string{
	"darwin_arm64": "159b8e8164022189e654b61ee8bb7edf2d3ff9354ff06cf4c315bb59f5cc9eca",
	"darwin_amd64": "2ee772efb7e2e365307599e41f849c54539f6019a2ebd4563be9b1256edfeb65",
	"linux_arm64":  "a8113b5928ba1a934b81557b61b8a07954b76921a4b567d54c7f086f8b39d9a2",
	"linux_amd64":  "e02e34c3337a35f20ac9e5aa31524566108964e6e37dbc46cf8292169f6c792b",
}

type Paths struct{ Root, Local, Tools, Recordings, Reports string }

func NewPaths(root string) Paths {
	if absolute, err := filepath.Abs(root); err == nil {
		root = absolute
	}
	return Paths{Root: root, Local: filepath.Join(root, ".local"), Tools: filepath.Join(root, ".tools"),
		Recordings: filepath.Join(root, "recordings"), Reports: filepath.Join(root, "reports")}
}

type Ports struct{ SRT, RTSP, API, Metrics int }

var Field = Ports{SRT: 18890, RTSP: 18554, API: 19997, Metrics: 19998}
var Central = Ports{SRT: 28890, RTSP: 28554, API: 29997, Metrics: 29998}

type Settings struct {
	Host    string         `json:"host"`
	Sources []SourceConfig `json:"sources,omitempty"`
	// Legacy fields are read only to migrate an existing single-source setup.
	PublisherUser     string `json:"publisher_user,omitempty"`
	PublisherPassword string `json:"publisher_password,omitempty"`
	PublishPassphrase string `json:"publish_passphrase,omitempty"`
	RelayUser         string `json:"relay_user"`
	RelayPassword     string `json:"relay_password"`
	CentralPassphrase string `json:"central_passphrase"`
	Version           string `json:"version"`
	FFmpeg            string `json:"ffmpeg"`
	FFprobe           string `json:"ffprobe"`
}

type SourceConfig struct {
	ID                string `json:"id"`
	Label             string `json:"label"`
	PublisherUser     string `json:"publisher_user"`
	PublisherPassword string `json:"publisher_password"`
	PublishPassphrase string `json:"publish_passphrase"`
}

// GetSources preserves existing credentials while giving legacy setups a neutral ID.
func GetSources(s Settings) []SourceConfig {
	if len(s.Sources) > 0 {
		return append([]SourceConfig(nil), s.Sources...)
	}
	return []SourceConfig{{ID: "camera-01", Label: "Camera 1", PublisherUser: s.PublisherUser, PublisherPassword: s.PublisherPassword, PublishPassphrase: s.PublishPassphrase}}
}

func (p Paths) BinaryPath() string {
	// An explicit local selection keeps a tested correction separate from the
	// official download. Lstat also detects a broken symlink: fail at startup
	// instead of silently switching back to a receiver with different behavior.
	selected := filepath.Join(p.Tools, "mediamtx-active")
	if _, err := os.Lstat(selected); !errors.Is(err, os.ErrNotExist) {
		return filepath.Join(selected, "mediamtx")
	}
	return p.officialMediaMTXPath()
}

func (p Paths) officialMediaMTXPath() string {
	return filepath.Join(p.Tools, "mediamtx-v"+mediaMTXVersion, "mediamtx")
}

func (p Paths) LoadSettings() (Settings, error) {
	var settings Settings
	path := filepath.Join(p.Local, "settings.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return settings, errors.New("the lab is not set up yet; run ./lab setup")
	}
	if err != nil {
		return settings, fmt.Errorf("read private settings: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return settings, errors.New("settings.json must be a regular private file; use chmod 600 .local/settings.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return settings, fmt.Errorf("read private settings: %w", err)
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return settings, errors.New("the private settings file is invalid; repair .local/settings.json before setup")
	}
	settings.Sources = GetSources(settings)
	if err := settings.validate(); err != nil {
		return settings, fmt.Errorf("invalid private settings: %w", err)
	}
	return settings, nil
}

func (s Settings) validate() error {
	if _, err := validateIPv4(s.Host); err != nil {
		return err
	}
	if s.Version != mediaMTXVersion {
		return errors.New("settings use an unsupported MediaMTX version")
	}
	userPattern := regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	users := []string{s.RelayUser}
	secrets := []string{s.RelayPassword, s.CentralPassphrase}
	sources := GetSources(s)
	if len(sources) > MaxSources {
		return fmt.Errorf("this lab supports at most %d sources", MaxSources)
	}
	ids, publisherUsers := map[string]bool{}, map[string]bool{}
	for _, source := range sources {
		if err := validateSourceName(source.ID, source.Label); err != nil {
			return err
		}
		if ids[source.ID] || publisherUsers[source.PublisherUser] {
			return errors.New("sources need unique IDs and publisher usernames")
		}
		ids[source.ID], publisherUsers[source.PublisherUser] = true, true
		users = append(users, source.PublisherUser)
		secrets = append(secrets, source.PublisherPassword, source.PublishPassphrase)
	}
	for _, user := range users {
		if !userPattern.MatchString(user) || user == "any" {
			return errors.New("publisher and relay usernames must be 1-32 letters, digits, underscores or hyphens, and cannot be 'any'")
		}
	}
	// Generated secrets are URL-safe, avoiding interpretation as SRT URL parameters.
	for _, secret := range secrets {
		decoded, err := hex.DecodeString(secret)
		if err != nil || len(decoded) != 16 {
			return errors.New("connection passwords and passphrases must retain their generated 32-character hexadecimal values")
		}
	}
	if !filepath.IsAbs(s.FFmpeg) || !filepath.IsAbs(s.FFprobe) {
		return errors.New("FFmpeg and ffprobe paths must be absolute")
	}
	return nil
}

func validateSourceName(id, label string) error {
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`).MatchString(id) {
		return errors.New("source ID must start with a lowercase letter and contain at most 32 lowercase letters, digits or hyphens")
	}
	if !utf8.ValidString(label) || strings.TrimSpace(label) == "" || utf8.RuneCountInString(label) > 80 || strings.IndexFunc(label, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return errors.New("source label must be 1-80 visible text characters on one line")
	}
	return nil
}

func newSource(id, label string) (SourceConfig, error) {
	source := SourceConfig{ID: id, Label: label, PublisherUser: id}
	if err := validateSourceName(id, label); err != nil {
		return source, err
	}
	var err error
	if source.PublisherPassword, err = randomSecret(); err != nil {
		return source, err
	}
	source.PublishPassphrase, err = randomSecret()
	return source, err
}

// AtomicJSON writes a private file, flushes it, and replaces the previous file.
func AtomicJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return privateWrite(path, append(data, '\n'))
}

func privateWrite(path string, data []byte) (err error) {
	directory := filepath.Dir(path)
	if err = os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	// Persist the directory entry as well as the new file's contents.
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func validateIPv4(value string) (string, error) {
	address, err := netip.ParseAddr(value)
	if err != nil || !address.Is4() || address.IsUnspecified() || address.IsMulticast() || value == "255.255.255.255" {
		return "", errors.New("use this computer's IPv4 address for --host, for example 192.168.1.20")
	}
	return address.String(), nil
}

func shortCommand(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func detectHost() (string, error) {
	var candidates []string
	if runtime.GOOS == "darwin" {
		candidates = append(candidates, shortCommand("/usr/sbin/ipconfig", "getifaddr", "en0"))
		route := shortCommand("/sbin/route", "-n", "get", "default")
		match := regexp.MustCompile(`(?m)^\s*interface:\s*(\S+)`).FindStringSubmatch(route)
		if len(match) == 2 {
			candidates = append(candidates, shortCommand("/usr/sbin/ipconfig", "getifaddr", match[1]))
		}
	}
	// Selecting a local UDP address does not transmit any data to this destination.
	if connection, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 9}); err == nil {
		candidates = append(candidates, connection.LocalAddr().(*net.UDPAddr).IP.String())
		connection.Close()
	}
	for _, candidate := range candidates {
		address, err := netip.ParseAddr(candidate)
		if err == nil && address.Is4() && !address.IsLoopback() && !address.IsUnspecified() && !address.IsMulticast() {
			return address.String(), nil
		}
	}
	return "", errors.New("could not find a local network address; connect this computer and your source devices to a local network or use ./lab setup --host YOUR_COMPUTER_IP")
}

func randomSecret() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (p Paths) Setup(host string) (Settings, error) {
	var settings Settings
	for _, dir := range []string{p.Local, p.Recordings, p.Reports} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return settings, err
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return settings, err
		}
	}
	lock, err := p.lockSourceRegistry()
	if err != nil {
		return settings, err
	}
	defer lock.Close()
	if _, err := os.Stat(filepath.Join(p.Local, "settings.json")); err == nil {
		settings, err = p.LoadSettings()
		if err != nil {
			return settings, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return settings, err
	}
	if host == "" {
		host, err = detectHost()
	} else {
		host, err = validateIPv4(host)
	}
	if err != nil {
		return settings, err
	}
	settings.Host, settings.Version = host, mediaMTXVersion
	if settings.FFmpeg, err = exec.LookPath("ffmpeg"); err != nil {
		return settings, errors.New("FFmpeg is not installed or is not on PATH; install FFmpeg, then run ./lab setup")
	}
	if settings.FFprobe, err = exec.LookPath("ffprobe"); err != nil {
		return settings, errors.New("ffprobe is not installed or is not on PATH; install FFmpeg, then run ./lab setup")
	}
	if len(settings.Sources) == 0 {
		source, e := newSource("camera-01", "Camera 1")
		if e != nil {
			return settings, e
		}
		settings.Sources = []SourceConfig{source}
	}
	if settings.RelayUser == "" {
		settings.RelayUser = "relay"
	}
	for _, field := range []*string{&settings.RelayPassword, &settings.CentralPassphrase} {
		if *field == "" {
			*field, err = randomSecret()
			if err != nil {
				return settings, err
			}
		}
	}
	if err := settings.validate(); err != nil {
		return settings, err
	}
	if err := p.installMediaMTX(); err != nil {
		return settings, err
	}
	if err := p.writeSourceConfiguration(settings); err != nil {
		return settings, err
	}
	return settings, nil
}

func (p Paths) lockSourceRegistry() (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(p.Local, "sources.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// AddSource changes a stopped lab; the CLI owns the stop/start requirement.
func (p Paths) AddSource(id, label string) error {
	if label == "" {
		label = id
	}
	if err := validateSourceName(id, label); err != nil {
		return err
	}
	lock, err := p.lockSourceRegistry()
	if err != nil {
		return err
	}
	defer lock.Close()
	settings, err := p.LoadSettings()
	if err != nil {
		return err
	}
	if len(settings.Sources) >= MaxSources {
		return fmt.Errorf("this lab supports at most %d sources", MaxSources)
	}
	for _, source := range settings.Sources {
		if source.ID == id {
			return fmt.Errorf("source %q already exists", id)
		}
	}
	source, err := newSource(id, label)
	if err != nil {
		return err
	}
	settings.Sources = append(settings.Sources, source)
	return p.writeSourceConfiguration(settings)
}

func (p Paths) writeSourceConfiguration(settings Settings) error {
	if err := settings.validate(); err != nil {
		return err
	}
	guides := map[string]string{}
	var index strings.Builder
	fmt.Fprintln(&index, "FIELD VIDEO LAB — SOURCES")
	fmt.Fprintln(&index, "Each source has its own connection and GStreamer viewing commands. Multiple sources can send at the same time.")
	fmt.Fprintln(&index, "The files listed below contain passwords. Keep those files private.")
	fmt.Fprintln(&index)
	for _, source := range GetSources(settings) {
		guide, err := renderSourceSetup(settings, source)
		if err != nil {
			return err
		}
		guidePath := filepath.Join(p.Local, "connections", source.ID+".txt")
		guides[guidePath] = guide
		fmt.Fprintf(&index, "%s (%s)\nConnection instructions: %s\nWatch locally: ./lab --source %s view local\nWatch forwarded: ./lab --source %s view forwarded\n\n", source.Label, source.ID, guidePath, source.ID, source.ID)
	}
	for name, guide := range guides {
		if err := privateWrite(name, []byte(guide)); err != nil {
			return err
		}
	}
	for _, item := range []struct {
		name  string
		value any
	}{
		{"field.yml", serverConfig(settings, false)},
		{"central.yml", serverConfig(settings, true)},
		{"settings.json", settings},
	} {
		if err := AtomicJSON(filepath.Join(p.Local, item.name), item.value); err != nil {
			return err
		}
	}
	return privateWrite(filepath.Join(p.Root, "SOURCES.txt"), []byte(index.String()))
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (p Paths) installMediaMTX() error {
	// Setup manages only the pinned official release, never a selected local
	// build. Building and selecting a correction are separate explicit steps.
	binary := p.officialMediaMTXPath()
	if info, err := os.Stat(binary); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
		if shortCommand(binary, "--version") == "v"+mediaMTXVersion {
			return nil
		}
		return errors.New("the installed MediaMTX binary has the wrong version; move .tools/mediamtx-v1.21.0 aside and run setup again")
	}
	target := runtime.GOOS + "_" + runtime.GOARCH
	expected, ok := releaseChecksums[target]
	if !ok {
		return errors.New("the installer supports macOS and Linux on ARM64 or Intel/AMD64")
	}
	downloadDir := filepath.Join(p.Tools, "downloads")
	if err := os.MkdirAll(downloadDir, 0700); err != nil {
		return err
	}
	name := "mediamtx_v" + mediaMTXVersion + "_" + target + ".tar.gz"
	archive := filepath.Join(downloadDir, name+".part")
	url := "https://github.com/bluenviron/mediamtx/releases/download/v" + mediaMTXVersion + "/" + name
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 5 * time.Minute}
	if err := resumableDownload(ctx, client, url, archive, expected); err != nil {
		return err
	}
	return extractMediaMTX(archive, filepath.Dir(binary))
}

// resumableDownload retains verified-position partial bytes after connection errors.
// A resumed response must start at exactly our file length before we append anything.
func resumableDownload(ctx context.Context, client *http.Client, url, partial, expected string) error {
	if digest, err := fileSHA256(partial); err == nil && digest == expected {
		return nil
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("MediaMTX download paused; run setup again to resume: %w", ctx.Err())
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		lastErr = downloadAttempt(ctx, client, url, partial)
		if lastErr != nil {
			continue
		}
		digest, err := fileSHA256(partial)
		if err != nil {
			lastErr = err
			continue
		}
		if digest == expected {
			return nil
		}
		// A finished but invalid archive must not be reused as a resume prefix.
		lastErr = errors.New("the completed archive did not match the pinned SHA-256 checksum")
		if err := os.Rename(partial, partial+".invalid"); err != nil {
			return fmt.Errorf("%w; could not quarantine it: %v", lastErr, err)
		}
	}
	return fmt.Errorf("MediaMTX download did not finish: %w; partial data is kept in .tools/downloads; run ./lab setup again to resume", lastErr)
}

func downloadAttempt(ctx context.Context, client *http.Client, url, partial string) error {
	var offset int64
	if info, err := os.Lstat(partial); err == nil {
		if !info.Mode().IsRegular() || info.Size() > maxArchiveBytes {
			return errors.New("the saved download is not a valid partial archive")
		}
		offset = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "field-video-lab-setup")
	req.Header.Set("Accept-Encoding", "identity")
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if encoding := response.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return errors.New("server returned encoded download bytes; saved partial data was left unchanged")
	}
	var total int64 = -1
	switch response.StatusCode {
	case http.StatusOK:
		offset = 0 // The server ignored Range; replace instead of duplicating bytes.
		total = response.ContentLength
	case http.StatusPartialContent:
		parts := regexp.MustCompile(`^bytes ([0-9]+)-([0-9]+)/([0-9]+)$`).FindStringSubmatch(response.Header.Get("Content-Range"))
		if len(parts) != 4 {
			return errors.New("server returned an invalid download range; saved partial data was left unchanged")
		}
		first, err1 := strconv.ParseInt(parts[1], 10, 64)
		last, err2 := strconv.ParseInt(parts[2], 10, 64)
		total, err = strconv.ParseInt(parts[3], 10, 64)
		if err1 != nil || err2 != nil || err != nil || first != offset || last < first || total <= last || total > maxArchiveBytes {
			return errors.New("server returned an invalid download range; saved partial data was left unchanged")
		}
		if response.ContentLength >= 0 && response.ContentLength != last-first+1 {
			return errors.New("server returned an inconsistent download range length")
		}
	case http.StatusRequestedRangeNotSatisfiable:
		parts := regexp.MustCompile(`^bytes \*/([0-9]+)$`).FindStringSubmatch(response.Header.Get("Content-Range"))
		if len(parts) == 2 {
			total, err = strconv.ParseInt(parts[1], 10, 64)
		}
		if len(parts) == 2 && err == nil && total == offset {
			return nil // Caller validates the entire existing archive by checksum.
		}
		return errors.New("server rejected the saved download range")
	default:
		return fmt.Errorf("download server returned HTTP %d", response.StatusCode)
	}
	if total > maxArchiveBytes {
		return errors.New("MediaMTX archive is unexpectedly large")
	}
	flags := os.O_CREATE | os.O_WRONLY
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(partial, flags, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Downloading MediaMTX (attempt begins at %.1f MB).\n", float64(offset)/1e6)
	progress := &downloadProgress{destination: f, bytes: offset, lastBytes: offset, lastAt: time.Now()}
	_, copyErr := io.Copy(progress, io.LimitReader(response.Body, maxArchiveBytes-offset+1))
	syncErr := f.Sync()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if progress.bytes > maxArchiveBytes {
		return errors.New("MediaMTX archive exceeded the download size limit")
	}
	if total >= 0 && progress.bytes != total {
		return fmt.Errorf("download interrupted at %d of %d bytes", progress.bytes, total)
	}
	return nil
}

type downloadProgress struct {
	destination io.Writer
	bytes       int64
	lastBytes   int64
	lastAt      time.Time
}

func (p *downloadProgress) Write(data []byte) (int, error) {
	n, err := p.destination.Write(data)
	p.bytes += int64(n)
	if p.bytes-p.lastBytes >= 4<<20 || time.Since(p.lastAt) >= 15*time.Second {
		fmt.Fprintf(os.Stderr, "MediaMTX downloaded: %.1f MB.\n", float64(p.bytes)/1e6)
		p.lastBytes, p.lastAt = p.bytes, time.Now()
	}
	return n, err
}

func extractMediaMTX(archive, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".mediamtx-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	// Bound decompressed bytes too: a small gzip archive can expand enormously.
	reader := tar.NewReader(io.LimitReader(gz, 2*maxArchiveBytes))
	found := map[string]bool{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(header.Name, "./")
		if name != "mediamtx" && name != "LICENSE" {
			continue
		}
		if found[name] || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || header.Size > maxArchiveBytes || header.Size < 0 {
			return fmt.Errorf("unexpected archive entry for %s", name)
		}
		mode := os.FileMode(0644)
		if name == "mediamtx" {
			mode = 0755
		}
		output, err := os.OpenFile(filepath.Join(stage, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, reader)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		found[name] = true
	}
	if !found["mediamtx"] || !found["LICENSE"] {
		return errors.New("the release archive is missing MediaMTX or its license")
	}
	if err := os.MkdirAll(destination, 0700); err != nil {
		return err
	}
	for _, name := range []string{"LICENSE", "mediamtx"} {
		if err := os.Rename(filepath.Join(stage, name), filepath.Join(destination, name)); err != nil {
			return err
		}
	}
	return nil
}

func serverConfig(settings Settings, central bool) map[string]any {
	ports, host := Field, settings.Host
	if central {
		ports, host = Central, "127.0.0.1"
	}
	paths := map[string]any{}
	users := []any{}
	readPermissions := []any{map[string]any{"action": "api"}, map[string]any{"action": "metrics"}}
	relayPermissions := []any{}
	for _, source := range GetSources(settings) {
		phrase := source.PublishPassphrase
		if central {
			phrase = settings.CentralPassphrase
			relayPermissions = append(relayPermissions, map[string]any{"action": "publish", "path": source.ID})
		} else {
			users = append(users, map[string]any{"user": source.PublisherUser, "pass": source.PublisherPassword, "ips": []string{}, "permissions": []any{map[string]any{"action": "publish", "path": source.ID}}})
		}
		paths[source.ID] = map[string]any{"source": "publisher", "record": false, "srtPublishPassphrase": phrase}
		readPermissions = append(readPermissions, map[string]any{"action": "read", "path": source.ID})
	}
	if central {
		users = append(users, map[string]any{"user": settings.RelayUser, "pass": settings.RelayPassword, "ips": []string{"127.0.0.1", "::1"}, "permissions": relayPermissions})
	}
	users = append(users, map[string]any{"user": "any", "pass": "", "ips": []string{"127.0.0.1", "::1"}, "permissions": readPermissions})
	local := func(port int) string { return fmt.Sprintf("127.0.0.1:%d", port) }
	return map[string]any{
		"logLevel": "info", "logDestinations": []string{"stdout"}, "readTimeout": "5s", "writeTimeout": "5s", "writeQueueSize": 512,
		"authMethod":        "internal",
		"authInternalUsers": users,
		"api":               true, "apiAddress": local(ports.API), "apiAllowOrigins": []string{},
		"metrics": true, "metricsAddress": local(ports.Metrics), "metricsAllowOrigins": []string{},
		"pprof": false, "playback": false,
		"rtsp": true, "rtspAddress": local(ports.RTSP), "rtspTransports": []string{"tcp"}, "rtspEncryption": "no",
		"rtmp": false, "hls": false, "moq": false,
		"webrtc": false,
		"srt":    true, "srtAddress": fmt.Sprintf("%s:%d", host, ports.SRT),
		"pathDefaults": map[string]any{"record": false, "maxReaders": 12, "overridePublisher": false},
		"paths":        paths,
	}
}

//go:embed templates/source-setup.txt
var sourceSetupTemplate string

func renderSourceSetup(settings Settings, source SourceConfig) (string, error) {
	values := struct {
		ID, Label                                        string
		ConnectionURL, Host, StreamID, PublishPassphrase string
		Port                                             int
	}{
		ID:                source.ID,
		Label:             source.Label,
		ConnectionURL:     fmt.Sprintf("srt://%s:%d", settings.Host, Field.SRT),
		Host:              settings.Host,
		Port:              Field.SRT,
		StreamID:          "publish:" + source.ID + ":" + source.PublisherUser + ":" + source.PublisherPassword,
		PublishPassphrase: source.PublishPassphrase,
	}
	guide, err := template.New("source-setup").Option("missingkey=error").Parse(sourceSetupTemplate)
	if err != nil {
		return "", fmt.Errorf("parse embedded source setup instructions: %w", err)
	}
	var output strings.Builder
	if err := guide.Execute(&output, values); err != nil {
		return "", fmt.Errorf("render source setup instructions: %w", err)
	}
	return output.String(), nil
}
