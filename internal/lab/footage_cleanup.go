package lab

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// This is a one-shot, explicit-manifest cleanup, not an automatic retention job.
// Execution requires the supervisor and uploader locks; preview may run live.
type FootageCleanup struct {
	Version     int                `json:"version"`
	Root        string             `json:"root"`
	Cutoff      time.Time          `json:"cutoff"`
	PreparedAt  time.Time          `json:"prepared_at"`
	Destination string             `json:"destination_sha256"`
	Recordings  []CleanupRecording `json:"recordings"`
	Files       []CleanupFile      `json:"files"`
	UnknownR2   []CleanupObject    `json:"unknown_r2_objects_preserved"`
	Excluded    []CleanupExclusion `json:"excluded_old_video"`
}

type CleanupExclusion struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type CleanupRecording struct {
	Segment
	ObjectKey string         `json:"object_key"`
	Main      bool           `json:"main_catalog"`
	Remote    *CleanupObject `json:"remote_at_preview,omitempty"`
}

type CleanupFile struct {
	Path     string    `json:"path"`
	Kind     string    `json:"kind"`
	Bytes    int64     `json:"bytes"`
	SHA256   string    `json:"sha256"`
	Modified time.Time `json:"modified"`
}

type CleanupObject struct {
	Key      string    `json:"key"`
	Bytes    int64     `json:"bytes"`
	ETag     string    `json:"etag"`
	Modified time.Time `json:"modified"`
}

type FootageCleanupResult struct {
	Cutoff         time.Time `json:"cutoff"`
	FinishedAt     time.Time `json:"finished_at"`
	RemoteDeleted  []string  `json:"remote_deleted"`
	LocalDeleted   []string  `json:"local_deleted"`
	CatalogDeleted int64     `json:"catalog_rows_deleted"`
	Error          string    `json:"error,omitempty"`
}

type cleanupObjects interface {
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	DeleteObjects(context.Context, *s3.DeleteObjectsInput, ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

func cleanupDestination(c R2Config) string {
	sum := sha256.Sum256([]byte(c.AccountID + "/" + c.Bucket + "/" + c.Prefix))
	return hex.EncodeToString(sum[:])
}

func cleanupConfiguration(p Paths) (*R2Config, cleanupObjects, error) {
	c, err := LoadR2Config(p)
	if err != nil || c == nil {
		return nil, nil, errors.New("cleanup requires the existing private R2 configuration")
	}
	// Deletion remains explicit even if automatic uploads are disabled.
	c.Enabled = true
	if err := normalizeR2Config(c); err != nil {
		return nil, nil, err
	}
	client := newR2Client(*c, "https://"+c.AccountID+".r2.cloudflarestorage.com", &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	})
	return c, client, nil
}

func cleanupList(ctx context.Context, client cleanupObjects, config R2Config) (map[string]CleanupObject, error) {
	objects := map[string]CleanupObject{}
	var token *string
	for page := 0; page < 20; page++ {
		out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(config.Bucket), Prefix: aws.String(config.Prefix + "/"), MaxKeys: aws.Int32(1000), ContinuationToken: token})
		if err != nil {
			return nil, errors.New("R2 object listing failed")
		}
		for _, item := range out.Contents {
			key := aws.ToString(item.Key)
			if !strings.HasPrefix(key, config.Prefix+"/") {
				return nil, errors.New("R2 listing returned an object outside the project prefix")
			}
			objects[key] = CleanupObject{Key: key, Bytes: aws.ToInt64(item.Size), ETag: aws.ToString(item.ETag), Modified: aws.ToTime(item.LastModified)}
		}
		if !aws.ToBool(out.IsTruncated) {
			return objects, nil
		}
		if out.NextContinuationToken == nil || aws.ToString(out.NextContinuationToken) == aws.ToString(token) {
			return nil, errors.New("R2 listing did not advance")
		}
		token = out.NextContinuationToken
	}
	return nil, errors.New("R2 prefix exceeds this bounded cleanup's 20000-object limit")
}

