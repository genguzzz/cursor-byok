# Design

```text
cc-connect → Cursor CLI AgentRunRequest
  ├── request_context.skill_options
  ├── direct skill_options fallback
  └── when the CLI sends no skills or rules:
        local `~/.cursor/skills*`, `~/.claude/skills`, and workspace skills
        → compile_context
             → <agent_skills><available_skills>
                  → provider request
```

The model keeps using `AgentSkill` entries where supplied. `SkillDescriptor`
entries supplement them by their `readme_file_path`; a stable first-seen
set removes duplicates without changing the existing `AgentSkill` behavior.
