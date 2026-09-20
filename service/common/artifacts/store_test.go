package artifacts

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestContentAddressedPublicationAndDamage(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := Reference([]byte("immutable"))
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ref, err := s.Put(context.Background(), []byte("immutable"))
			if err != nil || ref != want {
				t.Errorf("put=%+v err=%v", ref, err)
			}
		}()
	}
	wg.Wait()
	if err := os.WriteFile(filepath.Join(dir, want.Key), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), want); err == nil {
		t.Fatal("accepted corrupt object")
	}
	if _, err := s.Put(context.Background(), []byte("immutable")); err == nil {
		t.Fatal("silently replaced immutable object")
	}
}
