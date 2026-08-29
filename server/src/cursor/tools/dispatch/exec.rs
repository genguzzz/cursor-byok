//! Direct Exec and dynamic MCP dispatch.

use serde_json::{Map, Value};

use crate::{cursor::proto::agent::v1 as pb, model::ToolCall, Result};

use super::{normalized, ToolStart};
use crate::cursor::tools::{
    codec, result,
    runtime::{CursorToolRuntime, ExecContext},
};

pub(super) async fn start(
    runtime: &CursorToolRuntime,
    call: &ToolCall,
    context: &ExecContext,
) -> Result<ToolStart> {
    let message = match normalized(&call.name).as_str() {
        "getmcptools" => {
            let id = runtime.reserve_exec(call, context).await?;
            codec::mcp_state_request(id, call)
        }
        "callmcptool" => {
            // Models routinely emit snake_case aliases or bury the identity
            // fields inside `arguments`.  Normalize first so a recoverable
            // naming slip does not become a run-level protocol error.
            let call = &normalize_mcp_call(call);
            let (Some(server), Some(tool)) = (
                identity(call, &["server", "server_identifier", "serverIdentifier"]),
                identity(call, &["toolName", "tool_name", "name"]),
            ) else {
                return Ok(ToolStart {
                    messages: Vec::new(),
                    completion: Some(result::mcp_failure(
                        call,
                        "CallMcpTool requires a non-empty server and toolName".into(),
                    )?),
                });
            };
            let Some((server, route)) = resolve_route(context, &server, &tool) else {
                return Ok(ToolStart {
                    messages: Vec::new(),
                    completion: Some(result::mcp_failure(
                        call,
                        mcp_route_hint(context, &server, &tool),
                    )?),
                });
            };
            let id = runtime.reserve_exec(call, context).await?;
            codec::mcp_meta_request(id, call, &server, route)?
        }
        _ => {
            let id = runtime.reserve_exec(call, context).await?;
            codec::request(id, call, context)?
        }
    };
    Ok(ToolStart {
        messages: vec![message],
        completion: None,
    })
}

/// Rewrite a `CallMcpTool` call so downstream codecs see canonical
/// `server` / `toolName` / `arguments` keys.
fn normalize_mcp_call(call: &ToolCall) -> ToolCall {
    let mut normalized = call.clone();
    let Some(arguments) = normalized.arguments.as_object_mut() else {
        return normalized;
    };
    lift_nested_identity(arguments);
    canonicalize(
        arguments,
        "server",
        &["server_identifier", "serverIdentifier"],
    );
    canonicalize(arguments, "toolName", &["tool_name"]);
    canonicalize(arguments, "arguments", &["tool_args", "toolArgs", "args"]);
    normalized
}

/// Move identity fields out of a nested `arguments` object when the model put
/// them there instead of alongside it.
fn lift_nested_identity(arguments: &mut Map<String, Value>) {
    const IDENTITY: &[&str] = &[
        "server",
        "server_identifier",
        "serverIdentifier",
        "toolName",
        "tool_name",
    ];
    let Some(nested) = arguments
        .get_mut("arguments")
        .and_then(Value::as_object_mut)
    else {
        return;
    };
    let lifted: Vec<(String, Value)> = IDENTITY
        .iter()
        .filter_map(|key| nested.remove(*key).map(|value| ((*key).to_string(), value)))
        .collect();
    for (key, value) in lifted {
        arguments.entry(key).or_insert(value);
    }
}

fn canonicalize(arguments: &mut Map<String, Value>, canonical: &str, aliases: &[&str]) {
    if arguments
        .get(canonical)
        .is_some_and(|value| !is_blank(value))
    {
        return;
    }
    for alias in aliases {
        if let Some(value) = arguments
            .get(*alias)
            .filter(|value| !is_blank(value))
            .cloned()
        {
            arguments.insert(canonical.to_string(), value);
            return;
        }
    }
}

fn is_blank(value: &Value) -> bool {
    match value {
        Value::Null => true,
        Value::String(value) => value.trim().is_empty(),
        _ => false,
    }
}

fn identity(call: &ToolCall, keys: &[&str]) -> Option<String> {
    keys.iter().find_map(|key| {
        call.arguments
            .get(*key)
            .and_then(Value::as_str)
            .map(str::trim)
            .filter(|value| !value.is_empty())
            .map(str::to_string)
    })
}

