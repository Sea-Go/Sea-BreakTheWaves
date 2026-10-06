package datacenter

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDeploymentIdentityReadsRotatedCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity")
	for _, value := range []string{"first-token\n", "second-token\n"} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		actual, err := FileTokenSource(path)(context.Background())
		if err != nil || actual != value[:len(value)-1] {
			t.Fatalf("rotated DataCenter identity was not read: %q %v", actual, err)
		}
	}
}
