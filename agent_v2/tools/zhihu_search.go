package tools

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

	"agent_v2/config"
)

const (
	defaultZhihuSkillDir = "skills/zhihu-search"
	zhihuScriptPath      = "scripts/zhihu-search.py"
	defaultPythonCommand = "python"
	zhihuToolTimeout     = 15 * time.Second
)

type zhihuRuntime struct {
	cfg           config.ZhihuConfig
	skillDir      string
	pythonCommand string
	timeout       time.Duration
}

type ZhihuSearchInput struct {
	Query string `json:"query" jsonschema:"description=知乎搜索关键词，例如 AI Agent、如何理解 rave 文化"`
	Count int    `json:"count,omitempty" jsonschema:"description=返回数量，范围 1-10，默认 5"`
}

type ZhihuSearchResult struct {
	Code      int               `json:"code"`
	Message   string            `json:"message"`
	ItemCount int               `json:"item_count"`
	Items     []ZhihuSearchItem `json:"items"`
}

type ZhihuSearchItem struct {
	Title        string `json:"title"`
	URL          string `json:"url"`
	AuthorName   string `json:"author_name"`
	Summary      string `json:"summary"`
	VoteUpCount  int64  `json:"vote_up_count"`
	CommentCount int64  `json:"comment_count"`
	EditTime     int64  `json:"edit_time"`
}

func newZhihuRuntime(cfg config.ZhihuConfig) *zhihuRuntime {
	return &zhihuRuntime{
		cfg:           cfg,
		skillDir:      defaultZhihuSkillDir,
		pythonCommand: defaultPythonCommand,
		timeout:       zhihuToolTimeout,
	}
}

func (r *zhihuRuntime) Search(ctx context.Context, in ZhihuSearchInput) (ZhihuSearchResult, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return ZhihuSearchResult{}, errors.New("query cannot be empty")
	}
	count := clampZhihuSearchCount(in.Count)

	skillDir, err := filepath.Abs(r.skillDir)
	if err != nil {
		return ZhihuSearchResult{}, fmt.Errorf("resolve zhihu skill dir: %w", err)
	}
	if _, err := os.Stat(filepath.Join(skillDir, zhihuScriptPath)); err != nil {
		return ZhihuSearchResult{}, fmt.Errorf("zhihu skill script not found: %w", err)
	}

	timeout := r.timeout
	if timeout <= 0 {
		timeout = zhihuToolTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(
		ctx,
		r.resolvePythonCommand(),
		zhihuScriptPath,
		"--query",
		query,
		"--count",
		strconv.Itoa(count),
	)
	cmd.Dir = skillDir
	cmd.Env = r.zhihuScriptEnv()

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
			return ZhihuSearchResult{}, fmt.Errorf("run zhihu skill script: %w: %s", err, detail)
		}
		return ZhihuSearchResult{}, fmt.Errorf("run zhihu skill script: %w", err)
	}

	var result ZhihuSearchResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &result); err != nil {
		return ZhihuSearchResult{}, fmt.Errorf("decode zhihu skill output: %w", err)
	}
	return result, nil
}

func (r *zhihuRuntime) zhihuScriptEnv() []string {
	env := os.Environ()
	if secret := strings.TrimSpace(r.cfg.AccessSecret); secret != "" {
		env = append(env, "ZHIHU_ACCESS_SECRET="+secret)
	}
	if baseURL := strings.TrimSpace(r.cfg.OpenAPIBaseURL); baseURL != "" {
		env = append(env, "ZHIHU_OPENAPI_BASE_URL="+baseURL)
	}
	if searchURL := strings.TrimSpace(r.cfg.ZhihuSearchURL); searchURL != "" {
		env = append(env, "ZHIHU_ZHIHU_SEARCH_URL="+searchURL)
	}
	return env
}

func clampZhihuSearchCount(count int) int {
	if count <= 0 {
		return 5
	}
	if count > 10 {
		return 10
	}
	return count
}

func (r *zhihuRuntime) resolvePythonCommand() string {
	command := strings.TrimSpace(r.pythonCommand)
	if command == "" {
		command = defaultPythonCommand
	}
	if resolved, err := exec.LookPath(command); err == nil {
		return resolved
	}
	if command == defaultPythonCommand {
		if resolved, err := exec.LookPath("python3"); err == nil {
			return resolved
		}
	}
	return command
}
