// Package blob stores immutable, streamed source objects without whole-object reads.
package blob

import (
	"context"
	"errors"
	"fmt"
	"github.com/minio/minio-go/v7"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Store interface {
	Put(context.Context, string, io.Reader, int64, string) (int64, error)
	Open(context.Context, string, int64, int64) (io.ReadCloser, error)
	Size(context.Context, string) (int64, error)
}

func valid(key string) bool {
	return key != "" && !strings.HasPrefix(key, "/") && !strings.Contains(key, "\\") && filepath.ToSlash(filepath.Clean(key)) == key && key != ".." && !strings.HasPrefix(key, "../")
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

type Local struct{ Root string }

func NewLocal(root string) (*Local, error) {
	p, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(p, 0700); e != nil {
		return nil, e
	}
	return &Local{p}, nil
}
func (s *Local) path(k string) (string, error) {
	if !valid(k) {
		return "", errors.New("invalid object key")
	}
	return filepath.Join(s.Root, k), nil
}
func (s *Local) Put(ctx context.Context, k string, r io.Reader, size int64, _ string) (int64, error) {
	p, e := s.path(k)
	if e != nil {
		return 0, e
	}
	if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		return 0, e
	}
	f, e := os.CreateTemp(filepath.Dir(p), ".upload-")
	if e != nil {
		return 0, e
	}
	defer os.Remove(f.Name())
	n, e := io.Copy(f, contextReader{ctx, r})
	if e == nil && size >= 0 && n != size {
		e = io.ErrUnexpectedEOF
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return n, e
	}
	// Immutable keys: never replace a previously accepted source object.
	if e = os.Link(f.Name(), p); e != nil {
		return n, e
	}
	return n, nil
}

type limitedReadCloser struct {
	io.Reader
	io.Closer
}

func (s *Local) Open(ctx context.Context, k string, start, length int64) (io.ReadCloser, error) {
	p, e := s.path(k)
	if e != nil {
		return nil, e
	}
	if start < 0 {
		return nil, errors.New("negative offset")
	}
	f, e := os.Open(p)
	if e != nil {
		return nil, e
	}
	if _, e = f.Seek(start, 0); e != nil {
		f.Close()
		return nil, e
	}
	var r io.Reader = contextReader{ctx, f}
	if length >= 0 {
		r = io.LimitReader(r, length)
	}
	return &limitedReadCloser{r, f}, nil
}
func (s *Local) Size(ctx context.Context, k string) (int64, error) {
	if e := ctx.Err(); e != nil {
		return 0, e
	}
	p, e := s.path(k)
	if e != nil {
		return 0, e
	}
	i, e := os.Stat(p)
	if e != nil {
		return 0, e
	}
	return i.Size(), nil
}

type S3 struct {
	Client *minio.Client
	Bucket string
	Prefix string
}

func (s *S3) key(k string) (string, error) {
	if !valid(k) {
		return "", errors.New("invalid object key")
	}
	return strings.Trim(s.Prefix, "/") + "/" + k, nil
}
func (s *S3) Put(ctx context.Context, k string, r io.Reader, size int64, media string) (int64, error) {
	key, e := s.key(k)
	if e != nil {
		return 0, e
	}
	out, e := s.Client.PutObject(ctx, s.Bucket, key, contextReader{ctx, r}, size, minio.PutObjectOptions{ContentType: media})
	return out.Size, e
}
func (s *S3) Open(ctx context.Context, k string, start, length int64) (io.ReadCloser, error) {
	key, e := s.key(k)
	if e != nil {
		return nil, e
	}
	if start < 0 {
		return nil, errors.New("negative offset")
	}
	opts := minio.GetObjectOptions{}
	if length == 0 {
		return io.NopCloser(strings.NewReader("")), nil
	}
	if start > 0 || length > 0 {
		end := int64(0)
		if length > 0 {
			end = start + length - 1
		}
		if e = opts.SetRange(start, end); e != nil {
			return nil, e
		}
	}
	obj, e := s.Client.GetObject(ctx, s.Bucket, key, opts)
	if e != nil {
		return nil, e
	}
	if _, e = obj.Stat(); e != nil {
		obj.Close()
		return nil, fmt.Errorf("open object: %w", e)
	}
	return obj, nil
}
func (s *S3) Size(ctx context.Context, k string) (int64, error) {
	key, e := s.key(k)
	if e != nil {
		return 0, e
	}
	out, e := s.Client.StatObject(ctx, s.Bucket, key, minio.StatObjectOptions{})
	return out.Size, e
}

type Object struct {
	Key      string
	Size     int64
	Modified time.Time
}
type Janitor interface {
	List(context.Context, string, func(Object) error) error
	Delete(context.Context, string) error
}

func (s *Local) List(ctx context.Context, prefix string, visit func(Object) error) error {
	if !valid(strings.TrimSuffix(prefix, "/")) {
		return errors.New("invalid prefix")
	}
	base := filepath.Join(s.Root, prefix)
	if _, e := os.Stat(base); os.IsNotExist(e) {
		return nil
	}
	return filepath.WalkDir(base, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		stat, e := d.Info()
		if e != nil {
			return e
		}
		key, e := filepath.Rel(s.Root, path)
		if e != nil {
			return e
		}
		return visit(Object{Key: filepath.ToSlash(key), Size: stat.Size(), Modified: stat.ModTime()})
	})
}
func (s *Local) Delete(ctx context.Context, key string) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	path, e := s.path(key)
	if e != nil {
		return e
	}
	return os.Remove(path)
}
func (s *S3) List(ctx context.Context, prefix string, visit func(Object) error) error {
	full, e := s.key(strings.TrimSuffix(prefix, "/"))
	if e != nil {
		return e
	}
	for object := range s.Client.ListObjects(ctx, s.Bucket, minio.ListObjectsOptions{Prefix: full + "/", Recursive: true}) {
		if object.Err != nil {
			return object.Err
		}
		key := strings.TrimPrefix(object.Key, strings.Trim(s.Prefix, "/")+"/")
		if e = visit(Object{Key: key, Size: object.Size, Modified: object.LastModified}); e != nil {
			return e
		}
	}
	return ctx.Err()
}
func (s *S3) Delete(ctx context.Context, key string) error {
	k, e := s.key(key)
	if e != nil {
		return e
	}
	return s.Client.RemoveObject(ctx, s.Bucket, k, minio.RemoveObjectOptions{})
}
