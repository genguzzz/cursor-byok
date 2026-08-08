package forwarder

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"cursor/gen/agentv1"
	"cursor/internal/logger"
)

// enrichRequestContextFromFilesystem 在 CLI --endpoint 等未携带 skills/rules 的请求上，
// 按官方 Cursor 客户端相同的目录约定扫描本地文件系统并补齐 request_context。
//
// 安全约束：仅当 AgentSkills、Rules、SkillOptions.SkillDescriptors 全部为空时才扫描，
// 避免覆盖 Cursor GUI 已打包的 request_context。
func enrichRequestContextFromFilesystem(requestContext *agentv1.RequestContext) *agentv1.RequestContext {
	if !requestContextNeedsFilesystemSkills(requestContext) {
		return requestContext
	}

	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	workspaces := requestContextWorkspaceRoots(requestContext)
	skills, skillRules := scanFilesystemAgentSkills(home, workspaces)
	rules := scanFilesystemCursorRules(home, workspaces)
	rules = append(rules, skillRules...)
	if len(skills) == 0 && len(rules) == 0 {
		return requestContext
	}

	if requestContext == nil {
		requestContext = &agentv1.RequestContext{}
	}
	if len(skills) > 0 {
		requestContext.AgentSkills = skills
	}
	if len(rules) > 0 {
		requestContext.Rules = rules
	}
	logger.Infof("request_context filesystem enrich skills=%d rules=%d workspaces=%d", len(skills), len(rules), len(workspaces))
	return requestContext
}

func requestContextNeedsFilesystemSkills(requestContext *agentv1.RequestContext) bool {
	if requestContext == nil {
		return true
	}
	return len(requestContext.GetAgentSkills()) == 0 &&
		len(requestContext.GetRules()) == 0 &&
		len(requestContext.GetSkillOptions().GetSkillDescriptors()) == 0
}

func requestContextWorkspaceRoots(requestContext *agentv1.RequestContext) []string {
	if requestContext == nil || requestContext.GetEnv() == nil {
		return nil
	}
	env := requestContext.GetEnv()
	roots := make([]string, 0, len(env.GetWorkspacePaths())+2)
	seen := make(map[string]struct{})
	add := func(path string) {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			return
		}
		cleaned := filepath.Clean(trimmed)
		if _, ok := seen[cleaned]; ok {
			return
		}
		seen[cleaned] = struct{}{}
		roots = append(roots, cleaned)
	}
	for _, path := range env.GetWorkspacePaths() {
		add(path)
	}
	add(env.GetProjectFolder())
	add(env.GetProcessWorkingDirectory())
	return roots
}

func scanFilesystemAgentSkills(home string, workspaces []string) ([]*agentv1.AgentSkill, []*agentv1.CursorRule) {
	roots := filesystemSkillRoots(home, workspaces)
	type skillItem struct {
		skill *agentv1.AgentSkill
		rule  *agentv1.CursorRule
	}
	ordered := make([]skillItem, 0, 32)
	seenReal := make(map[string]struct{})

	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() {
				names = append(names, entry.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			skillPath := filepath.Join(root, name, "SKILL.md")
			realPath, err := filepath.EvalSymlinks(skillPath)
			if err != nil {
				realPath = skillPath
			}
			realKey := strings.ToLower(filepath.Clean(realPath))
			if _, ok := seenReal[realKey]; ok {
				continue
			}
			skill, rule, ok := loadFilesystemAgentSkill(skillPath)
			if !ok {
				continue
			}
			seenReal[realKey] = struct{}{}
			ordered = append(ordered, skillItem{skill: skill, rule: rule})
		}
	}

	skills := make([]*agentv1.AgentSkill, 0, len(ordered))
	rules := make([]*agentv1.CursorRule, 0, len(ordered))
	for _, item := range ordered {
		skills = append(skills, item.skill)
		if item.rule != nil {
			rules = append(rules, item.rule)
		}
	}
	return skills, rules
}

