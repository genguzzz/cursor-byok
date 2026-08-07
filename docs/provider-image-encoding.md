# Provider 识图编码方案（CodeBuddy / tclaude Anthropic）

## Goal

- 本地模式粘贴图、Read 图，在 **CodeBuddy（OpenAI 兼容）** 与 **tclaude（Anthropic Messages）** 上都能被上游多模态正确看见像素内容。
- 出站编码与 **tclaude CLI 成功流量**对齐，避免模型只读到尺寸文案或 `[Unsupported Image]`。
- 不改 tclaude 源码、不改 Proxyman Local Map；修复只落在本仓库 adapter / prompt。

## 问题现象

| 症状 | 典型表述 |
|------|----------|
| 模型「看不见图」 | 只能确认 WxH 尺寸；无法描述画面/文字 |
| 网关拒图 | 思考或文案出现 `[Unsupported Image]` |
| 粘贴失败、Read 偶发成功 | CodeBuddy 粘贴被滤；Anthropic 结构/字节不对 |

根因往往不是「没发图」，而是：**发了图但上游不认**（MIME/字节不一致、非 JFIF JPEG、结构与 CLI 不符、或模型本身弱）。

## 对照证据（Proxyman）

对照窗口示例：`41512–41621`（CLI 成功）、`41488/41494`（本助手失败）。

| 项 | CLI 成功（如 41621 / 41570） | 本助手失败（如 41494） |
|----|------------------------------|------------------------|
| Client | `claude.exe`（tclaude） | Cursor Local Assistant |
| Model | `claude-deepseek-v4-pro` | `claude-hy3` |
| JPEG magic | `ffd8ffe0` + **JFIF APP0** | `ffd8ffdb`（Go `jpeg.Encode` 默认，无 JFIF） |
| Read 结构 | `tool_result.content[]` 内嵌 `image` + trailing 尺寸文案 | 曾出现 MIME≠字节、无 JFIF、或顶层/嵌套混用 |
| Paste 结构 | 顶层 `image` + `[Image: source: path]` | 一度 strip 成仅 path（与 CLI 粘贴不一致） |
| PNG | CLI 明确：PNG → Unsupported，转 JPEG 后成功 | 曾把 PNG 字节标成 `image/jpeg` |

CLI 成功 Read 形态（摘要）：

```json
{
  "type": "tool_result",
  "tool_use_id": "...",
  "content": [{
    "type": "image",
    "source": {
      "type": "base64",
      "data": "<JFIF-JPEG>",
      "media_type": "image/jpeg"
    }
  }]
}
```

同条 user 消息随后：

```text
[Image: original 2560x1600, displayed at 2000x1250. Multiply coordinates by 1.28 to map to original image.]
```

CLI 成功 Paste 形态（摘要，flow 41570）：

```text
[Image #N] …
{ type: image, source: { type, media_type, data } }
[Image: source: /path/to/file.jpg]
```

## 方案分叉

### A. CodeBuddy（OpenAI 兼容）

**问题**：user 角色 `image_url` 常被中转过滤。

**策略**：

1. Strip user inline 图，只保留本地 path（`<selected_images>` + Read 提示）。
2. 模型调 `Read` → tool 结果走已验证的 `image_url` data URL 路径。

**入口**：`rewriteUserInlineImagesToPathFallback`（`codebuddy.go`）。

### B. tclaude / Anthropic Messages

**策略**：对齐 CLI，而不是 CodeBuddy 的 strip 逻辑。

1. **字节**：一律真实 JPEG；`media_type` 与 magic 一致；插入 **JFIF APP0**（`ffd8ffe0…JFIF`）。
2. **Read**：image **嵌在** `tool_result.content[]`，其后 trailing `[Image: original…displayed…]`；**禁止**把嵌套图 hoist 到 tool_result 同级。
3. **Paste**：保留 user 顶层 `image`，并追加 `[Image: source: path]`（对齐 41570）；不要对 Anthropic 套用 CodeBuddy strip。
4. **过小图**：最小边 &lt; 256 时放大，避免极端横条（如 714×82）只剩元数据；trailing 写清 original vs displayed。
5. **PNG**：上游不认 PNG 多模态时，出站必须重编码为 JFIF JPEG（与 CLI「先转 JPEG 再 Read」一致）。

