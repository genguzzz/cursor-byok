package forwarder

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	modeladapter "cursor/internal/backend/agent/model"
	promptengine "cursor/internal/backend/agent/prompt"
)

const (
	estimatedTokensPerMessageOverhead  = int64(8)
	estimatedTokensPerToolCallOverhead = int64(6)
	estimatedTokensPerImagePart        = int64(1024)
)

// Official Context Usage category ids/labels (captured from cloud conversation_state.token_details).
const (
	promptCategorySystemPromptID           = "system_prompt"
	promptCategoryToolsID                  = "tools"
	promptCategoryRulesID                  = "rules"
	promptCategorySkillsID                 = "skills"
	promptCategoryMCPID                    = "mcp"
	promptCategorySubagentsID              = "subagents"
	promptCategorySummarizedConversationID = "summarized_conversation"
	promptCategoryConversationID           = "conversation"

	promptCategorySystemPromptLabel           = "System prompt"
	promptCategoryToolsLabel                  = "Tool definitions"
	promptCategoryRulesLabel                  = "Rules"
	promptCategorySkillsLabel                 = "Skills"
	promptCategoryMCPLabel                    = "MCP & dynamic tools"
	promptCategorySubagentsLabel              = "Subagent definitions"
	promptCategorySummarizedConversationLabel = "Summarized conversation"
	promptCategoryConversationLabel           = "Conversation"
)

var (
	promptSectionRulesRE  = regexp.MustCompile(`(?s)<rules\b[^>]*>.*?</rules>`)
	promptSectionSkillsRE = regexp.MustCompile(`(?s)<agent_skills\b[^>]*>.*?</agent_skills>`)
	promptSectionMCPRE    = regexp.MustCompile(`(?s)<mcp_file_system\b[^>]*>.*?</mcp_file_system>`)
)

type promptCategoryAccumulator struct {
	tokens int64
	chars  int64
}

func (acc *promptCategoryAccumulator) addText(text string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}
	acc.chars += int64(utf8.RuneCountInString(trimmed))
	acc.tokens += estimateTextTokens(trimmed)
}

func (acc *promptCategoryAccumulator) addMessageOverhead(message modeladapter.Message) {
	acc.tokens += estimatedTokensPerMessageOverhead
	acc.addText(message.Role)
	acc.addText(message.ToolCallID)
	acc.addText(message.Name)
	acc.addText(message.ReasoningContent)
	acc.addText(message.ReasoningSignature)
	for _, toolCall := range message.ToolCalls {
		acc.tokens += estimatedTokensPerToolCallOverhead
		acc.addText(toolCall.ID)
		acc.addText(toolCall.Type)
		acc.addText(toolCall.Function.Name)
		acc.addText(toolCall.Function.Arguments)
	}
}

func estimateCompiledPromptTokens(compiled CompiledConversation) int64 {
	return estimateModelMessagesTokens(compiled.Messages) + estimateToolDescriptorsTokens(compiled.Tools)
}

func estimateModelMessagesTokens(messages []modeladapter.Message) int64 {
	total := int64(0)
	for _, item := range messages {
		total += estimateModelMessageTokens(item)
	}
	return total
}

func estimateModelMessageTokens(item modeladapter.Message) int64 {
	total := estimatedTokensPerMessageOverhead
	total += estimateTextTokens(item.Role)
	total += estimateTextTokens(item.Content)
	total += estimateModelContentPartsTokens(item.Content, item.ContentParts)
	total += estimateTextTokens(item.ReasoningContent)
	total += estimateTextTokens(item.ReasoningSignature)
	total += estimateTextTokens(item.ToolCallID)
	total += estimateTextTokens(item.Name)
	for _, toolCall := range item.ToolCalls {
		total += estimatedTokensPerToolCallOverhead
		total += estimateTextTokens(toolCall.ID)
		total += estimateTextTokens(toolCall.Type)
		total += estimateTextTokens(toolCall.Function.Name)
		total += estimateTextTokens(toolCall.Function.Arguments)
	}
	return total
}

