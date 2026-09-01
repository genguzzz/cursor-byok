//! Builds Cursor usage and context breakdown data.
//!
//! The category attribution mirrors the official `agent-exec` metering strategy
//! (see the legacy-go `align context metering` change): each category is token
//! estimated with `(rune_count + 3) / 4 + newlines` plus per-message and
//! per-tool-call overhead, and the reported total is the maximum of the
//! provider-reported usage and the sum of the category estimates.
use std::collections::HashSet;
use std::sync::OnceLock;

use regex::Regex;

use crate::{
    cursor::protocol::proto::agent::v1 as pb,
    model::{CanonicalMessage, ContentPart, MessageContent, Role, ToolCallContent, ToolDefinition},
    Result,
};

const CATEGORIES: [(&str, &str); 8] = [
    ("system_prompt", "System prompt"),
    ("tools", "Tool definitions"),
    ("rules", "Rules"),
    ("skills", "Skills"),
    ("mcp", "MCP & dynamic tools"),
    ("subagents", "Subagent definitions"),
    ("summarized_conversation", "Summarized conversation"),
    ("conversation", "Conversation"),
];

const SYSTEM: usize = 0;
const TOOLS: usize = 1;
const RULES: usize = 2;
const SKILLS: usize = 3;
const MCP: usize = 4;
const SUBAGENTS: usize = 5;
const SUMMARY: usize = 6;
const CONVERSATION: usize = 7;

const MESSAGE_OVERHEAD_TOKENS: u64 = 8;
const TOOL_CALL_OVERHEAD_TOKENS: u64 = 6;
const IMAGE_PART_TOKENS: u64 = 1024;

/// Marker that separates the built-in subagent list inside the `Task` tool
/// descriptor from the rest of the tool definition.
const TASK_SUBAGENTS_MARKER: &str = "Available subagent_type";

fn rules_section() -> &'static Regex {
    static PATTERN: OnceLock<Regex> = OnceLock::new();
    PATTERN.get_or_init(|| Regex::new(r"(?s)<rules\b[^>]*>.*?</rules>").expect("rules section"))
}

fn skills_section() -> &'static Regex {
    static PATTERN: OnceLock<Regex> = OnceLock::new();
    PATTERN
        .get_or_init(|| Regex::new(r"(?s)<agent_skills\b[^>]*>.*?</agent_skills>").expect("skills section"))
}

fn mcp_section() -> &'static Regex {
    static PATTERN: OnceLock<Regex> = OnceLock::new();
    PATTERN.get_or_init(|| {
        Regex::new(r"(?s)<mcp_meta_tools\b[^>]*>.*?</mcp_meta_tools>").expect("mcp section")
    })
}

fn subagents_section() -> &'static Regex {
    static PATTERN: OnceLock<Regex> = OnceLock::new();
    PATTERN
        .get_or_init(|| Regex::new(r"(?s)<subagents\b[^>]*>.*?</subagents>").expect("subagents section"))
}

#[derive(Clone, Copy, Default)]
struct Measure {
    characters: u64,
    tokens: u64,
}

impl Measure {
    fn add(&mut self, text: &str) {
        self.characters += trimmed_characters(text);
        self.tokens += estimate_text_tokens(text);
    }

    fn add_message_overhead(&mut self, role: &str) {
        self.tokens += MESSAGE_OVERHEAD_TOKENS;
        self.add(role);
    }

    fn add_tool_call(&mut self, call: &ToolCallContent) -> Result<()> {
        self.tokens += TOOL_CALL_OVERHEAD_TOKENS;
        self.add(&call.call_id);
        self.add(&call.name);
        self.add(&serde_json::to_string(&call.arguments)?);
        Ok(())
    }
}

