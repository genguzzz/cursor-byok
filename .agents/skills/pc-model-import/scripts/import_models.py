#!/usr/bin/env python3
"""Import models from an OpenAI-compatible endpoint into Cursor BYOK."""
from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

DEFAULT_GROUP = "PC"
DEFAULT_BASE_URL = "https://sub2.2006608.xyz/v1"
DEFAULT_CONTEXT_WINDOW_TOKENS = 200_000
DEFAULT_MAX_COMPLETION_TOKENS = 64_000
MODEL_INPUT_KEYS = (
    "sort_order",
    "display_name",
    "group_name",
    "type",
    "base_url",
    "use_full_url",
    "api_key",
    "tooltip_data",
    "model_id",
    "reasoning_effort",
    "openai_endpoint",
    "openai_extra_params_enabled",
    "openai_extra_params",
    "custom_headers_enabled",
    "custom_headers",
    "anthropic_extra_params_enabled",
    "anthropic_extra_params",
    "claude_code_compat",
    "context_window_tokens",
    "max_completion_tokens",
    "anthropic_max_tokens",
    "anthropic_thinking_effort",
    "thinking_budget_tokens",
)


def load_dotenv(path: Path) -> None:
    if not path.is_file():
        return
    for raw_line in path.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        value = value.strip().strip("\"'")
        if key and value and key not in os.environ:
            os.environ[key] = value


