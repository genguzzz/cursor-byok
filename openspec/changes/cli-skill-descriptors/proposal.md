# CLI Skill Descriptor Projection

## Problem

The Cursor CLI used by cc-connect places available skills in
`AgentRunRequest.skill_options` and `RequestContext.skill_options`. Rust only
rendered `RequestContext.agent_skills`, so models received no `<agent_skills>`
block for CLI-only skill catalogs.

## Change

Project deduplicated `SkillOptions.skill_descriptors` alongside `agent_skills`.
When the request context lacks descriptors, merge the direct
`AgentRunRequest.skill_options` field before compiling provider context. When
the CLI sends no skills or rules, scan the same local and workspace skill
roots used by the legacy backend to build `AgentSkill` entries.

## Validation

- Unit-test descriptor-only projection and deduplication.
- Run the Linux ARM64 server on tutu.
- Invoke the Cursor CLI with a PC model and require it to identify an
  available skill path without tool calls.