func cleanupKey(prefix string, s Segment) string {
	return path.Join(prefix, s.Session, strings.TrimSuffix(filepath.Base(s.Path), ".mp4")+"-"+s.SHA256+".mp4")
}

// No symlink component is accepted, even if it would remain inside the project.
func cleanupFileInfo(root, relative string) (os.FileInfo, error) {
	if filepath.IsAbs(relative) || relative == "." || filepath.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, "../") {
		return nil, errors.New("cleanup path is not a contained relative file")
	}
	parts := strings.Split(relative, string(os.PathSeparator))
	current := root
	var info os.FileInfo
	for i, part := range parts {
		current = filepath.Join(current, part)
		var err error
		info, err = os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if i < len(parts)-1 && !info.IsDir() || i == len(parts)-1 && !info.Mode().IsRegular() {
			return nil, errors.New("cleanup paths must contain only real directories and regular files")
		}
	}
	return info, nil
}

func inspectCleanupFile(ctx context.Context, p Paths, relative, kind string, cutoff time.Time) (CleanupFile, error) {
	before, err := cleanupFileInfo(p.Root, relative)
	if err != nil {
		return CleanupFile{}, err
	}
	if before.ModTime().After(cutoff) {
		return CleanupFile{}, errors.New("cleanup file is newer than the cutoff")
	}
	root, err := os.OpenRoot(p.Root)
	if err != nil {
		return CleanupFile{}, err
	}
	defer root.Close()
	f, err := root.Open(relative)
	if err != nil {
		return CleanupFile{}, err
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, archiveContextReader{ctx: ctx, reader: f})
	if err != nil {
		return CleanupFile{}, err
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || n != before.Size() || after.Size() != n || !before.ModTime().Equal(after.ModTime()) {
		return CleanupFile{}, errors.New("cleanup file changed during inspection")
	}
	return CleanupFile{Path: relative, Kind: kind, Bytes: n, SHA256: hex.EncodeToString(hash.Sum(nil)), Modified: after.ModTime().UTC()}, nil
}

var cleanupTestRoot = regexp.MustCompile(`^reports/(test-run|r2-check)-[0-9]+/`)

func cleanupDiagnosticPath(relative string) bool {
	if !strings.HasPrefix(relative, ".local/diagnostics/") && !cleanupTestRoot.MatchString(relative) {
		return false
	}
	switch strings.ToLower(filepath.Ext(relative)) {
	case ".mp4", ".mov", ".mkv", ".ts", ".h264", ".h265", ".hevc", ".yuv":
		return true
	}
	return false
}

func PrepareFootageCleanup(ctx context.Context, p Paths, cutoff time.Time) (FootageCleanup, error) {
	config, client, err := cleanupConfiguration(p)
	if err != nil {
		return FootageCleanup{}, err
	}
	return prepareFootageCleanup(ctx, p, cutoff, *config, client)
}

func prepareFootageCleanup(ctx context.Context, p Paths, cutoff time.Time, config R2Config, client cleanupObjects) (FootageCleanup, error) {
	plan := FootageCleanup{Version: 1, Root: p.Root, Cutoff: cutoff.UTC(), PreparedAt: time.Now().UTC(), Destination: cleanupDestination(config)}
	if cutoff.IsZero() || cutoff.After(time.Now()) {
		return plan, errors.New("cleanup cutoff must be in the past")
	}
	c, err := OpenCatalog(p)
	if err != nil {
		return plan, err
	}
	defer c.Close()
	var destination string
	err = c.db.QueryRowContext(ctx, "SELECT value FROM catalog_metadata WHERE name='archive_destination'").Scan(&destination)
	if err != nil || destination != config.AccountID+"/"+config.Bucket+"/"+config.Prefix {
		return plan, errors.New("R2 destination does not match the recording catalog")
	}
	rows, err := c.db.QueryContext(ctx, "SELECT "+segmentColumns+" FROM recordings WHERE created_at<=? ORDER BY path", cutoff.UnixMilli())
	if err != nil {
		return plan, err
	}
	for rows.Next() {
		s, err := scanCatalogSegment(rows)
		if err != nil || validateSegment(s.Segment) != nil {
			rows.Close()
			return plan, errors.New("cleanup catalog contains an invalid recording")
		}
		key := cleanupKey(config.Prefix, s.Segment)
		if s.ObjectKey != "" && s.ObjectKey != key {
			rows.Close()
			return plan, errors.New("catalog object key does not match its recording identity")
		}
		plan.Recordings = append(plan.Recordings, CleanupRecording{Segment: s.Segment, ObjectKey: key, Main: true})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return plan, err
	}
	for _, s := range plan.Recordings {
		f, err := inspectCleanupFile(ctx, p, filepath.Join("recordings", s.Path), "recording", cutoff)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || f.Bytes != s.Bytes || f.SHA256 != s.SHA256 {
			return plan, errors.New("a catalog recording changed or is newer than the cutoff; cleanup was not prepared")
		}
		plan.Files = append(plan.Files, f)
	}
	if err := addUnindexedCleanupFiles(ctx, p, c, &plan); err != nil {
		return plan, err
	}
	if err := addCleanupCheckProof(ctx, p, &plan, config); err != nil {
		return plan, err
	}
	for _, directory := range []string{"reports", ".local/diagnostics"} {
		err := filepath.WalkDir(filepath.Join(p.Root, directory), func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if errors.Is(walkErr, os.ErrNotExist) {
					return nil
				}
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(p.Root, name)
			if err != nil || !cleanupDiagnosticPath(rel) || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.ModTime().After(cutoff) || !info.Mode().IsRegular() {
				return nil
			}
			f, err := inspectCleanupFile(ctx, p, rel, "generated-or-diagnostic-video", cutoff)
			if err != nil {
				return err
			}
			plan.Files = append(plan.Files, f)
			return nil
		})
		if err != nil {
			return plan, err
		}
	}
	objects, err := cleanupList(ctx, client, config)
	if err != nil {
		return plan, err
	}
	for i := range plan.Recordings {
		s := &plan.Recordings[i]
		if object, ok := objects[s.ObjectKey]; ok {
			if object.Bytes != s.Bytes {
				return plan, errors.New("R2 recording size differs from catalog; cleanup was not prepared")
			}
			s.Remote = &object
			delete(objects, s.ObjectKey)
		}
	}
	for _, object := range objects {
		plan.UnknownR2 = append(plan.UnknownR2, object)
	}
	sort.Slice(plan.Files, func(i, j int) bool { return plan.Files[i].Path < plan.Files[j].Path })
	sort.Slice(plan.UnknownR2, func(i, j int) bool { return plan.UnknownR2[i].Key < plan.UnknownR2[j].Key })
	return plan, nil
}

