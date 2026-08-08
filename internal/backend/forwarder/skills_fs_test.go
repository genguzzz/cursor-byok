package forwarder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cursor/gen/agentv1"
)

func TestRequestContextNeedsFilesystemSkills(t *testing.T) {
	t.Parallel()

	if !requestContextNeedsFilesystemSkills(nil) {
		t.Fatal("nil request context should need enrich")
	}
	if !requestContextNeedsFilesystemSkills(&agentv1.RequestContext{}) {
		t.Fatal("empty request context should need enrich")
	}
	if requestContextNeedsFilesystemSkills(&agentv1.RequestContext{
		AgentSkills: []*agentv1.AgentSkill{{FullPath: "/tmp/SKILL.md", Description: "x"}},
	}) {
		t.Fatal("existing agent skills must not be overwritten")
	}
	if requestContextNeedsFilesystemSkills(&agentv1.RequestContext{
		Rules: []*agentv1.CursorRule{{FullPath: "/tmp/a.mdc", Content: "rule"}},
	}) {
		t.Fatal("existing rules must not be overwritten")
	}
	if requestContextNeedsFilesystemSkills(&agentv1.RequestContext{
		SkillOptions: &agentv1.SkillOptions{
			SkillDescriptors: []*agentv1.SkillDescriptor{{
				Name:           "x",
				Description:    "desc",
				ReadmeFilePath: "/tmp/SKILL.md",
			}},
		},
	}) {
		t.Fatal("existing skill descriptors must not be overwritten")
	}
}

func TestEnrichRequestContextFromFilesystemMatchesOfficialShape(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	workspace := t.TempDir()

	writeSkill := func(root, name, body string) string {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir skill: %v", err)
		}
		path := filepath.Join(dir, "SKILL.md")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write skill: %v", err)
		}
		return path
	}
	writeRule := func(root, name, body string) string {
		t.Helper()
		dir := root
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir rules: %v", err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write rule: %v", err)
		}
		return path
	}

	cursorSkillPath := writeSkill(filepath.Join(home, ".cursor", "skills-cursor"), "canvas", strings.Join([]string{
		"---",
		"name: canvas",
		"description: >-",
		"  A Cursor Canvas is a live React app.",
		"environments:",
		"  - local",
		"---",
		"# Canvas",
		"",
		"body",
	}, "\n"))
	claudeSkillPath := writeSkill(filepath.Join(home, ".claude", "skills"), "crypto", strings.Join([]string{
		"---",
		"name: crypto",
		"description: Crypto analyzer skill.",
		"---",
		"# Crypto",
	}, "\n"))
	// Same real content via .cursor/skills symlink-equivalent duplicate directory name different path —
	// use identical realpath by symlink to ensure dedupe.
	cursorSkillsDir := filepath.Join(home, ".cursor", "skills")
	if err := os.MkdirAll(filepath.Dir(cursorSkillsDir), 0o755); err != nil {
		t.Fatalf("mkdir cursor skills parent: %v", err)
	}
	if err := os.Symlink(filepath.Join(home, ".claude", "skills"), cursorSkillsDir); err != nil {
		t.Fatalf("symlink cursor skills: %v", err)
	}
	projectSkillPath := writeSkill(filepath.Join(workspace, ".agents", "skills"), "coding-guidance", strings.Join([]string{
		"---",
		"name: coding-guidance",
		"description: 本地模式实现指南",
		"---",
		"当用户在处理本地模式的时候，使用此指南",
	}, "\n"))
	rulePath := writeRule(filepath.Join(home, ".cursor", "rules"), "run-and-verify.mdc", strings.Join([]string{
		"---",
		"description: Run code yourself after writing or changing it.",
		"alwaysApply: true",
		"---",
		"",
		"# Run and Verify",
		"",
		"Do the work.",
	}, "\n"))

	// Temporarily override home via scanning helpers directly (enrich uses os.UserHomeDir).
	skills, skillRules := scanFilesystemAgentSkills(home, []string{workspace})
	rules := scanFilesystemCursorRules(home, []string{workspace})
	rules = append(rules, skillRules...)

	if len(skills) != 3 {
		t.Fatalf("expected 3 deduped skills, got %d", len(skills))
	}
	byPath := map[string]*agentv1.AgentSkill{}
	for _, skill := range skills {
		byPath[skill.GetFullPath()] = skill
	}
	for _, path := range []string{cursorSkillPath, claudeSkillPath, projectSkillPath} {
		abs, _ := filepath.Abs(path)
		skill := byPath[abs]
		if skill == nil {
			t.Fatalf("missing skill for %s (keys=%v)", abs, keysOf(byPath))
		}
		if strings.TrimSpace(skill.GetDescription()) == "" {
			t.Fatalf("skill %s missing description", abs)
		}
		if !strings.Contains(skill.GetContent(), "---\nname:") {
			t.Fatalf("skill content should keep frontmatter: %s", abs)
		}
	}
	canvas := byPath[mustAbs(cursorSkillPath)]
	if got := canvas.GetDescription(); got != "A Cursor Canvas is a live React app." {
		t.Fatalf("folded description mismatch: %q", got)
	}
	if len(canvas.GetEnvironments()) != 1 || canvas.GetEnvironments()[0] != "local" {
		t.Fatalf("environments mismatch: %#v", canvas.GetEnvironments())
	}

	var mdc *agentv1.CursorRule
	skillAsRuleCount := 0
	for _, rule := range rules {
		if strings.HasSuffix(rule.GetFullPath(), "run-and-verify.mdc") {
			mdc = rule
		}
		if strings.HasSuffix(rule.GetFullPath(), "SKILL.md") {
			skillAsRuleCount++
			if rule.GetType().GetAgentFetched() == nil {
				t.Fatalf("skill-as-rule should be agent_fetched: %s", rule.GetFullPath())
			}
			if !strings.HasPrefix(strings.TrimSpace(rule.GetContent()), "---") {
				t.Fatalf("skill-as-rule content should keep frontmatter")
			}
		}
	}
	if skillAsRuleCount != 3 {
		t.Fatalf("expected 3 skill-as-rules, got %d", skillAsRuleCount)
	}
	if mdc == nil {
		t.Fatal("missing mdc rule")
	}
	if mdc.GetType().GetGlobal() == nil {
		t.Fatalf("alwaysApply rule should be global, got %#v", mdc.GetType())
	}
	if strings.HasPrefix(strings.TrimSpace(mdc.GetContent()), "---") {
		t.Fatalf("mdc content should strip frontmatter, got %q", mdc.GetContent())
	}
	if !strings.Contains(mdc.GetContent(), "# Run and Verify") {
		t.Fatalf("mdc body missing: %q", mdc.GetContent())
	}
	_ = rulePath

	// GUI guard: enrich must be a no-op when skills already present.
	existing := &agentv1.RequestContext{
		AgentSkills: []*agentv1.AgentSkill{{FullPath: "/keep/SKILL.md", Description: "keep"}},
	}
	if got := enrichRequestContextFromFilesystem(existing); got != existing || len(got.GetAgentSkills()) != 1 || got.GetAgentSkills()[0].GetFullPath() != "/keep/SKILL.md" {
		t.Fatalf("enrich overwrote existing GUI skills")
	}
}

