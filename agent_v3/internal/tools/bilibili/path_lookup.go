package bilibili

import (
	"os"
	"path/filepath"
	"strings"
)

func lookupPathFile(name string) (string, bool) {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		for _, candidate := range []string{name, name + ".exe", name + ".cmd", name + ".bat"} {
			path := filepath.Join(dir, candidate)
			info, err := os.Stat(path)
			if err == nil && !info.IsDir() {
				return path, true
			}
		}
	}
	return "", false
}