func scanFilesystemCursorRules(home string, workspaces []string) []*agentv1.CursorRule {
	roots := filesystemRuleRoots(home, workspaces)
	rules := make([]*agentv1.CursorRule, 0, 16)
	seenReal := make(map[string]struct{})
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			ext := strings.ToLower(filepath.Ext(name))
			if ext != ".mdc" && ext != ".md" {
				continue
			}
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			rulePath := filepath.Join(root, name)
			realPath, err := filepath.EvalSymlinks(rulePath)
			if err != nil {
				realPath = rulePath
			}
			realKey := strings.ToLower(filepath.Clean(realPath))
			if _, ok := seenReal[realKey]; ok {
				continue
			}
			rule, ok := loadFilesystemCursorRule(rulePath)
			if !ok {
				continue
			}
			seenReal[realKey] = struct{}{}
			rules = append(rules, rule)
		}
	}
	return rules
}

func filesystemSkillRoots(home string, workspaces []string) []string {
	roots := make([]string, 0, 8+len(workspaces)*3)
	home = strings.TrimSpace(home)
	if home != "" {
		// 顺序对齐官方 Cursor：skills-cursor → .claude/skills → .cursor/skills（后者做兼容，realpath 去重）。
		roots = append(roots,
			filepath.Join(home, ".cursor", "skills-cursor"),
			filepath.Join(home, ".claude", "skills"),
			filepath.Join(home, ".cursor", "skills"),
		)
	}
	for _, workspace := range workspaces {
		workspace = strings.TrimSpace(workspace)
		if workspace == "" {
			continue
		}
		roots = append(roots,
			filepath.Join(workspace, ".agents", "skills"),
			filepath.Join(workspace, ".claude", "skills"),
			filepath.Join(workspace, ".cursor", "skills"),
		)
	}
	return roots
}

func filesystemRuleRoots(home string, workspaces []string) []string {
	roots := make([]string, 0, 4+len(workspaces)*2)
	home = strings.TrimSpace(home)
	if home != "" {
		roots = append(roots,
			filepath.Join(home, ".cursor", "rules"),
			filepath.Join(home, ".claude", "rules"),
		)
	}
	for _, workspace := range workspaces {
		workspace = strings.TrimSpace(workspace)
		if workspace == "" {
			continue
		}
		roots = append(roots,
			filepath.Join(workspace, ".cursor", "rules"),
			filepath.Join(workspace, ".claude", "rules"),
		)
	}
	return roots
}

func loadFilesystemAgentSkill(path string) (*agentv1.AgentSkill, *agentv1.CursorRule, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, false
	}
	raw := string(data)
	frontmatter, body, hasFrontmatter := splitMarkdownFrontmatter(raw)
	meta := parseSkillFrontmatter(frontmatter)
	description := strings.TrimSpace(meta.Description)
	if description == "" && !hasFrontmatter {
		description = firstNonEmptySkillDescriptionFallback(body)
	}

	absPath := path
	if resolved, err := filepath.Abs(path); err == nil {
		absPath = resolved
	}

	skill := &agentv1.AgentSkill{
		FullPath:     absPath,
		Content:      raw,
		Description:  description,
		Environments: meta.Environments,
	}
	if description == "" && strings.TrimSpace(raw) == "" {
		parseError := "empty skill file"
		skill.ParseError = &parseError
	} else if description == "" {
		parseError := "missing skill description"
		skill.ParseError = &parseError
	}

	rule := &agentv1.CursorRule{
		FullPath: absPath,
		Content:  raw,
		Type: &agentv1.CursorRuleType{
			Type: &agentv1.CursorRuleType_AgentFetched{
				AgentFetched: &agentv1.CursorRuleTypeAgentFetched{
					Description: description,
				},
			},
		},
	}
	return skill, rule, true
}

func loadFilesystemCursorRule(path string) (*agentv1.CursorRule, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	raw := string(data)
	if strings.TrimSpace(raw) == "" {
		return nil, false
	}
	frontmatter, body, hasFrontmatter := splitMarkdownFrontmatter(raw)
	meta := parseRuleFrontmatter(frontmatter)
	content := raw
	if hasFrontmatter {
		content = strings.TrimSpace(body)
	}
	if content == "" {
		return nil, false
	}

	absPath := path
	if resolved, err := filepath.Abs(path); err == nil {
		absPath = resolved
	}

	rule := &agentv1.CursorRule{
		FullPath: absPath,
		Content:  content,
		Type:     cursorRuleTypeFromFrontmatter(meta),
	}
	return rule, true
}

