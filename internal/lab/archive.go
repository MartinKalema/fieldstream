package lab

import (
	"context"
	"crypto/md5" // R2 Content-MD5 checks transfer corruption; SHA-256 remains the file identity.
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/middleware"
)

const DefaultArchiveBytesPerSecond int64 = 125_000
const maxUploadSegmentBytes int64 = 64 << 20

type R2Config struct {
	Enabled         bool   `json:"enabled"`
	AccountID       string `json:"account_id"`
	Bucket          string `json:"bucket"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Prefix          string `json:"prefix"`
	BytesPerSecond  int64  `json:"bytes_per_second"`
}

// WriteR2Template never overwrites credentials or enables uploading.
func (p Paths) WriteR2Template() (string, error) {
	if err := os.MkdirAll(p.Local, 0700); err != nil {
		return "", err
	}
	name := filepath.Join(p.Local, "r2.json")
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return name, nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := json.MarshalIndent(R2Config{Prefix: "field-video-lab", BytesPerSecond: DefaultArchiveBytesPerSecond}, "", "  ")
	if err != nil {
		return "", err
	}
	if _, err = f.Write(append(data, '\n')); err != nil {
		return "", err
	}
	if err = f.Sync(); err != nil {
		return "", err
	}
	dir, err := os.Open(p.Local)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return "", err
	}
	return name, nil
}

func LoadR2Config(p Paths) (*R2Config, error) {
	name := filepath.Join(p.Local, "r2.json")
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("cannot read private R2 configuration")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("R2 configuration must be a regular private file with permissions 0600")
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, errors.New("cannot read private R2 configuration")
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 16<<10))
	decoder.DisallowUnknownFields()
	var config R2Config
	if decoder.Decode(&config) != nil {
		return nil, errors.New("R2 configuration contains invalid or unknown fields")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("R2 configuration contains extra data")
	}
	if err = normalizeR2Config(&config); err != nil {
		return nil, err
	}
	return &config, nil
}

func normalizeR2Config(c *R2Config) error {
	if c.BytesPerSecond == 0 {
		c.BytesPerSecond = DefaultArchiveBytesPerSecond
	}
	if c.BytesPerSecond < 16_384 || c.BytesPerSecond > 100_000_000 {
		return errors.New("R2 bytes_per_second must be between 16384 and 100000000")
	}
	if c.Prefix == "" {
		c.Prefix = "field-video-lab"
	}
	if len(c.Prefix) > 200 || strings.HasPrefix(c.Prefix, "/") || path.Clean(c.Prefix) != c.Prefix ||
		strings.Contains(c.Prefix, "\\") || strings.ContainsRune(c.Prefix, 0) || c.Prefix == ".." || strings.HasPrefix(c.Prefix, "../") {
		return errors.New("R2 prefix must be a short relative object path without parent-directory components")
	}
	if !c.Enabled {
		return nil
	}
	if !regexp.MustCompile(`^[a-fA-F0-9]{32}$`).MatchString(c.AccountID) {
		return errors.New("R2 account_id must contain 32 hexadecimal characters")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`).MatchString(c.Bucket) {
		return errors.New("R2 bucket must be its existing lowercase bucket name")
	}
	if len(c.AccessKeyID) < 8 || len(c.SecretAccessKey) < 16 || strings.TrimSpace(c.AccessKeyID) != c.AccessKeyID || strings.TrimSpace(c.SecretAccessKey) != c.SecretAccessKey {
		return errors.New("R2 access credentials are missing or invalid")
	}
	return nil
}

type ArchiveState struct {
	Configured      bool           `json:"configured"`
	Enabled         bool           `json:"enabled"`
	Running         bool           `json:"running"`
	Uploading       bool           `json:"uploading"`
	CurrentPath     string         `json:"current_path,omitempty"`
	CurrentSourceID string         `json:"current_source_id,omitempty"`
	LastError       string         `json:"last_error,omitempty"`
	BytesPerSecond  int64          `json:"bytes_per_second"`
	Summary         CatalogSummary `json:"summary"`
}

type objectArchive interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
}

type Archiver struct {
	paths   Paths
	catalog *Catalog
	config  R2Config
	client  objectArchive
	mu      sync.RWMutex
	state   ArchiveState
}

func NewArchiver(p Paths, catalog *Catalog, config R2Config) (*Archiver, error) {
	if err := normalizeR2Config(&config); err != nil {
		return nil, err
	}
	if catalog == nil {
		return nil, errors.New("recording catalog is required for archival")
	}
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout: 30 * time.Second, MaxIdleConns: 2, MaxIdleConnsPerHost: 1,
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	archive := &Archiver{paths: p, catalog: catalog, config: config}
	archive.state = ArchiveState{Configured: true, Enabled: config.Enabled, BytesPerSecond: config.BytesPerSecond}
	archive.client = newR2Client(config, "https://"+config.AccountID+".r2.cloudflarestorage.com", client)
	return archive, nil
}

