package recommendationv2

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	CostLevelLow    = "low"
	CostLevelMedium = "medium"
	CostLevelHigh   = "high"

	PermissionRead  = "read"
	PermissionWrite = "write"
	PermissionLLM   = "llm"
)

type SkillDefinition struct {
	Name         string         `json:"name"`
	Category     string         `json:"category"`
	Description  string         `json:"description,omitempty"`
	InputSchema  map[string]any `json:"input_schema,omitempty"`
	OutputSchema map[string]any `json:"output_schema,omitempty"`
	CostLevel    string         `json:"cost_level"`
	Permissions  []string       `json:"permissions,omitempty"`
	Paths        []string       `json:"paths,omitempty"`
	Online       bool           `json:"online"`
	Write        bool           `json:"write"`
	Extension    map[string]any `json:"extension,omitempty"`
}

func (s SkillDefinition) AllowedPath(path string) bool {
	if len(s.Paths) == 0 || path == "" {
		return true
	}
	for _, p := range s.Paths {
		if p == path || p == "*" {
			return true
		}
	}
	return false
}

type SkillRegistry struct {
	mu     sync.RWMutex
	skills map[string]SkillDefinition
}

func NewSkillRegistry() *SkillRegistry {
	r := &SkillRegistry{skills: make(map[string]SkillDefinition)}
	for _, skill := range defaultSkillDefinitions() {
		_ = r.Upsert(skill)
	}
	return r
}

func (r *SkillRegistry) Register(skill SkillDefinition) error {
	if r == nil {
		return fmt.Errorf("recommendation v2: nil skill registry")
	}
	skill.Name = strings.TrimSpace(skill.Name)
	if skill.Name == "" {
		return fmt.Errorf("recommendation v2: skill name is required")
	}
	if skill.CostLevel == "" {
		skill.CostLevel = CostLevelLow
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.skills[skill.Name]; exists {
		return fmt.Errorf("recommendation v2: skill already registered: %s", skill.Name)
	}
	r.skills[skill.Name] = skill
	return nil
}

func (r *SkillRegistry) Upsert(skill SkillDefinition) error {
	if r == nil {
		return fmt.Errorf("recommendation v2: nil skill registry")
	}
	skill.Name = strings.TrimSpace(skill.Name)
	if skill.Name == "" {
		return fmt.Errorf("recommendation v2: skill name is required")
	}
	if skill.CostLevel == "" {
		skill.CostLevel = inferSkillCostLevel(skill)
	}
	if skill.Category == "" {
		skill.Category = inferSkillCategory(skill.Name)
	}
	if len(skill.Paths) == 0 {
		skill.Paths = defaultSkillPaths(skill)
	}
	if len(skill.Permissions) == 0 {
		skill.Permissions = defaultSkillPermissions(skill)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.skills[skill.Name] = skill
	return nil
}

func (r *SkillRegistry) Get(name string) (SkillDefinition, bool) {
	if r == nil {
		return SkillDefinition{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	skill, ok := r.skills[name]
	return skill, ok
}

func (r *SkillRegistry) List() []SkillDefinition {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]SkillDefinition, 0, len(r.skills))
	for _, skill := range r.skills {
		out = append(out, skill)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *SkillRegistry) LoadDirectory(root string) error {
	if r == nil {
		return fmt.Errorf("recommendation v2: nil skill registry")
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return fmt.Errorf("recommendation v2: skill directory is required")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), "SKILL.md")
		skill, err := LoadSkillDefinitionFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := r.Upsert(skill); err != nil {
			return err
		}
	}
	return nil
}

func LoadSkillDefinitionFile(path string) (SkillDefinition, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return SkillDefinition{}, err
	}
	meta, err := parseSkillFrontMatter(string(raw))
	if err != nil {
		return SkillDefinition{}, fmt.Errorf("%s: %w", path, err)
	}
	name := meta["name"]
	if name == "" {
		name = filepath.Base(filepath.Dir(path))
	}
	skill := SkillDefinition{
		Name:        name,
		Category:    meta["category"],
		Description: meta["description"],
		CostLevel:   meta["cost_level"],
		Online:      true,
		Extension: map[string]any{
			"source": path,
		},
	}
	if tools := parseStringList(meta["tools"]); len(tools) > 0 {
		skill.Extension["tools"] = tools
	}
	if inputs := parseSchemaList(meta["inputs"]); len(inputs) > 0 {
		skill.InputSchema = inputs
	}
	if outputs := parseSchemaList(meta["outputs"]); len(outputs) > 0 {
		skill.OutputSchema = outputs
	}
	skill.CostLevel = inferSkillCostLevel(skill)
	skill.Permissions = defaultSkillPermissions(skill)
	skill.Paths = defaultSkillPaths(skill)
	skill.Write = hasPermission(skill.Permissions, PermissionWrite)
	return skill, nil
}