func estimateToolDescriptorsTokens(tools []json.RawMessage) int64 {
	total := int64(0)
	for _, item := range tools {
		total += estimateTextTokens(string(item))
	}
	return total
}

func estimateContextItemTokens(item *aiserverv1.ContextItem) int64 {
	if item == nil {
		return 0
	}
	body, err := protojson.MarshalOptions{EmitUnpopulated: false}.Marshal(item)
	if err != nil {
		return estimateTextTokens(item.String())
	}
	return estimateTextTokens(string(body))
}

func estimateTextTokens(text string) int64 {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	runeCount := utf8.RuneCountInString(trimmed)
	if runeCount <= 0 {
		return 0
	}
	estimated := int64((runeCount + 3) / 4)
	estimated += int64(strings.Count(trimmed, "\n"))
	if estimated < 1 {
		return 1
	}
	return estimated
}

func estimateModelContentPartsTokens(content string, parts []modeladapter.ContentPart) int64 {
	if len(parts) == 0 {
		return 0
	}
	total := int64(0)
	countText := strings.TrimSpace(content) == ""
	for _, part := range parts {
		switch strings.TrimSpace(strings.ToLower(part.Type)) {
		case "", "text":
			if countText {
				total += estimateTextTokens(part.Text)
			}
		case "image":
			total += estimatedTokensPerImagePart
			if part.Image != nil {
				total += estimateTextTokens(part.Image.MIMEType)
				total += estimateTextTokens(part.Image.Path)
			}
		}
	}
	return total
}

func estimatePromptContentPartsTokens(content string, parts []promptengine.ContentPart) int64 {
	if len(parts) == 0 {
		return 0
	}
	total := int64(0)
	countText := strings.TrimSpace(content) == ""
	for _, part := range parts {
		switch strings.TrimSpace(strings.ToLower(part.Type)) {
		case "", "text":
			if countText {
				total += estimateTextTokens(part.Text)
			}
		case "image":
			total += estimatedTokensPerImagePart
			if part.Image != nil {
				total += estimateTextTokens(part.Image.MIMEType)
				total += estimateTextTokens(part.Image.Path)
			}
		}
	}
	return total
}

func estimateCheckpointPromptTokenBreakdown(compiled CompiledConversation, hasCompiled bool, usedTokens uint32, maxTokens uint32) *agentv1.PromptTokenBreakdownSnapshot {
	if maxTokens == 0 {
		return nil
	}
	categories := make([]*agentv1.PromptTokenBreakdownCategory, 0, 8)
	if hasCompiled {
		buckets := estimateOfficialPromptCategoryBuckets(compiled)
		categories = appendOfficialPromptTokenCategories(categories, buckets)
	} else if usedTokens > 0 {
		categories = appendOfficialPromptTokenCategories(categories, map[string]promptCategoryAccumulator{
			promptCategoryConversationID: {tokens: int64(usedTokens), chars: 0},
		})
	} else {
		categories = appendOfficialPromptTokenCategories(categories, nil)
	}
	categoryTotal := int64(0)
	for _, category := range categories {
		categoryTotal += int64(category.GetEstimatedTokens())
	}
	totalUsedTokens := usedTokens
	if categoryTotal > int64(totalUsedTokens) {
		totalUsedTokens = clampInt64ToUint32(categoryTotal)
	}
	return &agentv1.PromptTokenBreakdownSnapshot{
		TotalUsedTokens: totalUsedTokens,
		MaxTokens:       maxTokens,
		Categories:      categories,
	}
}

const promptContextUsageTreeSchemaVersion = uint32(1)

