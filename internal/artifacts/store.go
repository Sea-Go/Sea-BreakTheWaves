// Package artifacts stores the content-addressed H03/H06 exchange objects.
// This is distinct from tRPC's session-scoped, integer-versioned artifacts:
// RTW and BTW exchange a fixed SHA256 key, without a session or latest version.
package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
)

const MaxBytes = 16 << 20

type Store interface {
	Put(context.Context, []byte) (corpus.Ref, error)
	Get(context.Context, corpus.Ref) ([]byte, error)
}

func Hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func ValidHash(hash string) bool {
	decoded, err := hex.DecodeString(hash)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == hash
}

func Reference(data []byte) corpus.Ref {
	h := Hash(data)
	return corpus.Ref{Key: "sha256/" + h, SHA256: h}
}

// Local is for isolated development and integration. The directory may be
// shared with a local RTW instance; no caller-supplied arbitrary paths are used.
type Local struct{ directory string }

func NewLocal(directory string) (*Local, error) {
	if directory == "" {
		return nil, errors.New("artifact directory required")
	}
	abs, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(abs, "sha256"), 0700); err != nil {
		return nil, err
	}
	return &Local{directory: abs}, nil
}

func (s *Local) Get(ctx context.Context, ref corpus.Ref) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ValidHash(ref.SHA256) || ref.Key != "sha256/"+ref.SHA256 {
		return nil, errors.New("invalid artifact reference")
	}
	f, err := os.Open(filepath.Join(s.directory, ref.Key))
	if err != nil {
		return nil, fmt.Errorf("open artifact: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read artifact: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(data) > MaxBytes || Hash(data) != ref.SHA256 {
		return nil, errors.New("artifact hash or size mismatch")
	}
	return data, nil
}

func (s *Local) Put(ctx context.Context, data []byte) (corpus.Ref, error) {
	if err := ctx.Err(); err != nil {
		return corpus.Ref{}, err
	}
	if len(data) > MaxBytes {
		return corpus.Ref{}, errors.New("artifact exceeds size limit")
	}
	ref := Reference(data)
	tmp, err := os.CreateTemp(filepath.Join(s.directory, "sha256"), ".pending-")
	if err != nil {
		return corpus.Ref{}, err
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(data)
	if writeErr == nil {
		writeErr = tmp.Sync()
	}
	closeErr := tmp.Close()
	if writeErr != nil {
		return corpus.Ref{}, writeErr
	}
	if closeErr != nil {
		return corpus.Ref{}, closeErr
	}
	if err := ctx.Err(); err != nil {
		return corpus.Ref{}, err
	}
	if err := os.Link(tmp.Name(), filepath.Join(s.directory, ref.Key)); err != nil && !errors.Is(err, os.ErrExist) {
		return corpus.Ref{}, err
	}
	if _, err := s.Get(ctx, ref); err != nil {
		return corpus.Ref{}, err
	}
	return ref, nil
}