pub(crate) fn breakdown(
    used_tokens: u32,
    max_tokens: u32,
    baseline: Option<&pb::PromptTokenBreakdownSnapshot>,
    instructions: &str,
    tools: &[ToolDefinition],
    dynamic_tools: &HashSet<String>,
    messages: &[CanonicalMessage],
) -> Result<pb::PromptTokenBreakdownSnapshot> {
    let mut measures = [Measure::default(); 8];
    measures[SYSTEM].add(instructions);
    for tool in tools {
        let encoded = serde_json::to_string(tool)?;
        if dynamic_tools.contains(&tool.name) {
            measures[MCP].add(&encoded);
        } else if tool.name.eq_ignore_ascii_case("Task") {
            let (tools_part, subagents_part) = split_task_tool(&encoded);
            measures[TOOLS].add(&tools_part);
            measures[SUBAGENTS].add(&subagents_part);
        } else {
            measures[TOOLS].add(&encoded);
        }
    }
    for message in messages {
        measure_message(message, &mut measures)?;
    }

    let mut estimates = [0_u64; 8];
    for (index, measure) in measures.iter().enumerate() {
        estimates[index] = measure.tokens;
    }
    if measures[SUMMARY].characters == 0 {
        if let Some(prior) = baseline
            .and_then(|snapshot| {
                snapshot
                    .categories
                    .iter()
                    .find(|category| category.id == CATEGORIES[SUMMARY].0)
            })
            .filter(|category| category.estimated_tokens != 0)
        {
            measures[SUMMARY].characters = prior.character_count.unwrap_or(0) as u64;
            estimates[SUMMARY] = prior.estimated_tokens as u64;
        }
    }
    let category_total = estimates.iter().sum::<u64>();
    let total_used_tokens = (used_tokens as u64).max(category_total).min(u32::MAX as u64) as u32;

    let categories = CATEGORIES
        .iter()
        .enumerate()
        .map(|(index, (id, label))| pb::PromptTokenBreakdownCategory {
            id: (*id).into(),
            label: (*label).into(),
            estimated_tokens: estimates[index].min(u32::MAX as u64) as u32,
            character_count: (measures[index].characters != 0)
                .then_some(measures[index].characters.min(u32::MAX as u64) as u32),
        })
        .collect::<Vec<_>>();
    Ok(pb::PromptTokenBreakdownSnapshot {
        total_used_tokens,
        max_tokens,
        categories,
    })
}

fn measure_message(message: &CanonicalMessage, measures: &mut [Measure; 8]) -> Result<()> {
    match &message.content {
        MessageContent::Parts { parts } => {
            let mut texts = Vec::with_capacity(parts.len());
            let mut image_mimes = Vec::new();
            for part in parts {
                match part {
                    ContentPart::Text { text } => texts.push(text.as_str()),
                    ContentPart::Image { mime_type, .. } => image_mimes.push(mime_type.as_str()),
                }
            }
            let content = texts.join("\n");
            let target = classify_target(message, &content);
            let remaining = extract_section(&content, rules_section(), measures, RULES);
            let remaining = extract_section(&remaining, skills_section(), measures, SKILLS);
            let remaining = extract_section(&remaining, mcp_section(), measures, MCP);
            let remaining = extract_section(&remaining, subagents_section(), measures, SUBAGENTS);
            measures[target].add_message_overhead(role_str(&message.role));
            measures[target].add(&remaining);
            if !image_mimes.is_empty() {
                measures[target].tokens += image_mimes.len() as u64 * IMAGE_PART_TOKENS;
                for mime_type in image_mimes {
                    measures[target].add(mime_type);
                }
            }
        }
        MessageContent::Assistant {
            text,
            thinking,
            tool_calls,
            ..
        } => {
            measures[CONVERSATION].add_message_overhead(role_str(&message.role));
            measures[CONVERSATION].add(text);
            measures[CONVERSATION].add(thinking);
            for call in tool_calls {
                measures[CONVERSATION].add_tool_call(call)?;
            }
        }
        MessageContent::ToolResult(result) => {
            measures[CONVERSATION].add_message_overhead(role_str(&message.role));
            measures[CONVERSATION].add(&serde_json::to_string(result)?);
        }
    }
    Ok(())
}

fn classify_target(message: &CanonicalMessage, content: &str) -> usize {
    if content.contains("<conversation_summary>") || content.contains("</conversation_summary>") {
        return SUMMARY;
    }
    if message.role == Role::System {
        return SYSTEM;
    }
    CONVERSATION
}

fn role_str(role: &Role) -> &'static str {
    match role {
        Role::System => "system",
        Role::User => "user",
        Role::Assistant => "assistant",
        Role::Tool => "tool",
    }
}

fn extract_section(
    content: &str,
    pattern: &Regex,
    measures: &mut [Measure; 8],
    category: usize,
) -> String {
    if content.trim().is_empty() {
        return String::new();
    }
    let mut remaining = String::with_capacity(content.len());
    let mut cursor = 0;
    for matched in pattern.find_iter(content) {
        measures[category].add(&content[matched.start()..matched.end()]);
        remaining.push_str(&content[cursor..matched.start()]);
        remaining.push('\n');
        cursor = matched.end();
    }
    remaining.push_str(&content[cursor..]);
    remaining.trim().to_string()
}