**非目标**：

- 不修改 tclaude daemon / CLI 源码。
- 不依赖 Proxyman Local Map 改包。
- 不把「换模型」当成唯一修复；但 **hy3 与 deepseek-v4-pro 识图能力可能不同**，复测应以 CLI 同款模型为对照。

## 出站契约（Anthropic）

| 场景 | 必须满足 |
|------|----------|
| MIME | `media_type: image/jpeg` |
| 字节 | SOI + APP0 JFIF（`ffd8ffe0`），可 `image.Decode` |
| Read | nested in `tool_result.content`；可选 trailing 尺寸文案 |
| Paste | top-level `image` + `[Image: source: …]` |
| source 字段 | `type` / `data` / `media_type`（结构体固定顺序，贴近 CLI） |

错误示范（已踩坑）：

1. `media_type=image/jpeg` 但 payload 仍是 PNG → Unsupported。
2. 合法 JPEG 但无 JFIF（`ffd8ffdb`）→ 模型常只看到尺寸文案。
3. 把 nested image 提到 tool_result 同级顶层 → 与 CLI 不符。
4. Anthropic 误用 CodeBuddy strip → 粘贴路径与 CLI 41570 偏离。

## 实现映射

| 能力 | 位置 |
|------|------|
| CodeBuddy strip + path 提示 | `internal/backend/agent/model/codebuddy.go` |
| Anthropic 消息归一 / relocate / tool_result 嵌套 | `internal/backend/agent/model/anthropic.go` |
| JFIF JPEG 编码 / 最小边放大 | `encodeAnthropicVisionJPEG` / `ensureJPEGJFIFHeader` |
| ContentPart → Anthropic blocks + source 文案 | `internal/backend/agent/model/content_parts.go` |
| Read 压缩上限（OpenAI 路径等） | `internal/backend/agent/model/read_tool_image_compress.go` |
| 粘贴 path 进 replay | `internal/backend/agent/prompt/replay.go` |
| 单测 | `anthropic_tool_image_test.go` / `codebuddy_user_image_test.go` / `selected_image_replay_test.go` |

## 验证清单

1. **单元测试**：PNG→JFIF；nested tool_result；paste 顶层 + source note；过小图放大。
2. **安装**：`./dev.sh install` 后走菜单栏助手。
3. **Proxyman**：对 `/v1/messages` 抓包，确认：
   - magic 为 `ffd8ffe0`；
   - Read 为 nested；Paste 有顶层 image + source 文案；
   - 模型思考出现「能看见画面」而非仅尺寸。
4. **模型对照**：同一张图分别用 `claude-deepseek-v4-pro`（CLI 同款）与当前配置模型；若仅 hy3 失败，记为模型能力差异，而非编码回退。

## 排障顺序

1. 出站是否有 `type=image` / nested `tool_result` image？
2. `media_type` 与文件头是否一致？是否 JFIF？
3. 结构是否 hoist / 重复顶层+嵌套导致网关异常？
4. 响应里是否 `[Unsupported Image]` 或「只有尺寸」？
5. 换 CLI 同款模型是否立刻好转？

## 残留风险

- **模型差异**：CLI 实证成功多在 `claude-deepseek-v4-pro`；`claude-hy3` 即使 JFIF 仍可能弱识图，需产品侧选型或提示。
- **超大 PNG**：须压缩+转 JPEG；失败时应有明确错误而非静默尺寸文案。
- **relocateImages**：非 `api.anthropic.com` 会把顶层图挪到末条 user；需继续用抓包确认不破坏 nested Read。

## 变更记录（摘要）

| 日期 | 结论 |
|------|------|
| 2026-08-07 | CodeBuddy：strip user 图 → Read `image_url` |
| 2026-08-07 | Anthropic：nested tool_result + 真实 JPEG（修 PNG 冒充） |
| 2026-08-07 | 对比 41512–41621：补 **JFIF**；恢复 paste 顶层 + source 文案；过小图放大 |
| — | 未改 tclaude 源码 / Proxyman Local Map |