type skillFrontmatterMeta struct {
	Description  string
	Environments []string
}

type ruleFrontmatterMeta struct {
	Description string
	AlwaysApply bool
	HasGlobs    bool
	Globs       []string
}

func parseSkillFrontmatter(frontmatter string) skillFrontmatterMeta {
	meta := skillFrontmatterMeta{}
	if strings.TrimSpace(frontmatter) == "" {
		return meta
	}
	var raw struct {
		Description  string   `yaml:"description"`
		Environments []string `yaml:"environments"`
	}
	if err := yaml.Unmarshal([]byte(frontmatter), &raw); err != nil {
		return meta
	}
	meta.Description = strings.TrimSpace(raw.Description)
	for _, env := range raw.Environments {
		if trimmed := strings.TrimSpace(env); trimmed != "" {
			meta.Environments = append(meta.Environments, trimmed)
		}
	}
	return meta
}

func parseRuleFrontmatter(frontmatter string) ruleFrontmatterMeta {
	meta := ruleFrontmatterMeta{}
	if strings.TrimSpace(frontmatter) == "" {
		return meta
	}
	var raw struct {
		Description string `yaml:"description"`
		AlwaysApply bool   `yaml:"alwaysApply"`
		Globs       any    `yaml:"globs"`
	}
	if err := yaml.Unmarshal([]byte(frontmatter), &raw); err != nil {
		return meta
	}
	meta.Description = strings.TrimSpace(raw.Description)
	meta.AlwaysApply = raw.AlwaysApply
	meta.Globs, meta.HasGlobs = normalizeFrontmatterGlobs(raw.Globs)
	return meta
}

func normalizeFrontmatterGlobs(value any) ([]string, bool) {
	switch typed := value.(type) {
	case nil:
		return nil, false
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil, false
		}
		return []string{trimmed}, true
	case []any:
		globs := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				continue
			}
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				globs = append(globs, trimmed)
			}
		}
		if len(globs) == 0 {
			return nil, false
		}
		return globs, true
	case []string:
		globs := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				globs = append(globs, trimmed)
			}
		}
		if len(globs) == 0 {
			return nil, false
		}
		return globs, true
	default:
		return nil, false
	}
}

func cursorRuleTypeFromFrontmatter(meta ruleFrontmatterMeta) *agentv1.CursorRuleType {
	switch {
	case meta.AlwaysApply:
		return &agentv1.CursorRuleType{Type: &agentv1.CursorRuleType_Global{Global: &agentv1.CursorRuleTypeGlobal{}}}
	case meta.HasGlobs:
		return &agentv1.CursorRuleType{
			Type: &agentv1.CursorRuleType_FileGlobbed{
				FileGlobbed: &agentv1.CursorRuleTypeFileGlobs{Globs: meta.Globs},
			},
		}
	case meta.Description != "":
		return &agentv1.CursorRuleType{
			Type: &agentv1.CursorRuleType_AgentFetched{
				AgentFetched: &agentv1.CursorRuleTypeAgentFetched{Description: meta.Description},
			},
		}
	default:
		return &agentv1.CursorRuleType{Type: &agentv1.CursorRuleType_Global{Global: &agentv1.CursorRuleTypeGlobal{}}}
	}
}

func splitMarkdownFrontmatter(raw string) (frontmatter string, body string, ok bool) {
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return "", text, false
	}
	rest := strings.TrimPrefix(text, "---\n")
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		if strings.HasSuffix(rest, "\n---") {
			return strings.TrimSuffix(rest, "\n---"), "", true
		}
		return "", text, false
	}
	return rest[:idx], rest[idx+len("\n---\n"):], true
}

func firstNonEmptySkillDescriptionFallback(body string) string {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		return trimmed
	}
	return ""
}