// The successful check report and its immutable test catalog jointly prove
// ownership of this one historical generated object. Other unknown keys stay.
func addCleanupCheckProof(ctx context.Context, p Paths, plan *FootageCleanup, config R2Config) error {
	var proof struct {
		TestRoot  string    `json:"test_root"`
		SHA256    string    `json:"sha256"`
		Bytes     int64     `json:"bytes"`
		Passed    bool      `json:"passed"`
		CheckedAt time.Time `json:"checked_at"`
	}
	err := readJSON(filepath.Join(p.Root, "reports", "latest-r2-check.json"), &proof)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !proof.Passed || proof.CheckedAt.After(plan.Cutoff) {
		return nil
	}
	rel, err := filepath.Rel(p.Root, proof.TestRoot)
	if err != nil || !regexp.MustCompile(`^reports/r2-check-[0-9]+$`).MatchString(rel) {
		return errors.New("R2 check proof is outside a generated project test root")
	}
	u := url.URL{Scheme: "file", Path: filepath.Join(proof.TestRoot, ".local", "recordings.sqlite"), RawQuery: "immutable=1"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	var s Segment
	var key, destination string
	err = db.QueryRowContext(ctx, "SELECT path,session,bytes,sha256,duration FROM recordings WHERE sha256=? AND upload_state='archived'", proof.SHA256).Scan(&s.Path, &s.Session, &s.Bytes, &s.SHA256, &s.Duration)
	if err != nil || s.Bytes != proof.Bytes {
		return errors.New("R2 check proof does not match its catalog")
	}
	if err = db.QueryRowContext(ctx, "SELECT object_key FROM recordings WHERE path=?", s.Path).Scan(&key); err != nil {
		return err
	}
	if err = db.QueryRowContext(ctx, "SELECT value FROM catalog_metadata WHERE name='archive_destination'").Scan(&destination); err != nil {
		return err
	}
	prefix := config.Prefix + "/checks/" + filepath.Base(proof.TestRoot)
	if destination != config.AccountID+"/"+config.Bucket+"/"+prefix || key != cleanupKey(prefix, s) || validateSegment(s) != nil {
		return errors.New("R2 check destination or identity is not proven")
	}
	s.SourceID = normalizedCatalogSourceID(s.SourceID)
	plan.Recordings = append(plan.Recordings, CleanupRecording{Segment: s, ObjectKey: key})
	return nil
}

var cleanupSegmentName = regexp.MustCompile(`^segment-[0-9]+\.mp4$`)

func addUnindexedCleanupFiles(ctx context.Context, p Paths, c *Catalog, plan *FootageCleanup) error {
	indexed := map[string]bool{}
	rows, err := c.db.QueryContext(ctx, "SELECT path FROM recordings")
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		indexed[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// lsof sees paths rather than command lines, so credentials are never read
	// or reported. Failure is conservative: leave all unindexed files alone.
	open := map[string]bool{}
	lsof, err := exec.LookPath("lsof")
	openKnown := err == nil
	if openKnown {
		data, e := exec.CommandContext(ctx, lsof, "-n", "-F", "n", "-a", "-c", "ffmpeg").Output()
		var exit *exec.ExitError
		if e != nil && (!errors.As(e, &exit) || exit.ExitCode() != 1) {
			openKnown = false
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "n/") {
				open[strings.TrimPrefix(line, "n")] = true
			}
		}
	}
	names, err := filepath.Glob(filepath.Join(p.Recordings, "*", "*.mp4"))
	if err != nil {
		return err
	}
	for _, name := range names {
		rel, err := filepath.Rel(p.Recordings, name)
		if err != nil {
			return err
		}
		if indexed[rel] {
			continue
		}
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		if info.ModTime().After(plan.Cutoff) {
			continue
		}
		projectRel := filepath.Join("recordings", rel)
		reason := ""
		if !info.Mode().IsRegular() || !cleanupSegmentName.MatchString(filepath.Base(name)) {
			reason = "not a regular generated recording segment"
		} else if _, e := recordingSource(filepath.Dir(name)); e != nil {
			reason = "source session identity is not proven"
		} else if !openKnown {
			reason = "open recording files could not be verified"
		} else if open[name] {
			reason = "file is still open in FFmpeg"
		}
		if reason != "" {
			plan.Excluded = append(plan.Excluded, CleanupExclusion{Path: projectRel, Reason: reason})
			continue
		}
		f, err := inspectCleanupFile(ctx, p, projectRel, "old-unindexed-recording", plan.Cutoff)
		if err != nil {
			return err
		}
		plan.Files = append(plan.Files, f)
	}
	return nil
}

func ExecuteFootageCleanup(ctx context.Context, p Paths, plan FootageCleanup) (FootageCleanupResult, error) {
	config, client, err := cleanupConfiguration(p)
	if err != nil {
		return FootageCleanupResult{}, err
	}
	return executeFootageCleanup(ctx, p, plan, *config, client)
}

func executeFootageCleanup(ctx context.Context, p Paths, plan FootageCleanup, config R2Config, client cleanupObjects) (result FootageCleanupResult, err error) {
	result.Cutoff = plan.Cutoff
	defer func() {
		result.FinishedAt = time.Now().UTC()
		if err != nil {
			result.Error = err.Error()
		}
	}()
	if plan.Version != 1 || plan.Root != p.Root || plan.Cutoff.IsZero() || plan.Cutoff.After(plan.PreparedAt) || plan.Destination != cleanupDestination(config) {
		return result, errors.New("cleanup manifest or destination does not match this project")
	}
	supervisor, err := fileLock(filepath.Join(p.Local, "supervisor.lock"), true)
	if err != nil {
		return result, errors.New("stop the lab before executing footage cleanup")
	}
	defer supervisor.Close()
	uploader, err := fileLock(filepath.Join(p.Local, "archive.lock"), true)
	if err != nil {
		return result, errors.New("archive worker is still active; cleanup was not started")
	}
	defer uploader.Close()
	c, err := OpenCatalog(p)
	if err != nil {
		return result, err
	}
	defer c.Close()
	expected := map[string]CleanupRecording{}
	for _, s := range plan.Recordings {
		if validateSegment(s.Segment) != nil {
			return result, errors.New("invalid manifest recording")
		}
		prefix := config.Prefix
		if !s.Main {
			// Re-read the original proof rather than accepting arbitrary keys
			// supplied in a hand-edited execution manifest.
			proof := FootageCleanup{Cutoff: plan.Cutoff}
			if e := addCleanupCheckProof(ctx, p, &proof, config); e != nil || len(proof.Recordings) != 1 || proof.Recordings[0].ObjectKey != s.ObjectKey || proof.Recordings[0].SHA256 != s.SHA256 {
				return result, errors.New("historical object proof no longer matches")
			}
		} else {
			if s.ObjectKey != cleanupKey(prefix, s.Segment) {
				return result, errors.New("manifest R2 key is outside its recording scope")
			}
			current, e := c.GetSegment(ctx, s.Path)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return result, e
			}
			if e == nil && current.Segment != s.Segment {
				return result, errors.New("catalog recording changed after preview")
			}
			var created int64
			if e == nil {
				if e = c.db.QueryRowContext(ctx, "SELECT created_at FROM recordings WHERE path=?", s.Path).Scan(&created); e != nil || created > plan.Cutoff.UnixMilli() {
					return result, errors.New("recording is newer than cleanup cutoff")
				}
			}
		}
		if _, exists := expected[s.ObjectKey]; exists {
			return result, errors.New("duplicate manifest R2 key")
		}
		expected[s.ObjectKey] = s
	}
	seen := map[string]bool{}
	for _, f := range plan.Files {
		if seen[f.Path] {
			return result, errors.New("duplicate cleanup file")
		}
		seen[f.Path] = true
		allowed := cleanupDiagnosticPath(f.Path)
		if f.Kind == "old-unindexed-recording" {
			rel, ok := strings.CutPrefix(f.Path, "recordings/")
			if ok && filepath.Dir(rel) != "." && filepath.Dir(filepath.Dir(rel)) == "." && cleanupSegmentName.MatchString(filepath.Base(rel)) {
				_, identityErr := recordingSource(filepath.Join(p.Recordings, filepath.Dir(rel)))
				_, catalogErr := c.GetSegment(ctx, rel)
				allowed = identityErr == nil && errors.Is(catalogErr, sql.ErrNoRows)
			}
		}
		if f.Kind == "recording" {
			for _, s := range plan.Recordings {
				if s.Main && f.Path == filepath.Join("recordings", s.Path) && f.SHA256 == s.SHA256 && f.Bytes == s.Bytes {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			return result, errors.New("manifest file is outside the footage cleanup scope")
		}
		current, e := inspectCleanupFile(ctx, p, f.Path, f.Kind, plan.Cutoff)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil || current.Bytes != f.Bytes || current.SHA256 != f.SHA256 || !current.Modified.Equal(f.Modified) {
			return result, errors.New("local footage changed after preview; nothing was deleted")
		}
	}
	objects, err := cleanupList(ctx, client, config)
	if err != nil {
		return result, err
	}
	var keys []string
	for key, s := range expected {
		object, exists := objects[key]
		if !exists {
			continue
		}
		if object.Bytes != s.Bytes || s.Remote != nil && object.ETag != s.Remote.ETag {
			return result, errors.New("R2 object changed after preview; nothing was deleted")
		}
		if s.Remote == nil {
			// An upload may finish between preview and stopping the uploader.
			head, e := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(config.Bucket), Key: aws.String(key)})
			if e != nil || aws.ToInt64(head.ContentLength) != s.Bytes || head.Metadata["sha256"] != s.SHA256 || head.Metadata["session"] != s.Session {
				return result, errors.New("newly completed R2 upload could not be matched to the manifest")
			}
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for begin := 0; begin < len(keys); begin += 1000 {
		end := min(begin+1000, len(keys))
		batch := make([]types.ObjectIdentifier, 0, end-begin)
		for _, key := range keys[begin:end] {
			batch = append(batch, types.ObjectIdentifier{Key: aws.String(key)})
		}
		out, e := client.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(config.Bucket), Delete: &types.Delete{Objects: batch, Quiet: aws.Bool(false)}})
		if e != nil {
			return result, errors.New("R2 deletion request failed; local footage was kept")
		}
		for _, deleted := range out.Deleted {
			result.RemoteDeleted = append(result.RemoteDeleted, aws.ToString(deleted.Key))
		}
		if len(out.Errors) > 0 {
			return result, errors.New("R2 refused some exact-key deletions; local footage was kept")
		}
	}
	remaining, err := cleanupList(ctx, client, config)
	if err != nil {
		return result, err
	}
	for key := range expected {
		if _, exists := remaining[key]; exists {
			return result, errors.New("R2 still lists a selected object; local footage was kept")
		}
	}
	root, err := os.OpenRoot(p.Root)
	if err != nil {
		return result, err
	}
	defer root.Close()
	for _, f := range plan.Files {
		// All hashes were checked before the first deletion. Re-check identity
		// metadata immediately before unlink; the stopped supervisor owns no writers.
		info, e := cleanupFileInfo(p.Root, f.Path)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil || info.Size() != f.Bytes || !info.ModTime().Equal(f.Modified) {
			return result, errors.New("local footage changed during cleanup")
		}
		if e = root.Remove(f.Path); e != nil {
			return result, errors.New("could not delete a selected local footage file")
		}
		result.LocalDeleted = append(result.LocalDeleted, f.Path)
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	for _, s := range plan.Recordings {
		if s.Main {
			res, e := tx.ExecContext(ctx, "DELETE FROM recordings WHERE path=? AND sha256=? AND bytes=?", s.Path, s.SHA256, s.Bytes)
			if e != nil {
				return result, e
			}
			n, _ := res.RowsAffected()
			result.CatalogDeleted += n
		}
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func (p FootageCleanup) Summary() string {
	var bytes int64
	remote := 0
	for _, f := range p.Files {
		bytes += f.Bytes
	}
	for _, s := range p.Recordings {
		if s.Remote != nil {
			remote++
		}
	}
	return fmt.Sprintf("%d local footage files (%d bytes), %d existing R2 objects, %d unrelated or newer R2 objects preserved; cutoff %s", len(p.Files), bytes, remote, len(p.UnknownR2), p.Cutoff.Format(time.RFC3339))
}
