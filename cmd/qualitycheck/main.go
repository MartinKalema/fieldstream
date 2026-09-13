// qualitycheck compares compression settings on one saved recording. It never
// changes the live profiles, starts media services, or contacts the archive.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"fieldvideolab/internal/lab"
)

type options struct {
	Root, Recording, ReportDir, Listen, TestScene string
	NoServe                                       bool
}

func parseOptions(args []string, output io.Writer) (options, error) {
	var cfg options
	flags := flag.NewFlagSet("qualitycheck", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&cfg.Root, "root", ".", "project folder containing the recording catalog")
	flags.StringVar(&cfg.Recording, "recording", "", "exact relative recording filename from the catalog")
	flags.StringVar(&cfg.ReportDir, "report-dir", "", "reopen an existing comparison folder without encoding again")
	flags.StringVar(&cfg.TestScene, "test-scene", "", "generated stress scene: fast-motion, fine-detail or dim-noise")
	flags.StringVar(&cfg.Listen, "listen", "127.0.0.1:19082", "loopback address for the comparison page")
	flags.BoolVar(&cfg.NoServe, "no-serve", false, "write the comparison report and exit")
	if err := flags.Parse(args); err != nil {
		return cfg, err
	}
	inputs := 0
	for _, input := range []string{cfg.Recording, cfg.ReportDir, cfg.TestScene} {
		if input != "" {
			inputs++
		}
	}
	if flags.NArg() != 0 || inputs != 1 {
		return cfg, errors.New("choose exactly one of --recording SESSION/FILE.mp4, --report-dir FOLDER or --test-scene NAME")
	}
	if cfg.TestScene != "" {
		if _, ok := sceneDefinition(cfg.TestScene); !ok {
			return cfg, errors.New("test scene must be fast-motion, fine-detail or dim-noise")
		}
	}
	host, port, err := net.SplitHostPort(cfg.Listen)
	ip, parseErr := netip.ParseAddr(host)
	number, portErr := strconv.Atoi(port)
	if err != nil || parseErr != nil || !ip.IsLoopback() || portErr != nil || number < 0 || number > 65535 {
		return cfg, errors.New("listen must be a loopback IP and a port from 0 to 65535")
	}
	return cfg, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// Freeze the exact cataloged bytes in a new private run folder. All later
// probing, encoding and HTTP reads operate on that copy, not the live files.
func copyCatalogRecording(ctx context.Context, p lab.Paths, relative, dir string) (err error) {
	if filepath.IsAbs(relative) || filepath.Clean(relative) != relative || strings.ContainsAny(relative, "\\\x00") ||
		strings.HasPrefix(relative, "../") || filepath.Ext(relative) != ".mp4" {
		return errors.New("recording must be an exact relative MP4 filename from the catalog")
	}
	database := filepath.Join(p.Local, "recordings.sqlite")
	if info, err := os.Lstat(database); err != nil || !info.Mode().IsRegular() {
		return errors.New("recording catalog is unavailable")
	}
	u := url.URL{Scheme: "file", Path: database}
	query := url.Values{"mode": {"ro"}, "_pragma": {"query_only(ON)", "busy_timeout(1500)"}}
	u.RawQuery = query.Encode()
	catalog, err := sql.Open("sqlite", u.String())
	if err != nil {
		return errors.New("recording catalog could not be opened")
	}
	var s struct {
		Bytes  int64
		SHA256 string
	}
	err = catalog.QueryRowContext(ctx, "SELECT bytes,sha256 FROM recordings WHERE path=?", relative).Scan(&s.Bytes, &s.SHA256)
	catalog.Close()
	if err != nil {
		return errors.New("selected recording was not found in the completed-file catalog")
	}
	if s.Bytes < 1 || s.Bytes > 64<<20 {
		return errors.New("comparison input must be no larger than 64 MiB")
	}
	root, err := os.OpenRoot(p.Recordings)
	if err != nil {
		return errors.New("recording directory is unavailable")
	}
	defer root.Close()
	f, err := root.OpenFile(relative, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return errors.New("selected local recording cannot be read")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != s.Bytes {
		return errors.New("selected recording is not a regular file matching the catalog size")
	}
	name := filepath.Join(dir, "original.mp4")
	copy, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("private comparison copy could not be created")
	}
	defer func() {
		copy.Close()
		if err != nil {
			_ = os.Remove(name) // Only this newly created file belongs to this call.
		}
	}()
	digest := sha256.New()
	n, err := io.Copy(io.MultiWriter(copy, digest), io.LimitReader(contextReader{ctx, f}, s.Bytes+1))
	if err != nil || n != s.Bytes || hex.EncodeToString(digest.Sum(nil)) != s.SHA256 {
		return errors.New("selected recording does not match its catalog checksum")
	}
	if err := copy.Sync(); err != nil {
		return errors.New("private comparison copy could not be flushed")
	}
	return nil
}

func checkFreeSpace(directory string) error {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(directory, &stats); err != nil {
		return errors.New("could not check free space before comparison")
	}
	if uint64(stats.Bavail)*uint64(stats.Bsize) < 2<<30 {
		return errors.New("comparison requires at least 2 GiB free space for this run and ongoing recordings")
	}
	return nil
}

func run(parent context.Context, args []string, output io.Writer) error {
	cfg, err := parseOptions(args, output)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	dir := cfg.ReportDir
	if dir == "" {
		absolute, err := filepath.Abs(cfg.Root)
		if err != nil {
			return err
		}
		p := lab.NewPaths(absolute)
		settings, err := p.LoadSettings()
		if err != nil {
			return err
		}
		lock, err := os.OpenFile(filepath.Join(p.Local, "qualitycheck.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return errors.New("comparison lock could not be opened")
		}
		defer lock.Close()
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			return errors.New("another compression comparison is already running in this project")
		}
		if err := os.MkdirAll(p.Reports, 0700); err != nil {
			return err
		}
		if err := checkFreeSpace(p.Reports); err != nil {
			return err
		}
		dir, err = os.MkdirTemp(p.Reports, "quality-run-")
		if err != nil {
			return errors.New("private comparison folder could not be created")
		}
		// Explicit bounded diagnostic; the long-lived page does not retain this
		// deadline after processing is done.
		ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
		defer cancel()
		var generated *testScene
		if cfg.TestScene != "" {
			fmt.Fprintln(output, "Creating a generated compression stress scene; this is not camera footage...")
			scene, err := generateScene(ctx, dir, settings.FFmpeg, cfg.TestScene)
			if err != nil {
				return err
			}
			generated = &scene
		} else if err := copyCatalogRecording(ctx, p, cfg.Recording, dir); err != nil {
			return err
		}
		fmt.Fprintln(output, "Comparing two compression settings against the same saved video...")
		report, err := runBenchmark(ctx, filepath.Join(dir, "original.mp4"), dir, settings.FFmpeg, settings.FFprobe)
		if err != nil {
			return err
		}
		report.TestScene = generated
		if err := lab.AtomicJSON(filepath.Join(dir, "result.json"), report); err != nil {
			return errors.New("comparison report could not be saved")
		}
		_ = lock.Close() // The idle report viewer needs no encoder lock.
	}
	report, err := readReport(dir)
	if err != nil {
		return err
	}
	printSummary(output, report)
	fmt.Fprintln(output, "Private comparison folder:", dir)
	if cfg.NoServe {
		return nil
	}
	return serveComparison(parent, cfg.Listen, dir, output)
}

func printSummary(output io.Writer, report comparisonReport) {
	if report.TestScene != nil {
		fmt.Fprintln(output, "Generated test scene:", report.TestScene.Title)
		fmt.Fprintln(output, report.TestScene.Note)
	}
	fmt.Fprintf(output, "Original: %.2f MB, %.2f pictures/second\n", float64(report.Input.Bytes)/1e6, report.Input.FPS)
	for _, variant := range report.Variants {
		saved := 100 * (1 - float64(variant.Bytes)/float64(report.Input.Bytes))
		change := "smaller"
		if saved < 0 {
			saved, change = -saved, "larger"
		}
		fmt.Fprintf(output, "%s: %.2f MB, %.1f%% %s, detail similarity %.4f at %d matched frames; encode %.2fs wall / %.2fs CPU\n", variant.Name, float64(variant.Bytes)/1e6, saved, change, variant.SSIM, variant.ComparedFrames, variant.EncodeSeconds, variant.CPUSeconds)
	}
	fmt.Fprintln(output, "Similarity is not percent quality retained; reduced motion sampling and live delivery delay are separate.")
}

func readReport(dir string) (comparisonReport, error) {
	var report comparisonReport
	root, err := os.OpenRoot(dir)
	if err != nil {
		return report, errors.New("comparison folder could not be opened")
	}
	defer root.Close()
	f, err := root.OpenFile("result.json", os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return report, errors.New("completed comparison report is missing")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return report, errors.New("comparison report is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 || !reportFieldsPresent(data) {
		return report, errors.New("comparison report is missing required measurements")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&report) != nil || report.CreatedAt.IsZero() || report.FFmpegVersion == "" || len(report.FFmpegVersion) > 256 || report.Input.File != "original.mp4" || report.Input.Bytes < 1 || report.Input.Bytes > 64<<20 || report.Input.Width < 1 || report.Input.Height < 1 || report.Input.Frames < 1 || report.Input.FPS <= 0 || report.Input.Duration <= 0 || report.Input.Duration > 15 || len(report.Variants) != 2 {
		return report, errors.New("comparison report is invalid or incomplete")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return report, errors.New("comparison report contains extra data")
	}
	if scene := report.TestScene; scene != nil {
		expected, ok := sceneDefinition(scene.ID)
		if !ok || *scene != expected || report.Input.Width != 1280 || report.Input.Height != 720 || report.Input.Frames != 150 || report.Input.FPS != 30 || report.Input.Duration != 5 {
			return report, errors.New("generated scene evidence does not match a supported test recipe")
		}
	}
	p := report.Playback
	if p.File != "playback.mp4" || p.Bytes < 1 || p.Bytes > maximumInputBytes || p.Codec != report.Input.Codec || p.PixelFormat != report.Input.PixelFormat || p.Width != report.Input.Width || p.Height != report.Input.Height || p.Frames != report.Input.Frames || p.FPS <= 0 || p.Duration <= 0 || p.Duration > 15 || p.StartTime != 0 {
		return report, errors.New("comparison playback copy is invalid or incomplete")
	}
	if err := verifyReportMedia(root, p.File, p.Bytes, p.SHA256); err != nil {
		return report, err
	}
	seen := map[string]bool{}
	for _, v := range report.Variants {
		if (v.File != "detail-20.mp4" && v.File != "small-20.mp4") || seen[v.File] || v.Bytes < 1 || v.Bytes > 64<<20 || v.Width < 1 || v.Height < 1 || v.FPS != 20 || v.Frames < 1 || v.ComparedFrames != v.Frames || v.SSIM < -1 || v.SSIM > 1 || v.EncodeSeconds <= 0 || v.CPUSeconds < 0 {
			return report, errors.New("comparison variant is invalid or incomplete")
		}
		if err := verifyReportMedia(root, v.File, v.Bytes, v.SHA256); err != nil {
			return report, err
		}
		seen[v.File] = true
	}
	if err := verifyReportMedia(root, report.Input.File, report.Input.Bytes, report.Input.SHA256); err != nil {
		return report, err
	}
	return report, nil
}

// Zero is a legitimate SSIM or CPU observation. Presence is checked separately
// so an absent measurement cannot silently turn into a displayed zero.
func reportFieldsPresent(data []byte) bool {
	has := func(object map[string]json.RawMessage, names ...string) bool {
		for _, name := range names {
			value, ok := object[name]
			if !ok || strings.TrimSpace(string(value)) == "null" {
				return false
			}
		}
		return true
	}
	var top map[string]json.RawMessage
	var variants []map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil || !has(top, "created_at", "ffmpeg_version", "input", "playback", "variants") ||
		json.Unmarshal(top["variants"], &variants) != nil || len(variants) != 2 {
		return false
	}
	if raw, exists := top["test_scene"]; exists {
		var scene map[string]json.RawMessage
		if json.Unmarshal(raw, &scene) != nil || !has(scene, "id", "title", "note", "filter", "source_encoding") {
			return false
		}
	}
	for _, name := range []string{"input", "playback"} {
		var media map[string]json.RawMessage
		if json.Unmarshal(top[name], &media) != nil || !has(media, "file", "codec", "pixel_format", "sha256", "width", "height", "frames", "fps", "duration", "bytes", "start_time", "sample_aspect_ratio", "square_pixels_assumed") {
			return false
		}
	}
	for _, v := range variants {
		if !has(v, "name", "file", "sha256", "width", "height", "frames", "fps", "bytes", "encode_seconds", "cpu_seconds", "ssim", "compared_frames") {
			return false
		}
	}
	return true
}

func verifyReportMedia(root *os.Root, name string, size int64, expected string) error {
	decoded, err := hex.DecodeString(expected)
	if err != nil || len(decoded) != sha256.Size {
		return errors.New("comparison artifact checksum is invalid")
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return errors.New("a measured comparison video is missing")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return errors.New("a comparison video changed since it was measured")
	}
	digest := sha256.New()
	n, err := io.Copy(digest, io.LimitReader(f, size+1))
	if err != nil || n != size || hex.EncodeToString(digest.Sum(nil)) != expected {
		return errors.New("a comparison video no longer matches its measured checksum")
	}
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Comparison could not finish:", err)
		os.Exit(1)
	}
}
