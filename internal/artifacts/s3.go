package artifacts

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/minio/minio-go/v7"
)

var ErrS3Artifact = errors.New("shared immutable S3 artifact unavailable or corrupt")

// S3 writes the same sha256/<hash> key RTW's knowledge ObjectStore reads in
// its configured bucket. The app layer owns client credentials and lifecycle;
// this adapter never selects a bucket or endpoint from an Agent result.
type S3 struct {
	client *minio.Client
	bucket string
}

func NewS3(client *minio.Client, bucket string) (*S3, error) {
	if client == nil || bucket == "" || strings.TrimSpace(bucket) != bucket {
		return nil, ErrS3Artifact
	}
	return &S3{client: client, bucket: bucket}, nil
}

func (s *S3) Put(ctx context.Context, data []byte) (corpus.Ref, error) {
	if s == nil || s.client == nil || ctx == nil || ctx.Err() != nil ||
		len(data) > MaxBytes {
		return corpus.Ref{}, ErrS3Artifact
	}
	ref := Reference(data)
	_, err := s.client.PutObject(ctx, s.bucket, ref.Key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/octet-stream"})
	if err != nil {
		return corpus.Ref{}, ErrS3Artifact
	}
	// A successful PUT response is not enough for RTW AcceptCompile. Prove
	// exact readback under the same key/hash before handing the ref onward.
	if _, err := s.Get(ctx, ref); err != nil {
		return corpus.Ref{}, err
	}
	return ref, nil
}

func (s *S3) Get(ctx context.Context, ref corpus.Ref) ([]byte, error) {
	if s == nil || s.client == nil || ctx == nil || ctx.Err() != nil ||
		!ValidHash(ref.SHA256) || ref.Key != "sha256/"+ref.SHA256 {
		return nil, ErrS3Artifact
	}
	object, err := s.client.GetObject(ctx, s.bucket, ref.Key, minio.GetObjectOptions{})
	if err != nil {
		return nil, ErrS3Artifact
	}
	defer object.Close()
	data, err := io.ReadAll(io.LimitReader(object, MaxBytes+1))
	if err != nil || ctx.Err() != nil || len(data) > MaxBytes ||
		Hash(data) != ref.SHA256 {
		return nil, ErrS3Artifact
	}
	return data, nil
}

var _ Store = (*S3)(nil)
