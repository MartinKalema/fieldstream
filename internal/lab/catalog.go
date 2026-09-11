package lab

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Catalog contains only finished recording segments. It never owns or deletes
// the recording files. A single SQL connection serializes short transactions;
// file hashing and network requests happen outside those transactions.
type Catalog struct {
	db    *sql.DB
	paths Paths
}

type CatalogSegment struct {
	Segment
	UploadState string     `json:"upload_state"`
	Attempts    int        `json:"attempts"`
	LastError   string     `json:"last_error,omitempty"`
	ObjectKey   string     `json:"object_key,omitempty"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
	Missing     bool       `json:"missing"`
}

type CatalogSummary struct {
	Segments       int   `json:"segments"`
	Bytes          int64 `json:"bytes"`
	Pending        int   `json:"pending"`
	PendingBytes   int64 `json:"pending_bytes"`
	Archived       int   `json:"archived"`
	ArchivedBytes  int64 `json:"archived_bytes"`
	Missing        int   `json:"missing"`
	LocalSegments  int   `json:"local_segments"`
	LocalBytes     int64 `json:"local_bytes"`
	PendingMissing int   `json:"pending_missing"`
}

func OpenCatalog(p Paths) (*Catalog, error) {
	if err := os.MkdirAll(p.Local, 0700); err != nil {
		return nil, err
	}
	name := filepath.Join(p.Local, "recordings.sqlite")
	if info, err := os.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("recording catalog must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open recording catalog: %w", err)
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: name}
	query := url.Values{}
	// Apply connection settings through the driver so replacement connections
	// retain the same durability and bounded lock-wait settings.
	query.Add("_pragma", "busy_timeout(1500)")
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "synchronous(FULL)")
	query.Add("_pragma", "fullfsync(ON)")
	query.Add("_pragma", "checkpoint_fullfsync(ON)")
	query.Add("_pragma", "foreign_keys(ON)")
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	c := &Catalog{db: db, paths: p}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var schemaVersion int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schemaVersion); err != nil {
		db.Close()
		return nil, err
	}
	if schemaVersion > 2 {
		db.Close()
		return nil, errors.New("recording catalog was created by a newer version of the lab")
	}
	if schemaVersion == 2 {
		return c, nil
	}
	// Schema changes and their version marker commit together. A crash during
	// migration leaves either the complete old schema or the complete new one.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		db.Close()
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS recordings (
		path TEXT PRIMARY KEY, session TEXT NOT NULL, bytes INTEGER NOT NULL CHECK(bytes > 0),
		sha256 TEXT NOT NULL, duration REAL NOT NULL CHECK(duration >= 0),
		upload_state TEXT NOT NULL DEFAULT 'pending' CHECK(upload_state IN ('pending','uploading','archived')),
		attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
		object_key TEXT NOT NULL DEFAULT '', archived_at INTEGER,
		missing INTEGER NOT NULL DEFAULT 0, next_attempt_at INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS recordings_pending ON recordings(upload_state, missing, next_attempt_at, path);
	CREATE TABLE IF NOT EXISTS catalog_metadata (name TEXT PRIMARY KEY, value TEXT NOT NULL);
	ALTER TABLE recordings ADD COLUMN source_id TEXT NOT NULL DEFAULT 'camera-01';
	CREATE INDEX recordings_source ON recordings(source_id,path);
	PRAGMA user_version=2;`)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		tx.Rollback()
		db.Close()
		return nil, fmt.Errorf("prepare recording catalog: %w", err)
	}
	return c, nil
}

func (c *Catalog) Close() error { return c.db.Close() }

const catalogDefaultSourceID = "camera-01"

var catalogSourceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

var ErrRecordingChanged = errors.New("finished recording changed; catalog entry was preserved")

func normalizedCatalogSourceID(source string) string {
	if source == "" {
		return catalogDefaultSourceID
	}
	return source
}