// Only tests supply a local endpoint. User configuration cannot redirect keys
// or recordings to a different host, and SDK errors never enter logs verbatim.
func newR2Client(config R2Config, endpoint string, client *http.Client) *s3.Client {
	return s3.New(s3.Options{
		Region: "auto", BaseEndpoint: aws.String(endpoint), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(config.AccessKeyID, config.SecretAccessKey, ""),
		HTTPClient:  client, RetryMaxAttempts: 1,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		APIOptions:                 []func(*middleware.Stack) error{v4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware},
	})
}

func (a *Archiver) Snapshot() ArchiveState { a.mu.RLock(); defer a.mu.RUnlock(); return a.state }

func (a *Archiver) updateState(change func(*ArchiveState)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	change(&a.state)
}

func (a *Archiver) refreshSummary(ctx context.Context) error {
	summary, err := a.catalog.Summary(ctx)
	if err == nil {
		a.updateState(func(s *ArchiveState) { s.Summary = summary })
	}
	return err
}

// Run is a single-worker queue, entirely separate from the live-video workers.
// A completed HTTP upload followed by a crash before the SQL commit repeats the
// same content-addressed PUT after restart, rather than inventing a new object.
func (a *Archiver) Run(ctx context.Context) (runError error) {
	defer func() {
		if runError != nil {
			a.updateState(func(s *ArchiveState) { s.LastError = safeArchiveError(runError) })
		}
	}()
	if !a.config.Enabled {
		return a.refreshSummary(ctx)
	}
	lock, err := fileLock(filepath.Join(a.paths.Local, "archive.lock"), true)
	if err != nil {
		return errors.New("another archive worker is already running")
	}
	defer lock.Close()
	if err = a.catalog.bindArchiveDestination(ctx, a.config.AccountID+"/"+a.config.Bucket+"/"+a.config.Prefix); err != nil {
		return err
	}
	if err = a.catalog.recoverUploads(ctx); err != nil {
		return err
	}
	a.updateState(func(s *ArchiveState) { s.Running = true })
	defer a.updateState(func(s *ArchiveState) {
		s.Running = false
		s.Uploading = false
		s.CurrentPath = ""
		s.CurrentSourceID = ""
	})
	for {
		if ctx.Err() != nil {
			return nil
		}
		// Only this worker owns the archive lock. Repair a previous attempt whose
		// completion could not be committed, without requiring a process restart.
		if err = a.catalog.recoverUploads(ctx); err != nil {
			return errors.New("archive catalog is unavailable")
		}
		worked, err := a.uploadNext(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			a.updateState(func(s *ArchiveState) { s.LastError = safeArchiveError(err) })
		}
		if e := a.refreshSummary(ctx); e != nil {
			return errors.New("archive catalog is unavailable")
		}
		if worked && err == nil {
			continue
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (a *Archiver) uploadNext(ctx context.Context) (bool, error) {
	s, err := a.catalog.claimUpload(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("archive catalog is unavailable")
	}
	a.updateState(func(state *ArchiveState) {
		state.Uploading = true
		state.CurrentPath = s.Path
		state.CurrentSourceID = s.SourceID
	})
	defer a.updateState(func(state *ArchiveState) { state.Uploading = false; state.CurrentPath = ""; state.CurrentSourceID = "" })
	key := path.Join(a.config.Prefix, s.Session, strings.TrimSuffix(filepath.Base(s.Path), ".mp4")+"-"+s.SHA256+".mp4")
	err = a.uploadFile(ctx, s, key)
	if err == nil {
		if err = a.catalog.uploadSucceeded(ctx, s.Path, key); err == nil {
			a.updateState(func(state *ArchiveState) { state.LastError = "" })
			return true, nil
		}
		return true, errors.New("archive confirmation could not be saved; safe retry pending after restart")
	}
	// A canceled operation still needs a short, independent SQL cleanup. If power
	// disappears here, recoverUploads repairs its 'uploading' state next startup.
	cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if errors.Is(err, os.ErrNotExist) {
		_ = a.catalog.MarkMissing(cleanup, s.Path, true)
	}
	delay := 5 * time.Second
	for n := 1; n < s.Attempts && delay < 5*time.Minute; n++ {
		delay *= 2
	}
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	safeError := safeArchiveError(err)
	if e := a.catalog.uploadFailed(cleanup, s.Path, safeError, time.Now().Add(delay)); e != nil {
		return true, errors.New("archive retry state could not be saved")
	}
	return true, errors.New(safeError)
}

func (a *Archiver) uploadFile(parent context.Context, s CatalogSegment, key string) error {
	if err := validateSegment(s.Segment); err != nil {
		return err
	}
	if s.Bytes > maxUploadSegmentBytes {
		return errors.New("recording exceeds the 64 MiB single-segment upload limit")
	}
	// Enough time for this capped transfer plus network overhead; cancellation
	// from the supervisor remains immediate even with a very low rate limit.
	budget := time.Duration(s.Bytes/a.config.BytesPerSecond+60) * time.Second
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	root, err := os.OpenRoot(a.paths.Recordings)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.Open(s.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != s.Bytes {
		return errors.New("local recording size changed; upload paused")
	}
	sha := sha256.New()
	md := md5.New()
	count, err := io.Copy(io.MultiWriter(sha, md), archiveContextReader{ctx: ctx, reader: f})
	if err != nil {
		return err
	}
	if count != s.Bytes || hex.EncodeToString(sha.Sum(nil)) != s.SHA256 {
		return errors.New("local recording SHA-256 changed; upload paused")
	}
	md5sum := base64.StdEncoding.EncodeToString(md.Sum(nil))
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	body := &pacedReadSeeker{ctx: ctx, source: f, bytesPerSecond: a.config.BytesPerSecond}
	_, err = a.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(a.config.Bucket), Key: aws.String(key), Body: body,
		ContentLength: aws.Int64(s.Bytes), ContentType: aws.String("video/mp4"), ContentMD5: aws.String(md5sum),
		Metadata: map[string]string{"sha256": s.SHA256, "session": s.Session, "source-id": normalizedCatalogSourceID(s.SourceID)},
	})
	if err != nil {
		return err
	}
	// R2 supports Content-MD5 on PUT and rejects a body/checksum mismatch. HEAD
	// additionally confirms the stored size and identity metadata. Metadata by
	// itself is not treated as an independent checksum of the stored payload.
	head, err := a.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(a.config.Bucket), Key: aws.String(key)})
	if err != nil {
		return err
	}
	if aws.ToInt64(head.ContentLength) != s.Bytes || head.Metadata["sha256"] != s.SHA256 || head.Metadata["session"] != s.Session || head.Metadata["source-id"] != normalizedCatalogSourceID(s.SourceID) {
		return errors.New("R2 object confirmation does not match the recording")
	}
	return nil
}

