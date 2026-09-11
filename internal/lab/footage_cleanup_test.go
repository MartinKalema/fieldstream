package lab

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type cleanupFixtureObjects struct {
	objects     map[string]CleanupObject
	head        map[string]string
	deleteCalls int
	refuse      bool
}

func (f *cleanupFixtureObjects) ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	out := &s3.ListObjectsV2Output{}
	for _, v := range f.objects {
		out.Contents = append(out.Contents, types.Object{Key: aws.String(v.Key), Size: aws.Int64(v.Bytes), ETag: aws.String(v.ETag), LastModified: aws.Time(v.Modified)})
	}
	return out, nil
}
func (f *cleanupFixtureObjects) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	v, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, errors.New("missing")
	}
	return &s3.HeadObjectOutput{ContentLength: aws.Int64(v.Bytes), Metadata: f.head}, nil
}
func (f *cleanupFixtureObjects) DeleteObjects(_ context.Context, in *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	f.deleteCalls++
	out := &s3.DeleteObjectsOutput{}
	if f.refuse {
		out.Errors = []types.Error{{Code: aws.String("AccessDenied")}}
		return out, nil
	}
	for _, v := range in.Delete.Objects {
		delete(f.objects, aws.ToString(v.Key))
		out.Deleted = append(out.Deleted, types.DeletedObject{Key: v.Key})
	}
	return out, nil
}