func validateSegment(s Segment) error {
	if !catalogSourceIDPattern.MatchString(normalizedCatalogSourceID(s.SourceID)) {
		return errors.New("recording source_id must start with a lowercase letter and contain at most 32 lowercase letters, digits or hyphens")
	}
	if s.Path == "" || filepath.IsAbs(s.Path) || filepath.Clean(s.Path) != s.Path ||
		strings.Contains(s.Path, "\\") || strings.ContainsRune(s.Path, 0) ||
		s.Path == ".." || strings.HasPrefix(s.Path, ".."+string(os.PathSeparator)) ||
		filepath.Ext(s.Path) != ".mp4" || filepath.Dir(s.Path) != s.Session ||
		s.Session == "." || s.Session == "" || filepath.Base(s.Session) != s.Session {
		return errors.New("recording must have a relative session/file.mp4 path")
	}
	digest, err := hex.DecodeString(s.SHA256)
	if err != nil || len(digest) != 32 || s.SHA256 != strings.ToLower(s.SHA256) || s.Bytes <= 0 ||
		math.IsNaN(s.Duration) || math.IsInf(s.Duration, 0) || s.Duration < 0 {
		return errors.New("recording has invalid size, duration or SHA-256")
	}
	return nil
}

// UpsertSegment is repeatable after a crash. An existing path cannot silently
// acquire different contents or lose its upload history.
func (c *Catalog) UpsertSegment(ctx context.Context, s Segment) error {
	s.SourceID = normalizedCatalogSourceID(s.SourceID)
	if err := validateSegment(s); err != nil {
		return err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO recordings(path,session,bytes,sha256,duration,created_at,source_id)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT(path) DO NOTHING`, s.Path, s.Session, s.Bytes, s.SHA256, s.Duration, time.Now().UnixMilli(), s.SourceID)
	if err != nil {
		return err
	}
	var session, checksum, sourceID string
	var size int64
	var duration float64
	if err = tx.QueryRowContext(ctx, `SELECT session,bytes,sha256,duration,source_id FROM recordings WHERE path=?`, s.Path).
		Scan(&session, &size, &checksum, &duration, &sourceID); err != nil {
		return err
	}
	if session != s.Session || size != s.Bytes || checksum != s.SHA256 || duration != s.Duration || sourceID != s.SourceID {
		return ErrRecordingChanged
	}
	if _, err = tx.ExecContext(ctx, `UPDATE recordings SET missing=0 WHERE path=?`, s.Path); err != nil {
		return err
	}
	return tx.Commit()
}

const segmentColumns = "path,session,bytes,sha256,duration,upload_state,attempts,last_error,object_key,archived_at,missing,source_id"

type sqlScanner interface{ Scan(...any) error }

func scanCatalogSegment(row sqlScanner) (CatalogSegment, error) {
	var s CatalogSegment
	var archived sql.NullInt64
	err := row.Scan(&s.Path, &s.Session, &s.Bytes, &s.SHA256, &s.Duration, &s.UploadState, &s.Attempts, &s.LastError, &s.ObjectKey, &archived, &s.Missing, &s.SourceID)
	if archived.Valid {
		value := time.UnixMilli(archived.Int64).UTC()
		s.ArchivedAt = &value
	}
	return s, err
}

// GetSegment returns sql.ErrNoRows when the segment has not been catalogued.
func (c *Catalog) GetSegment(ctx context.Context, path string) (CatalogSegment, error) {
	return scanCatalogSegment(c.db.QueryRowContext(ctx, `SELECT `+segmentColumns+` FROM recordings WHERE path=?`, path))
}

func (c *Catalog) ListSegments(ctx context.Context, limit, offset int) ([]CatalogSegment, error) {
	if limit < 1 || limit > 1000 || offset < 0 {
		return nil, errors.New("catalog page size must be 1–1000 with a nonnegative offset")
	}
	rows, err := c.db.QueryContext(ctx, `SELECT `+segmentColumns+` FROM recordings ORDER BY path LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]CatalogSegment, 0)
	for rows.Next() {
		s, e := scanCatalogSegment(rows)
		if e != nil {
			return nil, e
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

const summaryColumns = `COUNT(*),COALESCE(SUM(bytes),0),
		COALESCE(SUM(upload_state!='archived'),0),COALESCE(SUM(CASE WHEN upload_state!='archived' THEN bytes ELSE 0 END),0),
		COALESCE(SUM(upload_state='archived'),0),COALESCE(SUM(CASE WHEN upload_state='archived' THEN bytes ELSE 0 END),0),
		COALESCE(SUM(missing),0),COALESCE(SUM(missing=0),0),
		COALESCE(SUM(CASE WHEN missing=0 THEN bytes ELSE 0 END),0),
		COALESCE(SUM(missing=1 AND upload_state!='archived'),0)`

func summaryScanTargets(s *CatalogSummary) []any {
	return []any{&s.Segments, &s.Bytes, &s.Pending, &s.PendingBytes, &s.Archived, &s.ArchivedBytes, &s.Missing, &s.LocalSegments, &s.LocalBytes, &s.PendingMissing}
}

func (c *Catalog) Summary(ctx context.Context) (CatalogSummary, error) {
	var s CatalogSummary
	err := c.db.QueryRowContext(ctx, `SELECT `+summaryColumns+` FROM recordings`).Scan(summaryScanTargets(&s)...)
	return s, err
}

// SummariesBySource preserves old source identities even after a device is
// removed from configuration, so its recording and archive history stays visible.
func (c *Catalog) SummariesBySource(ctx context.Context) (map[string]CatalogSummary, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT source_id,`+summaryColumns+` FROM recordings GROUP BY source_id ORDER BY source_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]CatalogSummary{}
	for rows.Next() {
		var sourceID string
		var summary CatalogSummary
		targets := append([]any{&sourceID}, summaryScanTargets(&summary)...)
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		result[sourceID] = summary
	}
	return result, rows.Err()
}

func (c *Catalog) MarkMissing(ctx context.Context, path string, missing bool) error {
	_, err := c.db.ExecContext(ctx, `UPDATE recordings SET missing=? WHERE path=?`, missing, path)
	return err
}

// ReconcileMissing checks in bounded pages; an unavailable file is never
// forgotten, and its verified R2 status is retained if the local copy is gone.
func (c *Catalog) ReconcileMissing(ctx context.Context) error {
	for offset := 0; ; offset += 100 {
		segments, err := c.ListSegments(ctx, 100, offset)
		if err != nil {
			return err
		}
		for _, s := range segments {
			_, err := os.Stat(filepath.Join(c.paths.Recordings, s.Path))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return errors.New("cannot check a local recording")
			}
			missing := errors.Is(err, os.ErrNotExist)
			if missing != s.Missing {
				if err = c.MarkMissing(ctx, s.Path, missing); err != nil {
					return err
				}
			}
		}
		if len(segments) < 100 {
			return nil
		}
	}
}