// Deliberately exclude remote messages, URLs, request IDs and credentials.
func safeArchiveError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "upload interrupted; safe retry pending"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "R2 upload timed out; retry pending"
	}
	if errors.Is(err, os.ErrNotExist) {
		return "local recording is missing; upload paused"
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch":
			return "R2 access was rejected; check the private credentials and bucket permissions"
		case "NoSuchBucket":
			return "R2 bucket does not exist or is unavailable to this account"
		case "BadDigest", "InvalidDigest":
			return "R2 rejected the upload checksum; retry pending"
		case "SlowDown", "InternalError", "ServiceUnavailable":
			return "R2 is temporarily unavailable; retry pending"
		}
		return "R2 request failed; retry pending"
	}
	var network net.Error
	if errors.As(err, &network) {
		return "R2 network connection failed; retry pending"
	}
	// Local errors are selected from a fixed allowlist, never reflected from an
	// SDK/server response or arbitrary filesystem path.
	for _, message := range []string{
		"recording exceeds the 64 MiB single-segment upload limit",
		"local recording size changed; upload paused",
		"local recording SHA-256 changed; upload paused",
		"R2 object confirmation does not match the recording",
		"archive catalog is unavailable",
		"archive confirmation could not be saved; safe retry pending after restart",
		"archive retry state could not be saved",
		"another archive worker is already running",
		"archive destination changed; existing archive history must be migrated before uploading",
		"R2 access was rejected; check the private credentials and bucket permissions",
		"R2 bucket does not exist or is unavailable to this account",
		"R2 rejected the upload checksum; retry pending",
		"R2 is temporarily unavailable; retry pending",
		"R2 request failed; retry pending",
		"R2 network connection failed; retry pending",
		"upload interrupted; safe retry pending",
		"R2 upload timed out; retry pending",
		"local recording is missing; upload paused",
	} {
		if err.Error() == message {
			return message
		}
	}
	return "archive operation failed; retry pending"
}

type archiveContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r archiveContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Limit each body read to 32 KiB, then wait before returning those bytes to the
// HTTP transport. Seek resets accounting when the SDK prepares the request.
type pacedReadSeeker struct {
	ctx            context.Context
	source         io.ReadSeeker
	bytesPerSecond int64
	next           time.Time
}

func (r *pacedReadSeeker) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > 32<<10 {
		p = p[:32<<10]
	}
	n, err := r.source.Read(p)
	if n > 0 {
		now := time.Now()
		if r.next.Before(now) {
			r.next = now
		}
		r.next = r.next.Add(time.Duration(float64(n) / float64(r.bytesPerSecond) * float64(time.Second)))
		timer := time.NewTimer(time.Until(r.next))
		select {
		case <-r.ctx.Done():
			timer.Stop()
			return 0, r.ctx.Err()
		case <-timer.C:
		}
	}
	return n, err
}
func (r *pacedReadSeeker) Seek(offset int64, whence int) (int64, error) {
	r.next = time.Time{}
	return r.source.Seek(offset, whence)
}