def request_json(
    url: str,
    method: str = "GET",
    payload: Any = None,
    api_key: str | None = None,
) -> Any:
    body = None if payload is None else json.dumps(payload).encode("utf-8")
    headers = {"Accept": "application/json"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    if api_key:
        headers["Authorization"] = f"Bearer {api_key}"
    request = Request(url, data=body, method=method, headers=headers)
    try:
        with urlopen(request, timeout=60) as response:
            return json.load(response)
    except HTTPError as error:
        detail = error.read(512).decode("utf-8", errors="replace")
        raise RuntimeError(f"HTTP {error.code} from {url}: {detail}") from error
    except URLError as error:
        raise RuntimeError(f"request failed for {url}: {error.reason}") from error


def model_list(value: Any) -> list[dict[str, Any]]:
    rows = value.get("data", value) if isinstance(value, dict) else value
    if not isinstance(rows, list):
        raise RuntimeError("the endpoint returned no model list")
    result = []
    for row in rows:
        if isinstance(row, str):
            result.append({"id": row, "display_name": row})
        elif isinstance(row, dict) and isinstance(row.get("id"), str) and row["id"].strip():
            result.append(row)
    if not result:
        raise RuntimeError("the endpoint returned an empty model list")
    return result


def local_model_list(value: Any) -> list[dict[str, Any]]:
    rows = value.get("data", value) if isinstance(value, dict) else value
    if not isinstance(rows, list):
        raise RuntimeError("the control API returned no model list")
    result = [
        row
        for row in rows
        if isinstance(row, dict)
        and isinstance(row.get("model_hash"), str)
        and isinstance(row.get("model_id"), str)
    ]
    return result


def pricing_for(model_id: str, pricing: dict[str, Any]) -> tuple[str, dict[str, float] | None]:
    models = pricing.get("models", {})
    aliases = pricing.get("aliases", {})
    canonical = aliases.get(model_id, model_id)
    entry = models.get(canonical)
    if not isinstance(entry, dict):
        return canonical, None
    try:
        rates = {"input": float(entry["input"]), "output": float(entry["output"])}
    except (KeyError, TypeError, ValueError) as error:
        raise RuntimeError(f"invalid pricing entry for {canonical}") from error
    if rates["input"] < 0 or rates["output"] < 0:
        raise RuntimeError(f"pricing entry for {canonical} cannot be negative")
    return canonical, rates


def multiplier(model_id: str, pricing: dict[str, Any]) -> tuple[str, float | None, dict[str, float] | None]:
    baseline_id = pricing.get("baseline_model")
    if not isinstance(baseline_id, str):
        raise RuntimeError("pricing.baseline_model is missing")
    _, baseline = pricing_for(baseline_id, pricing)
    if not baseline:
        raise RuntimeError(f"baseline model {baseline_id} has no price")
    canonical, current = pricing_for(model_id, pricing)
    if not current:
        return canonical, None, None
    base_total = baseline["input"] + baseline["output"]
    if base_total <= 0:
        raise RuntimeError("baseline model total price must be greater than zero")
    return canonical, (current["input"] + current["output"]) / base_total, current


def official_limits_for(model_id: str, pricing: dict[str, Any]) -> tuple[int, int] | None:
    canonical, _ = pricing_for(model_id, pricing)
    entry = pricing.get("models", {}).get(canonical)
    if not isinstance(entry, dict):
        return None
    try:
        context_window_tokens = int(entry["context_window_tokens"])
        max_completion_tokens = int(entry["max_completion_tokens"])
    except (KeyError, TypeError, ValueError) as error:
        raise RuntimeError(f"invalid model limits for {canonical}") from error
    if context_window_tokens <= 0 or max_completion_tokens <= 0:
        raise RuntimeError(f"model limits for {canonical} must be greater than zero")
    return context_window_tokens, max_completion_tokens


def format_token_count(tokens: int) -> str:
    if tokens >= 1_000_000:
        return f"{tokens / 1_000_000:.2f}".rstrip("0").rstrip(".") + "M"
    if tokens >= 1_000:
        return f"{tokens // 1_000}K"
    return str(tokens)


def model_tooltip(context_window_tokens: int, max_completion_tokens: int, has_official_limits: bool) -> str:
    if not has_official_limits:
        return "上下文与最大输出：未公开"
    return (
        f"上下文：{format_token_count(context_window_tokens)} · "
        f"最大输出：{format_token_count(max_completion_tokens)}"
    )


def label(
    model: dict[str, Any], pricing: dict[str, Any], group: str
) -> tuple[str, str, float | None, dict[str, float] | None]:
    model_id = model["id"].strip()
    canonical, factor, rates = multiplier(model_id, pricing)
    display = str(model.get("display_name") or model_id).strip()
    suffix = f"x{factor:.2f}" if factor is not None else "未公开定价"
    return f"[{group}] {display} ({suffix})", canonical, factor, rates


def model_input(
    model: dict[str, Any],
    display_name: str,
    base_url: str,
    api_key: str,
    group: str,
    order: int,
    context_window_tokens: int,
    max_completion_tokens: int,
    tooltip_data: str,
) -> dict[str, Any]:
    model_id = model["id"].strip()
    return {
        "sort_order": order,
        "display_name": display_name,
        "group_name": group,
        "type": "openai",
        "base_url": base_url,
        "use_full_url": False,
        "api_key": api_key,
        "tooltip_data": tooltip_data,
        "model_id": model_id,
        "reasoning_effort": "medium",
        "openai_endpoint": "/v1/responses",
        "openai_extra_params_enabled": False,
        "openai_extra_params": {},
        "custom_headers_enabled": False,
        "custom_headers": {},
        "anthropic_extra_params_enabled": False,
        "anthropic_extra_params": {},
        "claude_code_compat": False,
        "context_window_tokens": context_window_tokens,
        "max_completion_tokens": max_completion_tokens,
        "anthropic_max_tokens": None,
        "anthropic_thinking_effort": None,
        "thinking_budget_tokens": None,
    }


def same_identity(row: dict[str, Any], base_url: str, group: str, model_id: str) -> bool:
    return (
        row.get("group_name") == group
        and str(row.get("base_url", "")).rstrip("/") == base_url
        and row.get("model_id") == model_id
    )


def needs_update(existing: dict[str, Any], planned: dict[str, Any]) -> bool:
    """Compare every persisted setting, including the API key, without printing it."""
    return any(existing.get(key) != planned.get(key) for key in MODEL_INPUT_KEYS)


def format_price(factor: float | None, rates: dict[str, float] | None, baseline_total: float) -> str:
    if factor is None or rates is None:
        return "official price unavailable"
    total = rates["input"] + rates["output"]
    delta = (factor - 1) * 100
    sign = "+" if delta >= 0 else ""
    return (
        f"x{factor:.2f} ({sign}{delta:.1f}% vs Luna); "
        f"input ${rates['input']:.4g} / output ${rates['output']:.4g}; "
        f"1M+1M total ${total:.4g} vs Luna ${baseline_total:.4g}"
    )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default=DEFAULT_BASE_URL, help="OpenAI-compatible /v1 base URL")
    parser.add_argument("--control-url", required=True, help="Cursor BYOK management API root ending in /api")
    parser.add_argument("--pricing", default=str(Path(__file__).resolve().parents[1] / "pricing.json"))
    parser.add_argument("--group", default=DEFAULT_GROUP)
    parser.add_argument("--api-key-env", default="PC_API_KEY")
    parser.add_argument(
        "--context-window-tokens",
        type=int,
        default=None,
        help="override context metadata; otherwise use official model limits when available",
    )
    parser.add_argument(
        "--max-completion-tokens",
        type=int,
        default=None,
        help="override output metadata; otherwise use official model limits when available",
    )
    parser.add_argument("--apply", action="store_true", help="write changes; default is dry-run")
    args = parser.parse_args()

    if (args.context_window_tokens is not None and args.context_window_tokens <= 0) or (
        args.max_completion_tokens is not None and args.max_completion_tokens <= 0
    ):
        raise RuntimeError("token limits must be greater than zero")

    skill_dir = Path(__file__).resolve().parents[1]
    load_dotenv(skill_dir / ".env")
    api_key = os.environ.get(args.api_key_env, "").strip()
    if not api_key:
        raise RuntimeError(f"missing {args.api_key_env}; set it in {skill_dir / '.env'} or the environment")

    base_url = args.base_url.rstrip("/")
    control_url = args.control_url.rstrip("/")
    pricing = json.loads(Path(args.pricing).read_text(encoding="utf-8"))
    baseline_id = pricing.get("baseline_model")
    _, baseline_rates = pricing_for(baseline_id, pricing) if isinstance(baseline_id, str) else ("", None)
    if not baseline_rates:
        raise RuntimeError(f"baseline model {baseline_id} has no price")
    baseline_total = baseline_rates["input"] + baseline_rates["output"]

    remote = model_list(request_json(f"{base_url}/models", api_key=api_key))
    local = local_model_list(request_json(f"{control_url}/models"))
    next_order = max((int(row.get("sort_order", 0)) for row in local), default=0) + 1

    planned: list[tuple[dict[str, Any], dict[str, Any] | None, float | None, dict[str, float] | None]] = []
    unknown: list[str] = []
    for remote_model in remote:
        display_name, canonical, factor, rates = label(remote_model, pricing, args.group)
        if factor is None:
            unknown.append(f"{remote_model['id']} (canonical: {canonical})")
        existing = next(
            (row for row in local if same_identity(row, base_url, args.group, remote_model["id"].strip())),
            None,
        )
        order = int(existing.get("sort_order", next_order)) if existing else next_order
        if existing is None:
            next_order += 1
        official_limits = official_limits_for(remote_model["id"].strip(), pricing)
        context_window_tokens = args.context_window_tokens or (
            official_limits[0]
            if official_limits
            else int(
                existing.get("context_window_tokens", DEFAULT_CONTEXT_WINDOW_TOKENS)
                if existing
                else DEFAULT_CONTEXT_WINDOW_TOKENS
            )
        )
        max_completion_tokens = args.max_completion_tokens or (
            official_limits[1]
            if official_limits
            else int(
                existing.get("max_completion_tokens", DEFAULT_MAX_COMPLETION_TOKENS)
                if existing
                else DEFAULT_MAX_COMPLETION_TOKENS
            )
        )
        tooltip_data = model_tooltip(
            context_window_tokens,
            max_completion_tokens,
            official_limits is not None,
        )
        planned_input = model_input(
            remote_model,
            display_name,
            base_url,
            api_key,
            args.group,
            order,
            context_window_tokens,
            max_completion_tokens,
            tooltip_data,
        )
        planned.append((planned_input, existing, factor, rates))

    matching = sum(existing is not None for _, existing, _, _ in planned)
    print(f"remote models: {len(remote)}; matching local models: {matching}")
    for planned_input, existing, factor, rates in planned:
        state = "create" if existing is None else ("update" if needs_update(existing, planned_input) else "skip")
        print(f"{state:6} {planned_input['model_id']}: {format_price(factor, rates, baseline_total)}")
    if unknown:
        print("official pricing unavailable:")
        for item in unknown:
            print(f"  {item}")

    if not args.apply:
        print("dry-run only; add --apply to write changes")
        return 0

    updated = 0
    for planned_input, existing, _, _ in planned:
        if existing is None or not needs_update(existing, planned_input):
            continue
        request_json(
            f"{control_url}/models/{existing['model_hash']}",
            method="PUT",
            payload=planned_input,
        )
        updated += 1

    new_inputs = [planned_input for planned_input, existing, _, _ in planned if existing is None]
    if new_inputs:
        request_json(f"{control_url}/models", method="POST", payload={"models": new_inputs})

    refreshed = local_model_list(request_json(f"{control_url}/models"))
    ordered = [row for row in refreshed if row.get("group_name") == args.group] + [
        row for row in refreshed if row.get("group_name") != args.group
    ]
    request_json(
        f"{control_url}/models/order",
        method="PUT",
        payload={"model_hashes": [row["model_hash"] for row in ordered]},
    )
    group_count = sum(row.get("group_name") == args.group for row in ordered)
    print(f"applied: {len(new_inputs)} created, {updated} updated")
    print(f"{args.group} group moved to the front ({group_count} models)")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (RuntimeError, OSError, json.JSONDecodeError) as error:
        print(f"error: {error}", file=sys.stderr)
        raise SystemExit(1)