fn split_task_tool(text: &str) -> (String, String) {
    match text.find(TASK_SUBAGENTS_MARKER) {
        Some(index) => {
            let tools_part = text[..index].trim().to_string();
            let subagents_part = text[index..].trim().to_string();
            // Keep the tools half structurally non-empty when the marker sits early.
            let tools_part = if tools_part.is_empty() {
                "{}".to_string()
            } else {
                tools_part
            };
            (tools_part, subagents_part)
        }
        None => (text.to_string(), String::new()),
    }
}

fn trimmed_characters(text: &str) -> u64 {
    text.trim().chars().count() as u64
}

fn estimate_text_tokens(text: &str) -> u64 {
    let trimmed = text.trim();
    let rune_count = trimmed.chars().count() as u64;
    if rune_count == 0 {
        return 0;
    }
    let newlines = trimmed.matches('\n').count() as u64;
    ((rune_count + 3) / 4 + newlines).max(1)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::Origin;

    fn category<'a>(snapshot: &'a pb::PromptTokenBreakdownSnapshot, id: &str) -> &'a pb::PromptTokenBreakdownCategory {
        snapshot
            .categories
            .iter()
            .find(|category| category.id == id)
            .unwrap()
    }

    fn tool(name: &str, description: &str) -> ToolDefinition {
        ToolDefinition {
            name: name.into(),
            description: description.into(),
            parameters: serde_json::json!({"type": "object", "properties": {}}),
        }
    }

    #[test]
    fn reports_exactly_eight_official_categories_without_the_stolen_token() {
        let snapshot = breakdown(100, 200_000, None, "be helpful", &[], &HashSet::new(), &[]).unwrap();
        let ids = snapshot
            .categories
            .iter()
            .map(|category| category.id.as_str())
            .collect::<Vec<_>>();
        assert_eq!(
            ids,
            [
                "system_prompt",
                "tools",
                "rules",
                "skills",
                "mcp",
                "subagents",
                "summarized_conversation",
                "conversation",
            ]
        );
        assert!(!ids.contains(&"leookun"));
    }

    #[test]
    fn total_is_at_least_the_sum_of_category_estimates() {
        let snapshot = breakdown(10, 200_000, None, "be helpful", &[], &HashSet::new(), &[]).unwrap();
        let sum = snapshot
            .categories
            .iter()
            .map(|category| category.estimated_tokens as u64)
            .sum::<u64>();
        assert!(snapshot.total_used_tokens as u64 >= sum);
        assert!(snapshot.total_used_tokens >= 10);
    }

    #[test]
    fn system_and_conversation_are_split_consistently() {
        let system = CanonicalMessage::text("s", Role::System, Origin::Prompt, "system body");
        let user = CanonicalMessage::text("u", Role::User, Origin::Runtime, "user body");
        let snapshot = breakdown(
            100,
            200_000,
            None,
            "be helpful",
            &[],
            &HashSet::new(),
            &[system, user],
        )
        .unwrap();
        let system_estimate = category(&snapshot, "system_prompt").estimated_tokens;
        let conversation_estimate = category(&snapshot, "conversation").estimated_tokens;
        assert!(system_estimate > 0, "system prompt estimate must be positive");
        assert!(conversation_estimate > 0, "conversation estimate must be positive");
    }

    #[test]
    fn runtime_request_context_sections_are_attributed_to_their_categories() {
        let context = CanonicalMessage::text(
            "ctx",
            Role::User,
            Origin::Runtime,
            "<rules>\nstay safe\n</rules>\n<agent_skills>\n<available_skills>\na skill\n</available_skills>\n</agent_skills>\nplain conversation",
        );
        let snapshot = breakdown(500, 200_000, None, "be helpful", &[], &HashSet::new(), &[context])
            .unwrap();
        assert!(category(&snapshot, "rules").estimated_tokens > 0);
        assert!(category(&snapshot, "skills").estimated_tokens > 0);
        assert!(category(&snapshot, "conversation").estimated_tokens > 0);
    }

    #[test]
    fn task_tool_splits_subagent_list_from_tool_definition() {
        let task = tool(
            "Task",
            "Launch a subagent.\n\nAvailable subagent_type values:\n- generalPurpose\n- explore",
        );
        let snapshot = breakdown(1000, 200_000, None, "", &[task], &HashSet::new(), &[]).unwrap();
        assert!(category(&snapshot, "tools").estimated_tokens > 0);
        assert!(category(&snapshot, "subagents").estimated_tokens > 0);
    }
}