func (c *Catalog) recoverUploads(ctx context.Context) error {
	_, err := c.db.ExecContext(ctx, `UPDATE recordings SET upload_state='pending',next_attempt_at=0,
		last_error='upload interrupted; safe retry pending' WHERE upload_state='uploading'`)
	return err
}

func (c *Catalog) bindArchiveDestination(ctx context.Context, destination string) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_metadata(name,value) VALUES('archive_destination',?) ON CONFLICT(name) DO NOTHING`, destination); err != nil {
		return err
	}
	var stored string
	if err = tx.QueryRowContext(ctx, `SELECT value FROM catalog_metadata WHERE name='archive_destination'`).Scan(&stored); err != nil {
		return err
	}
	if stored != destination {
		return errors.New("archive destination changed; existing archive history must be migrated before uploading")
	}
	return tx.Commit()
}

func (c *Catalog) claimUpload(ctx context.Context) (CatalogSegment, error) {
	// UPDATE ... RETURNING makes claim and attempt counting one atomic action.
	// Among eligible jobs, rowid follows catalog insertion order. Sorting by
	// source-prefixed filenames would let a busy early-named source starve other
	// sources; sorting by retry time would let fresh jobs starve retries forever.
	// The rowid orders work only; it is never used as a recording/object identity.
	return scanCatalogSegment(c.db.QueryRowContext(ctx, `UPDATE recordings SET upload_state='uploading',attempts=attempts+1
		WHERE path=(SELECT path FROM recordings WHERE upload_state='pending' AND missing=0 AND next_attempt_at<=?
		ORDER BY rowid LIMIT 1) RETURNING `+segmentColumns, time.Now().UnixMilli()))
}

func (c *Catalog) uploadFailed(ctx context.Context, path, safeError string, retryAt time.Time) error {
	_, err := c.db.ExecContext(ctx, `UPDATE recordings SET upload_state='pending',last_error=?,next_attempt_at=? WHERE path=? AND upload_state='uploading'`, safeError, retryAt.UnixMilli(), path)
	return err
}

func (c *Catalog) uploadSucceeded(ctx context.Context, path, objectKey string) error {
	_, err := c.db.ExecContext(ctx, `UPDATE recordings SET upload_state='archived',last_error='',object_key=?,archived_at=? WHERE path=? AND upload_state='uploading'`, objectKey, time.Now().UnixMilli(), path)
	return err
}