// buildPromptContextUsageTree builds a Context Explorer tree matching official
// agent-exec shape: category parents (id=category:<id>) plus tool/skill/rule children.
func buildPromptContextUsageTree(compiled CompiledConversation, hasCompiled bool, breakdown *agentv1.PromptTokenBreakdownSnapshot) *agentv1.PromptContextUsageTree {
	if breakdown == nil {
		return nil
	}
	nodes := make([]*agentv1.PromptContextNode, 0, 16+len(compiled.Tools))
	for _, category := range breakdown.GetCategories() {
		if category == nil {
			continue
		}
		nodes = append(nodes, &agentv1.PromptContextNode{
			Id:               "category:" + category.GetId(),
			Kind:             "category",
			Label:            category.GetLabel(),
			CategoryId:       category.GetId(),
			EstimatedTokens:  category.GetEstimatedTokens(),
			CharacterCount:   category.GetCharacterCount(),
			ContentAvailable: false,
		})
	}
	if hasCompiled {
		nodes = append(nodes, buildPromptContextToolNodes(compiled.Tools)...)
		nodes = append(nodes, buildPromptContextSectionChildNodes(compiled.Messages)...)
	}
	return &agentv1.PromptContextUsageTree{
		SchemaVersion: promptContextUsageTreeSchemaVersion,
		Nodes:         nodes,
	}
}

func buildPromptContextToolNodes(tools []json.RawMessage) []*agentv1.PromptContextNode {
	nodes := make([]*agentv1.PromptContextNode, 0, len(tools))
	for index, raw := range tools {
		text := string(raw)
		if strings.TrimSpace(text) == "" {
			continue
		}
		name := toolDescriptorName(raw)
		parentID := "category:" + promptCategoryToolsID
		kind := "tool_definition"
		categoryID := promptCategoryToolsID
		label := name
		if label == "" {
			label = "tool"
		}
		switch {
		case isMCPToolDescriptorName(name):
			parentID = "category:" + promptCategoryMCPID
			categoryID = promptCategoryMCPID
		case strings.EqualFold(name, "Task"):
			toolsPart, subagentsPart := splitTaskToolDescriptor(text)
			nodes = append(nodes, newPromptContextTextNode(
				fmt.Sprintf("tool:%d", index),
				parentID,
				kind,
				label,
				categoryID,
				toolsPart,
			))
			if strings.TrimSpace(subagentsPart) != "" {
				nodes = append(nodes, newPromptContextTextNode(
					fmt.Sprintf("tool:%d:subagents", index),
					"category:"+promptCategorySubagentsID,
					"subagent_description",
					"Available subagent_types",
					promptCategorySubagentsID,
					subagentsPart,
				))
			}
			continue
		}
		nodes = append(nodes, newPromptContextTextNode(
			fmt.Sprintf("tool:%d", index),
			parentID,
			kind,
			label,
			categoryID,
			text,
		))
	}
	return nodes
}

var (
	promptSkillItemRE = regexp.MustCompile(`(?s)<agent_skill\b[^>]*>.*?</agent_skill>`)
	// Match both singular/plural rule tags from Cursor request_context
	// (e.g. <user_rules>, <always_applied_workspace_rule>).
	promptRuleItemRE = regexp.MustCompile(`(?s)<[a-zA-Z0-9_-]*_rules?\b[^>]*>.*?</[a-zA-Z0-9_-]*_rules?>`)
)

func buildPromptContextSectionChildNodes(messages []modeladapter.Message) []*agentv1.PromptContextNode {
	nodes := make([]*agentv1.PromptContextNode, 0)
	skillIndex := 0
	ruleIndex := 0
	for _, message := range messages {
		content := message.Content
		if strings.TrimSpace(content) == "" {
			continue
		}
		for _, match := range promptSkillItemRE.FindAllString(content, -1) {
			label := "Skill"
			if path := readNestedXMLAttr(match, "fullPath"); path != "" {
				label = path
			}
			nodes = append(nodes, newPromptContextTextNode(
				fmt.Sprintf("skill:%d", skillIndex),
				"category:"+promptCategorySkillsID,
				"skill",
				label,
				promptCategorySkillsID,
				match,
			))
			skillIndex++
		}
		for _, match := range promptRuleItemRE.FindAllString(content, -1) {
			nodes = append(nodes, newPromptContextTextNode(
				fmt.Sprintf("rule:%d", ruleIndex),
				"category:"+promptCategoryRulesID,
				"rule",
				"Rule",
				promptCategoryRulesID,
				match,
			))
			ruleIndex++
		}
	}
	return nodes
}