/// Cursor advertises MCP servers with a `user-` prefix that models often drop.
/// Return the identifier that actually matched so later codecs stay consistent.
fn resolve_route<'a>(
    context: &'a ExecContext,
    server: &str,
    tool: &str,
) -> Option<(String, &'a crate::cursor::tools::runtime::McpRoute)> {
    let candidates = [server.to_string(), format!("user-{server}")];
    candidates.into_iter().find_map(|candidate| {
        context
            .mcp_routes
            .get(&(candidate.clone(), tool.to_string()))
            .map(|route| (candidate, route))
    })
}

/// Listing what is actually reachable lets the model correct itself instead of
/// guessing at another name.
fn mcp_route_hint(context: &ExecContext, server: &str, tool: &str) -> String {
    let mut message = format!("MCP descriptor not found for {server}/{tool}");
    let mut servers: Vec<&str> = context
        .mcp_routes
        .keys()
        .map(|(server, _)| server.as_str())
        .collect();
    servers.sort_unstable();
    servers.dedup();
    if !servers.is_empty() {
        message.push_str("\navailable servers: ");
        message.push_str(&servers.join(", "));
    }
    let mut tools: Vec<&str> = context
        .mcp_routes
        .keys()
        .filter(|(candidate, _)| candidate == server || candidate == &format!("user-{server}"))
        .map(|(_, tool)| tool.as_str())
        .collect();
    tools.sort_unstable();
    if !tools.is_empty() {
        message.push_str(&format!("\ntools on {server}: "));
        message.push_str(&tools.join(", "));
    }
    message
}

pub(super) async fn start_dynamic(
    runtime: &CursorToolRuntime,
    call: &ToolCall,
    definition: &pb::McpToolDefinition,
    context: &ExecContext,
) -> Result<ToolStart> {
    let id = runtime
        .reserve_dynamic_mcp(call, context, definition)
        .await?;
    Ok(ToolStart {
        messages: vec![codec::mcp_request(id, call, definition)?],
        completion: None,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn call(arguments: Value) -> ToolCall {
        ToolCall {
            index: 0,
            call_id: "call".into(),
            model_call_id: "model-call".into(),
            name: "CallMcpTool".into(),
            arguments_text: arguments.to_string(),
            arguments,
        }
    }

    #[test]
    fn snake_case_identity_is_canonicalized() {
        let normalized = normalize_mcp_call(&call(serde_json::json!({
            "server_identifier": "user-proxyman",
            "tool_name": "get_flows",
            "tool_args": { "limit": 10 },
        })));
        assert_eq!(
            identity(&normalized, &["server"]).as_deref(),
            Some("user-proxyman")
        );
        assert_eq!(
            identity(&normalized, &["toolName"]).as_deref(),
            Some("get_flows")
        );
        assert_eq!(
            normalized.arguments.get("arguments"),
            Some(&serde_json::json!({ "limit": 10 }))
        );
    }

    #[test]
    fn nested_identity_is_lifted_out_of_arguments() {
        let normalized = normalize_mcp_call(&call(serde_json::json!({
            "arguments": {
                "server": "user-rss",
                "toolName": "list_feeds",
                "limit": 5,
            },
        })));
        assert_eq!(
            identity(&normalized, &["server"]).as_deref(),
            Some("user-rss")
        );
        assert_eq!(
            identity(&normalized, &["toolName"]).as_deref(),
            Some("list_feeds")
        );
        let arguments = normalized.arguments.get("arguments").unwrap();
        assert_eq!(arguments.get("limit"), Some(&serde_json::json!(5)));
        assert!(arguments.get("server").is_none());
    }

    #[test]
    fn canonical_keys_win_over_aliases() {
        let normalized = normalize_mcp_call(&call(serde_json::json!({
            "server": "chosen",
            "server_identifier": "ignored",
            "toolName": "",
            "tool_name": "fallback",
        })));
        assert_eq!(
            identity(&normalized, &["server"]).as_deref(),
            Some("chosen")
        );
        // A blank canonical value still defers to a usable alias.
        assert_eq!(
            identity(&normalized, &["toolName", "tool_name"]).as_deref(),
            Some("fallback")
        );
    }
}
