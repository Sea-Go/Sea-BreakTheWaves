package artifacts_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// The dedicated parent starts a task-owned SeaweedFS S3 server. Ordinary Go
// tests skip this integration; no configured production bucket is contacted.
func TestSharedS3WikiBytesAreReadableByRTWKeyAndCorruptionRejects(t *testing.T) {
	host := os.Getenv("SEA_ARTIFACT_S3_HOST")
	bucket := os.Getenv("SEA_ARTIFACT_S3_BUCKET")
	if host == "" || bucket == "" {
		t.Skip("run scripts/test-shared-s3-artifacts.py with isolated SeaweedFS")
	}
	construct := func() *minio.Client {
		t.Helper()
		client, err := minio.New(host, &minio.Options{
			Creds:  credentials.NewStaticV4("", "", ""),
			Secure: false,
		})
		if err != nil {
			t.Fatal("task-owned S3 client could not be constructed")
		}
		return client
	}
	client, reader := construct(), construct()
	if _, err := artifacts.NewS3(nil, bucket); !errors.Is(err, artifacts.ErrS3Artifact) {
		t.Fatal("nil S3 client accepted")
	}
	shared, err := artifacts.NewS3(client, bucket)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	markdown := []byte("# 固定 Wiki 编制候选\n\n    四空格代码\n来源 revision-1 paragraph:1。\n")
	ref, err := shared.Put(ctx, markdown)
	if err != nil || ref.Key != "sha256/"+artifacts.Hash(markdown) ||
		ref.SHA256 != artifacts.Hash(markdown) {
		t.Fatalf("BTW shared S3 candidate key/hash mismatch: ref=%+v err=%v", ref, err)
	}
	// RTW knowledge's object.Store uses the same bucket and key/hash. A second
	// S3 client must read the exact content under that durable reference.
	object, err := reader.GetObject(ctx, bucket, ref.Key, minio.GetObjectOptions{})
	if err != nil {
		t.Fatal("RTW-shaped S3 read could not open candidate")
	}
	body, readErr := io.ReadAll(io.LimitReader(object, artifacts.MaxBytes+1))
	_ = object.Close()
	if readErr != nil || !bytes.Equal(body, markdown) || artifacts.Hash(body) != ref.SHA256 {
		t.Fatal("second S3 client did not recover RTW-readable original Markdown")
	}
	again, err := shared.Put(ctx, markdown)
	if err != nil || again != ref {
		t.Fatal("same original bytes changed the content-addressed object key")
	}
	wrong := corpus.Ref{Key: "sha256/" + artifacts.Hash([]byte("other")), SHA256: ref.SHA256}
	if _, err := shared.Get(ctx, wrong); !errors.Is(err, artifacts.ErrS3Artifact) {
		t.Fatal("wrong key/hash relation was read from shared S3")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := shared.Put(cancelled, markdown); !errors.Is(err, artifacts.ErrS3Artifact) {
		t.Fatal("cancelled candidate upload entered shared S3")
	}
	_, err = reader.PutObject(ctx, bucket, ref.Key, bytes.NewReader([]byte("tampered")),
		int64(len("tampered")), minio.PutObjectOptions{})
	if err != nil {
		t.Fatal("task-owned corruption injection could not replace candidate")
	}
	if _, err := shared.Get(ctx, ref); !errors.Is(err, artifacts.ErrS3Artifact) {
		t.Fatal("RTW-readable key returned modified bytes under the old hash")
	}
	if _, err := shared.Put(ctx, markdown); err != nil {
		t.Fatal("same original content could not restore task-owned candidate")
	}
	if restored, err := shared.Get(ctx, ref); err != nil || !bytes.Equal(restored, markdown) {
		t.Fatal("restored original Markdown remained unreadable")
	}
}
