package service

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var importRe = regexp.MustCompile(`"([^"]+)"`)

type rule struct {
	prefix    string
	forbidden []*regexp.Regexp
}

func TestServiceImportBoundaries(t *testing.T) {
	rules := []rule{
		{prefix: "service/common/", forbidden: []*regexp.Regexp{
			regexp.MustCompile(`^github.com/Sea-Go/Sea-BreakTheWaves/service/(search|recommend|async)/`),
		}},
		{prefix: "service/search/", forbidden: []*regexp.Regexp{
			regexp.MustCompile(`^github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/`),
		}},
		{prefix: "service/recommend/", forbidden: []*regexp.Regexp{
			regexp.MustCompile(`^github.com/Sea-Go/Sea-BreakTheWaves/service/search/.*/internal/`),
			regexp.MustCompile(`^github.com/Sea-Go/Sea-BreakTheWaves/service/async/.*/internal/`),
		}},
		{prefix: "service/async/", forbidden: []*regexp.Regexp{
			regexp.MustCompile(`^github.com/Sea-Go/Sea-BreakTheWaves/service/search/`),
			regexp.MustCompile(`^github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/`),
		}},
	}
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		for _, r := range rules {
			if !strings.HasPrefix(path, r.prefix) {
				continue
			}
			f, openErr := os.Open(path)
			if openErr != nil {
				return openErr
			}
			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				match := importRe.FindStringSubmatch(line)
				if match == nil {
					continue
				}
				for _, forbidden := range r.forbidden {
					if forbidden.MatchString(match[1]) {
						t.Fatalf("%s imports forbidden boundary %q", path, match[1])
					}
				}
			}
			closeErr := scanner.Err()
			_ = f.Close()
			return closeErr
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
