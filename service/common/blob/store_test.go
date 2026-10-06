package blob

import (
	"context"
	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"io"
	"os"
	"strings"
	"testing"
)

func TestLocalRangeAndImmutableCommit(t *testing.T) {
	store, e := NewLocal(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	if _, e = store.Put(ctx, "originals/source", strings.NewReader("abcdef"), 6, "text/plain"); e != nil {
		t.Fatal(e)
	}
	if _, e = store.Put(ctx, "originals/source", strings.NewReader("other"), 5, ""); e == nil {
		t.Fatal("overwrote immutable source")
	}
	r, e := store.Open(ctx, "originals/source", 2, 3)
	if e != nil {
		t.Fatal(e)
	}
	data, _ := io.ReadAll(r)
	r.Close()
	if string(data) != "cde" {
		t.Fatal(string(data))
	}
	if _, e = store.Open(ctx, "../outside", 0, -1); e == nil {
		t.Fatal("accepted outside object path")
	}
}
func TestPhysicalS3StreamingAndRange(t *testing.T) {
	endpoint := os.Getenv("BTW_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("requires isolated S3-compatible storage")
	}
	ctx := context.Background()
	client, e := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(os.Getenv("BTW_TEST_S3_ACCESS_KEY"), os.Getenv("BTW_TEST_S3_SECRET_KEY"), ""), Secure: os.Getenv("BTW_TEST_S3_SECURE") == "1"})
	if e != nil {
		t.Fatal(e)
	}
	bucket := os.Getenv("BTW_TEST_S3_BUCKET")
	key := "originals/" + uuid.NewString()
	store := &S3{Client: client, Bucket: bucket, Prefix: "btw-test"}
	defer store.Delete(ctx, key)
	size := 17 << 20
	if _, e = store.Put(ctx, key, strings.NewReader(strings.Repeat("z", size)), -1, "text/plain"); e != nil {
		t.Fatal(e)
	}
	r, e := store.Open(ctx, key, 16<<20, 16)
	if e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(r)
	r.Close()
	if e != nil || string(data) != strings.Repeat("z", 16) {
		t.Fatalf("S3 range failed %v", e)
	}
}