func parseSkillFrontMatter(raw string) (map[string]string, error) {
	lines := strings.Split(raw, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, fmt.Errorf("missing front matter")
	}
	meta := make(map[string]string)
	var current string
	var list []string
	flush := func() {
		if current != "" {
			meta[current] = strings.Join(list, "\n")
		}
		current = ""
		list = nil
	}
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			flush()
			return meta, nil
		}
		if strings.HasPrefix(trimmed, "- ") {
			if current != "" {
				list = append(list, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
			}
			continue
		}
		if strings.HasPrefix(line, "  ") {
			if current != "" && len(list) > 0 {
				list[len(list)-1] += "\n" + strings.TrimSpace(line)
			}
			continue
		}
		if i := strings.Index(trimmed, ":"); i >= 0 {
			flush()
			key := strings.TrimSpace(trimmed[:i])
			value := strings.TrimSpace(trimmed[i+1:])
			if value == "" {
				current = key
				continue
			}
			meta[key] = strings.Trim(value, "\"'")
		}
	}
	return nil, fmt.Errorf("front matter is not closed")
}

func parseStringList(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, ":") {
			continue
		}
		out = append(out, strings.Trim(line, "\"'"))
	}
	return out
}

func parseSchemaList(raw string) map[string]any {
	schema := make(map[string]any)
	var current map[string]any
	commit := func() {
		if current == nil {
			return
		}
		if name, _ := current["name"].(string); name != "" {
			schema[name] = current
		}
		current = nil
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "name:") {
			commit()
			current = map[string]any{"name": strings.TrimSpace(strings.TrimPrefix(line, "name:"))}
			continue
		}
		if current == nil {
			continue
		}
		if i := strings.Index(line, ":"); i >= 0 {
			key := strings.TrimSpace(line[:i])
			value := strings.TrimSpace(line[i+1:])
			current[key] = strings.Trim(value, "\"'")
		}
	}
	commit()
	if len(schema) == 0 {
		return nil
	}
	return schema
}

func inferSkillCategory(name string) string {
	if i := strings.Index(name, "."); i > 0 {
		return name[:i]
	}
	return "custom"
}

func inferSkillCostLevel(skill SkillDefinition) string {
	if skill.CostLevel != "" {
		return skill.CostLevel
	}
	name := strings.ToLower(skill.Name)
	desc := strings.ToLower(skill.Description)
	switch {
	case strings.Contains(name, ".llm") || strings.Contains(desc, "llm") || strings.Contains(desc, "agent"):
		return CostLevelHigh
	case strings.HasPrefix(name, "rerank.") || strings.HasPrefix(name, "graph.") || strings.HasPrefix(name, "search."):
		return CostLevelMedium
	default:
		return CostLevelLow
	}
}

func defaultSkillPermissions(skill SkillDefinition) []string {
	if len(skill.Permissions) > 0 {
		return skill.Permissions
	}
	name := strings.ToLower(skill.Name)
	perms := []string{PermissionRead}
	if strings.HasPrefix(name, "event.") || strings.HasPrefix(name, "profile.update") || strings.Contains(name, "register") {
		perms = append(perms, PermissionWrite)
	}
	if skill.CostLevel == CostLevelHigh || strings.Contains(name, ".llm") || strings.HasPrefix(name, "agent.") || strings.HasPrefix(name, "explain.") {
		perms = append(perms, PermissionLLM)
	}
	return perms
}

func defaultSkillPaths(skill SkillDefinition) []string {
	if len(skill.Paths) > 0 {
		return skill.Paths
	}
	name := strings.ToLower(skill.Name)
	if skill.CostLevel == CostLevelHigh || strings.Contains(name, ".llm") {
		return []string{PathSlow, PathHybrid}
	}
	if strings.HasPrefix(name, "agent.") {
		return []string{PathSlow}
	}
	return []string{PathFast, PathSlow, PathHybrid}
}

type ToolPolicy interface {
	Allow(ctx context.Context, decision ToolPolicyDecision) error
}

type ToolPolicyDecision struct {
	TenantID string
	Channel  string
	Path     string
	Budget   float64
	Skill    SkillDefinition
}

type StaticToolPolicy struct {
	mu               sync.RWMutex
	disabledSkills   map[string]struct{}
	tenantAllowList  map[string]map[string]struct{}
	channelAllowList map[string]map[string]struct{}
	requireWriteAuth bool
}