func newPromptContextTextNode(id, parentID, kind, label, categoryID, text string) *agentv1.PromptContextNode {
	parent := parentID
	chars := clampInt64ToUint32(int64(utf8.RuneCountInString(strings.TrimSpace(text))))
	tokens := clampInt64ToUint32(estimateTextTokens(text))
	return &agentv1.PromptContextNode{
		Id:               id,
		ParentId:         &parent,
		Kind:             kind,
		Label:            label,
		CategoryId:       categoryID,
		EstimatedTokens:  tokens,
		CharacterCount:   chars,
		ContentAvailable: false,
	}
}

func readNestedXMLAttr(tag string, attr string) string {
	pattern := regexp.MustCompile(attr + `="([^"]*)"`)
	match := pattern.FindStringSubmatch(tag)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}

func estimateOfficialPromptCategoryBuckets(compiled CompiledConversation) map[string]promptCategoryAccumulator {
	buckets := map[string]promptCategoryAccumulator{
		promptCategorySystemPromptID:           {},
		promptCategoryToolsID:                  {},
		promptCategoryRulesID:                  {},
		promptCategorySkillsID:                 {},
		promptCategoryMCPID:                    {},
		promptCategorySubagentsID:              {},
		promptCategorySummarizedConversationID: {},
		promptCategoryConversationID:           {},
	}
	accumulateToolDescriptorCategories(compiled.Tools, buckets)
	for _, message := range compiled.Messages {
		accumulateMessagePromptCategories(message, buckets)
	}
	return buckets
}

func accumulateToolDescriptorCategories(tools []json.RawMessage, buckets map[string]promptCategoryAccumulator) {
	for _, raw := range tools {
		text := string(raw)
		if strings.TrimSpace(text) == "" {
			continue
		}
		name := toolDescriptorName(raw)
		switch {
		case isMCPToolDescriptorName(name):
			addBucketText(buckets, promptCategoryMCPID, text)
		case strings.EqualFold(name, "Task"):
			toolsPart, subagentsPart := splitTaskToolDescriptor(text)
			addBucketText(buckets, promptCategoryToolsID, toolsPart)
			addBucketText(buckets, promptCategorySubagentsID, subagentsPart)
		default:
			addBucketText(buckets, promptCategoryToolsID, text)
		}
	}
}

func accumulateMessagePromptCategories(message modeladapter.Message, buckets map[string]promptCategoryAccumulator) {
	role := strings.TrimSpace(message.Role)
	content := message.Content
	if strings.TrimSpace(content) == "" && len(message.ContentParts) > 0 {
		parts := make([]string, 0, len(message.ContentParts))
		for _, part := range message.ContentParts {
			if strings.TrimSpace(strings.ToLower(part.Type)) == "image" {
				continue
			}
			if text := strings.TrimSpace(part.Text); text != "" {
				parts = append(parts, text)
			}
		}
		content = strings.Join(parts, "\n")
	}

	remaining := content
	remaining = extractPromptSection(remaining, promptSectionRulesRE, buckets, promptCategoryRulesID)
	remaining = extractPromptSection(remaining, promptSectionSkillsRE, buckets, promptCategorySkillsID)
	remaining = extractPromptSection(remaining, promptSectionMCPRE, buckets, promptCategoryMCPID)

	target := promptCategoryConversationID
	switch {
	case isConversationSummaryText(remaining) || isConversationSummaryMessage(message):
		target = promptCategorySummarizedConversationID
	case role == "system":
		target = promptCategorySystemPromptID
	}
	addBucketText(buckets, target, remaining)

	overhead := buckets[target]
	overhead.addMessageOverhead(message)
	if len(message.ContentParts) > 0 {
		for _, part := range message.ContentParts {
			if strings.TrimSpace(strings.ToLower(part.Type)) != "image" {
				continue
			}
			overhead.tokens += estimatedTokensPerImagePart
			if part.Image != nil {
				overhead.addText(part.Image.MIMEType)
				overhead.addText(part.Image.Path)
			}
		}
	}
	buckets[target] = overhead
}

