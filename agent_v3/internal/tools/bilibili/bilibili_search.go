package bilibili

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"agent_v3/internal/config"
)

const (
	defaultBilibiliSkillDir      = "skills/bilibili-search"
	bilibiliScriptPath           = "scripts/bilibili-search.py"
	defaultBilibiliPythonCommand = "python"
	bilibiliToolTimeout          = 20 * time.Second
)

type bilibiliRuntime struct {
	cfg           config.BilibiliConfig
	skillDir      string
	pythonCommand string
	timeout       time.Duration
}

type BilibiliSearchInput struct {
	Query string `json:"query" jsonschema:"description=B 站搜索关键词，例如 成都旅游攻略、重庆美食"`
	Count int    `json:"count,omitempty" jsonschema:"description=返回数量，范围 1-20，默认 10"`
}

type BilibiliSearchResult struct {
	Code      int                  `json:"code"`
	Message   string               `json:"message"`
	ItemCount int                  `json:"item_count"`
	Items     []BilibiliSearchItem `json:"items"`
}

type BilibiliSearchItem struct {
	Title         string `json:"title"`
	URL           string `json:"url"`
	BVID          string `json:"bvid"`
	AuthorName    string `json:"author_name"`
	Summary       string `json:"summary"`
	ViewCount     int64  `json:"view_count"`
	DanmakuCount  int64  `json:"danmaku_count"`
	LikeCount     int64  `json:"like_count"`
	FavoriteCount int64  `json:"favorite_count"`
	PublishTime   int64  `json:"publish_time"`
	Duration      string `json:"duration"`
	CoverURL      string `json:"cover_url"`
}

func newBilibiliRuntime(cfg config.BilibiliConfig) *bilibiliRuntime {
	timeout := bilibiliToolTimeout
	if cfg.SearchTimeout > 0 {
		timeout = time.Duration(cfg.SearchTimeout) * time.Second
	}
	return &bilibiliRuntime{
		cfg:           cfg,
		skillDir:      defaultBilibiliSkillDir,
		pythonCommand: defaultBilibiliPythonCommand,
		timeout:       timeout,
	}
}

func (r *bilibiliRuntime) Search(ctx context.Context, in BilibiliSearchInput) (BilibiliSearchResult, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return BilibiliSearchResult{}, errors.New("query cannot be empty")
	}
	count := clampBilibiliSearchCount(in.Count)

	skillDir, err := filepath.Abs(r.skillDir)
	if err != nil {
		return BilibiliSearchResult{}, fmt.Errorf("resolve bilibili skill dir: %w", err)
	}
	if _, err := os.Stat(filepath.Join(skillDir, bilibiliScriptPath)); err != nil {
		return BilibiliSearchResult{}, fmt.Errorf("bilibili skill script not found: %w", err)
	}

	timeout := r.timeout
	if timeout <= 0 {
		timeout = bilibiliToolTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(
		ctx,
		r.resolvePythonCommand(),
		bilibiliScriptPath,
		"--query",
		query,
		"--count",
		strconv.Itoa(count),
	)
	cmd.Dir = skillDir
	cmd.Env = r.bilibiliScriptEnv()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail != "" {
			return BilibiliSearchResult{}, fmt.Errorf("run bilibili skill script: %w: %s", err, detail)
		}
		return BilibiliSearchResult{}, fmt.Errorf("run bilibili skill script: %w", err)
	}

	var result BilibiliSearchResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &result); err != nil {
		return BilibiliSearchResult{}, fmt.Errorf("decode bilibili skill output: %w", err)
	}
	return result, nil
}

func (r *bilibiliRuntime) bilibiliScriptEnv() []string {
	env := os.Environ()
	if cookie := strings.TrimSpace(r.cfg.Cookie); cookie != "" {
		env = append(env, "BILIBILI_COOKIE="+cookie)
	}
	return env
}

func clampBilibiliSearchCount(count int) int {
	if count <= 0 {
		return 10
	}
	if count > 20 {
		return 20
	}
	return count
}

func (r *bilibiliRuntime) resolvePythonCommand() string {
	command := strings.TrimSpace(r.pythonCommand)
	if command == "" {
		command = defaultBilibiliPythonCommand
	}
	if resolved, err := exec.LookPath(command); err == nil {
		if command == defaultBilibiliPythonCommand && !filepath.IsAbs(resolved) {
			if resolvedPython3, ok := lookupPathFile("python3"); ok {
				return resolvedPython3
			}
			if resolvedPython3, err := exec.LookPath("python3"); err == nil {
				return resolvedPython3
			}
		}
		return resolved
	}
	if command == defaultBilibiliPythonCommand {
		if resolved, ok := lookupPathFile("python3"); ok {
			return resolved
		}
		if resolved, err := exec.LookPath("python3"); err == nil {
			return resolved
		}
	}
	return command
}