func TestNormalizeEnrichedRequestContextProducesSkillDescriptors(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	skillDir := filepath.Join(home, ".cursor", "skills-cursor", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: demo\ndescription: Demo skill.\n---\n# Demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ruleDir := filepath.Join(home, ".cursor", "rules")
	if err := os.MkdirAll(ruleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ruleDir, "always.mdc"), []byte("---\nalwaysApply: true\n---\n# Always\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills, skillRules := scanFilesystemAgentSkills(home, nil)
	rules := append(scanFilesystemCursorRules(home, nil), skillRules...)
	rc := &agentv1.RequestContext{
		AgentSkills: skills,
		Rules:       rules,
	}
	normalized := normalizeRequestContextForStorageMode(rc, true)
	if normalized == nil {
		t.Fatal("normalized nil")
	}
	if len(normalized.GetAgentSkills()) != 0 {
		t.Fatalf("storage should clear agent_skills, got %d", len(normalized.GetAgentSkills()))
	}
	descriptors := normalized.GetSkillOptions().GetSkillDescriptors()
	if len(descriptors) != 1 {
		t.Fatalf("expected 1 skill descriptor, got %d", len(descriptors))
	}
	if descriptors[0].GetDescription() != "Demo skill." {
		t.Fatalf("descriptor description=%q", descriptors[0].GetDescription())
	}
	if len(normalized.GetRules()) != 1 {
		t.Fatalf("expected only non-skill rules persisted, got %d", len(normalized.GetRules()))
	}
	if strings.HasSuffix(normalized.GetRules()[0].GetFullPath(), "SKILL.md") {
		t.Fatal("skill rules should be filtered from storage rules")
	}
}

func keysOf(m map[string]*agentv1.AgentSkill) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mustAbs(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}