func extractPromptSection(content string, pattern *regexp.Regexp, buckets map[string]promptCategoryAccumulator, categoryID string) string {
	if strings.TrimSpace(content) == "" || pattern == nil {
		return content
	}
	matches := pattern.FindAllString(content, -1)
	for _, match := range matches {
		addBucketText(buckets, categoryID, match)
	}
	return strings.TrimSpace(pattern.ReplaceAllString(content, "\n"))
}

func addBucketText(buckets map[string]promptCategoryAccumulator, categoryID string, text string) {
	acc := buckets[categoryID]
	acc.addText(text)
	buckets[categoryID] = acc
}

func appendOfficialPromptTokenCategories(categories []*agentv1.PromptTokenBreakdownCategory, buckets map[string]promptCategoryAccumulator) []*agentv1.PromptTokenBreakdownCategory {
	order := []struct {
		id    string
		label string
	}{
		{promptCategorySystemPromptID, promptCategorySystemPromptLabel},
		{promptCategoryToolsID, promptCategoryToolsLabel},
		{promptCategoryRulesID, promptCategoryRulesLabel},
		{promptCategorySkillsID, promptCategorySkillsLabel},
		{promptCategoryMCPID, promptCategoryMCPLabel},
		{promptCategorySubagentsID, promptCategorySubagentsLabel},
		{promptCategorySummarizedConversationID, promptCategorySummarizedConversationLabel},
		{promptCategoryConversationID, promptCategoryConversationLabel},
	}
	for _, item := range order {
		acc := promptCategoryAccumulator{}
		if buckets != nil {
			acc = buckets[item.id]
		}
		categories = append(categories, newOfficialPromptTokenCategory(item.id, item.label, acc))
	}
	return categories
}

func newOfficialPromptTokenCategory(id string, label string, acc promptCategoryAccumulator) *agentv1.PromptTokenBreakdownCategory {
	category := &agentv1.PromptTokenBreakdownCategory{
		Id:              id,
		Label:           label,
		EstimatedTokens: clampInt64ToUint32(acc.tokens),
	}
	chars := clampInt64ToUint32(acc.chars)
	category.CharacterCount = &chars
	return category
}

func toolDescriptorName(raw json.RawMessage) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	if name := readNestedString(payload, "function", "name"); name != "" {
		return name
	}
	if name := readNestedString(payload, "name"); name != "" {
		return name
	}
	return ""
}

func readNestedString(payload map[string]any, keys ...string) string {
	current := any(payload)
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current, ok = object[key]
		if !ok {
			return ""
		}
	}
	text, _ := current.(string)
	return strings.TrimSpace(text)
}

func isMCPToolDescriptorName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	switch normalized {
	case "callmcptool", "fetchmcpresource", "listmcpresources", "readmcpresource":
		return true
	default:
		return strings.Contains(normalized, "mcp")
	}
}

func splitTaskToolDescriptor(text string) (toolsPart string, subagentsPart string) {
	const marker = "Available subagent_types"
	index := strings.Index(text, marker)
	if index < 0 {
		return text, ""
	}
	toolsPart = strings.TrimSpace(text[:index])
	subagentsPart = strings.TrimSpace(text[index:])
	// Keep JSON structurally non-empty for tools half when marker sits early.
	if toolsPart == "" {
		toolsPart = "{}"
	}
	return toolsPart, subagentsPart
}

func isConversationSummaryText(text string) bool {
	return strings.Contains(text, "<conversation_summary>") || strings.Contains(text, "</conversation_summary>")
}

func isConversationSummaryMessage(message modeladapter.Message) bool {
	return isConversationSummaryText(message.Content)
}

func clampInt64ToInt32(value int64) int32 {
	if value <= 0 {
		return 0
	}
	if value > int64(^uint32(0)>>1) {
		return int32(^uint32(0) >> 1)
	}
	return int32(value)
}