func cleanupFixture(t *testing.T) (Paths, R2Config, *cleanupFixtureObjects, time.Time, Segment) {
	t.Helper()
	p := NewPaths(t.TempDir())
	ctx := context.Background()
	config := R2Config{AccountID: "test-account", Bucket: "test-bucket", Prefix: "field-video-lab"}
	cutoff := time.Now().Add(-time.Minute).UTC()
	write := func(rel string, data []byte, when time.Time) {
		t.Helper()
		name := filepath.Join(p.Root, rel)
		if e := os.MkdirAll(filepath.Dir(name), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(name, data, 0600); e != nil {
			t.Fatal(e)
		}
		if e := os.Chtimes(name, when, when); e != nil {
			t.Fatal(e)
		}
	}
	data := []byte("completed test footage")
	sum := sha256.Sum256(data)
	s := Segment{SourceID: "camera-01", Path: "session/clip.mp4", Session: "session", Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Duration: 5}
	write("recordings/"+s.Path, data, cutoff.Add(-time.Second))
	write("recordings/session/unfinished.mp4", []byte("unfinished"), cutoff.Add(-time.Second))
	write("reports/test-run-123/generated.mp4", []byte("generated old video"), cutoff.Add(-time.Second))
	write("reports/test-run-123/report.json", []byte(`{"preserve":true}`), cutoff.Add(-time.Second))
	write(".local/diagnostics/new.mp4", []byte("new video"), cutoff.Add(time.Second))
	c, e := OpenCatalog(p)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if e = c.UpsertSegment(ctx, s); e != nil {
		t.Fatal(e)
	}
	if _, e = c.db.ExecContext(ctx, "UPDATE recordings SET created_at=?", cutoff.Add(-time.Second).UnixMilli()); e != nil {
		t.Fatal(e)
	}
	if e = c.bindArchiveDestination(ctx, config.AccountID+"/"+config.Bucket+"/"+config.Prefix); e != nil {
		t.Fatal(e)
	}
	key := cleanupKey(config.Prefix, s)
	client := &cleanupFixtureObjects{objects: map[string]CleanupObject{key: {Key: key, Bytes: s.Bytes, ETag: "original", Modified: cutoff}, config.Prefix + "/unknown.mp4": {Key: config.Prefix + "/unknown.mp4", Bytes: 1}}, head: map[string]string{"sha256": s.SHA256, "session": s.Session}}
	return p, config, client, cutoff, s
}

func TestFootageCleanupFixedScopeAndNoCSVReimport(t *testing.T) {
	p, config, client, cutoff, s := cleanupFixture(t)
	ctx := context.Background()
	plan, e := prepareFootageCleanup(ctx, p, cutoff, config, client)
	if e != nil {
		t.Fatal(e)
	}
	if len(plan.Files) != 2 || len(plan.Recordings) != 1 || len(plan.UnknownR2) != 1 || client.deleteCalls != 0 {
		t.Fatal("preview scope or read-only behavior changed")
	}
	if e = os.WriteFile(filepath.Join(p.Recordings, "session", ".source.json"), []byte(`{"source_id":"camera-01"}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(p.Recordings, "session", "segments.csv"), []byte("clip.mp4,0,5\n"), 0600); e != nil {
		t.Fatal(e)
	}
	result, e := executeFootageCleanup(ctx, p, plan, config, client)
	if e != nil {
		t.Fatal(e)
	}
	if len(result.LocalDeleted) != 2 || len(result.RemoteDeleted) != 1 || result.CatalogDeleted != 1 {
		t.Fatalf("incomplete cleanup: %+v", result)
	}
	for _, kept := range []string{"recordings/session/unfinished.mp4", "reports/test-run-123/report.json", ".local/diagnostics/new.mp4"} {
		if _, e := os.Stat(filepath.Join(p.Root, kept)); e != nil {
			t.Fatalf("removed protected file %s", kept)
		}
	}
	if len(client.objects) != 1 {
		t.Fatal("unknown remote object was removed")
	}
	c, e := OpenCatalog(p)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if _, e = scanRecordings(ctx, p, map[string]Segment{}, c); e != nil {
		t.Fatal(e)
	}
	if _, e = c.GetSegment(ctx, s.Path); e == nil {
		t.Fatal("stale CSV reimported a deleted clip")
	}
	// A repeated execution is safe after an interrupted command or lost output.
	again, e := executeFootageCleanup(ctx, p, plan, config, client)
	if e != nil || len(again.LocalDeleted) != 0 || again.CatalogDeleted != 0 {
		t.Fatal("cleanup replay was not safe", e)
	}
}

func TestFootageCleanupRefusesChangedFilesAndActiveWorkers(t *testing.T) {
	for _, mode := range []string{"changed local", "changed remote", "supervisor active", "archive active", "remote refusal", "escape"} {
		t.Run(mode, func(t *testing.T) {
			p, config, client, cutoff, _ := cleanupFixture(t)
			ctx := context.Background()
			plan, e := prepareFootageCleanup(ctx, p, cutoff, config, client)
			if e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "changed local":
				if e = os.WriteFile(filepath.Join(p.Root, plan.Files[0].Path), []byte("changed"), 0600); e != nil {
					t.Fatal(e)
				}
			case "changed remote":
				for k, v := range client.objects {
					if k == plan.Recordings[0].ObjectKey {
						v.ETag = "replaced"
						client.objects[k] = v
					}
				}
			case "supervisor active", "archive active":
				name := "supervisor.lock"
				if mode == "archive active" {
					name = "archive.lock"
				}
				f, e := fileLock(filepath.Join(p.Local, name), true)
				if e != nil {
					t.Fatal(e)
				}
				defer f.Close()
			case "remote refusal":
				client.refuse = true
			case "escape":
				plan.Files[0].Path = "../outside.mp4"
			}
			_, e = executeFootageCleanup(ctx, p, plan, config, client)
			if e == nil {
				t.Fatal("unsafe cleanup accepted")
			}
			for _, f := range plan.Files {
				if mode == "escape" {
					break
				}
				if _, e := os.Stat(filepath.Join(p.Root, f.Path)); e != nil {
					t.Fatal("local file removed after refused preflight")
				}
			}
			if mode != "remote refusal" && client.deleteCalls != 0 {
				t.Fatal("remote deletion occurred despite failed preflight")
			}
		})
	}
}

func TestFootageCleanupCoversUploadFinishingAfterPreview(t *testing.T) {
	p, config, client, cutoff, s := cleanupFixture(t)
	ctx := context.Background()
	key := cleanupKey(config.Prefix, s)
	object := client.objects[key]
	delete(client.objects, key)
	plan, e := prepareFootageCleanup(ctx, p, cutoff, config, client)
	if e != nil {
		t.Fatal(e)
	}
	if plan.Recordings[0].Remote != nil {
		t.Fatal("unexpected remote object")
	}
	client.objects[key] = object
	if _, e = executeFootageCleanup(ctx, p, plan, config, client); e != nil {
		t.Fatal(e)
	}
	if _, exists := client.objects[key]; exists {
		t.Fatal("in-flight upload escaped cleanup")
	}
}