func NewStaticToolPolicy() *StaticToolPolicy {
	return &StaticToolPolicy{
		disabledSkills:   make(map[string]struct{}),
		tenantAllowList:  make(map[string]map[string]struct{}),
		channelAllowList: make(map[string]map[string]struct{}),
		requireWriteAuth: true,
	}
}

func (p *StaticToolPolicy) DisableSkill(name string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.disabledSkills[name] = struct{}{}
}

func (p *StaticToolPolicy) AllowTenant(skillName, tenantID string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	addPolicyValue(p.tenantAllowList, skillName, tenantID)
}

func (p *StaticToolPolicy) AllowChannel(skillName, channel string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	addPolicyValue(p.channelAllowList, skillName, channel)
}

func (p *StaticToolPolicy) Allow(_ context.Context, decision ToolPolicyDecision) error {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	skill := decision.Skill
	if _, disabled := p.disabledSkills[skill.Name]; disabled {
		return fmt.Errorf("skill disabled by policy: %s", skill.Name)
	}
	if !skill.AllowedPath(decision.Path) {
		return fmt.Errorf("skill %s is not allowed on path %s", skill.Name, decision.Path)
	}
	if allowedTenants := p.tenantAllowList[skill.Name]; len(allowedTenants) > 0 {
		if _, ok := allowedTenants[decision.TenantID]; !ok {
			return fmt.Errorf("skill %s is not allowed for tenant %s", skill.Name, decision.TenantID)
		}
	}
	if allowedChannels := p.channelAllowList[skill.Name]; len(allowedChannels) > 0 {
		if _, ok := allowedChannels[decision.Channel]; !ok {
			return fmt.Errorf("skill %s is not allowed for channel %s", skill.Name, decision.Channel)
		}
	}
	if skill.CostLevel == CostLevelHigh && decision.Path == PathFast {
		return fmt.Errorf("high cost skill %s is not allowed on fast path", skill.Name)
	}
	if skill.CostLevel == CostLevelHigh && decision.Budget > 0 && decision.Budget < 0.005 {
		return fmt.Errorf("budget %.4f is too low for high cost skill %s", decision.Budget, skill.Name)
	}
	if p.requireWriteAuth && skill.Write && !hasPermission(skill.Permissions, PermissionWrite) {
		return fmt.Errorf("write skill %s requires explicit write permission", skill.Name)
	}
	return nil
}

func addPolicyValue(target map[string]map[string]struct{}, skillName, value string) {
	if target[skillName] == nil {
		target[skillName] = make(map[string]struct{})
	}
	target[skillName][value] = struct{}{}
}

func hasPermission(perms []string, want string) bool {
	for _, perm := range perms {
		if perm == want {
			return true
		}
	}
	return false
}

func defaultSkillDefinitions() []SkillDefinition {
	return []SkillDefinition{
		{Name: "profile.load", Category: "profile", CostLevel: CostLevelLow, Permissions: []string{PermissionRead}, Paths: []string{PathFast, PathSlow, PathHybrid}, Online: true},
		{Name: "route.path", Category: "graph", CostLevel: CostLevelLow, Permissions: []string{PermissionRead}, Paths: []string{PathFast, PathSlow, PathHybrid}, Online: true},
		{Name: "recall.hybrid", Category: "recall", CostLevel: CostLevelLow, Permissions: []string{PermissionRead}, Paths: []string{PathFast, PathSlow, PathHybrid}, Online: true},
		{Name: "rank.traditional", Category: "rank", CostLevel: CostLevelLow, Permissions: []string{PermissionRead}, Paths: []string{PathFast, PathSlow, PathHybrid}, Online: true},
		{Name: "agent.intent", Category: "agent", CostLevel: CostLevelMedium, Permissions: []string{PermissionLLM}, Paths: []string{PathSlow}, Online: true},
		{Name: "rerank.self", Category: "rerank", CostLevel: CostLevelMedium, Permissions: []string{PermissionRead}, Paths: []string{PathSlow, PathHybrid}, Online: true},
		{Name: "quality.score", Category: "quality", CostLevel: CostLevelLow, Permissions: []string{PermissionRead}, Paths: []string{PathFast, PathSlow, PathHybrid}, Online: true},
		{Name: "explain.recommend", Category: "explain", CostLevel: CostLevelMedium, Permissions: []string{PermissionLLM}, Paths: []string{PathFast, PathSlow, PathHybrid}, Online: true},
		{Name: "event.report", Category: "event", CostLevel: CostLevelLow, Permissions: []string{PermissionWrite}, Paths: []string{PathFast, PathSlow, PathHybrid}, Online: true, Write: true},
	}
}
