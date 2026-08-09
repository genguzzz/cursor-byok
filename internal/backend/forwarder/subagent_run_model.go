package forwarder

import (
	"strings"

	"cursor/gen/agentv1"

	"google.golang.org/protobuf/proto"
)

// ExtractEffectiveRunModelID 返回本轮实际应执行的模型 ID。
// child run（subagent_type_name 非空）若有 model override，则以 override 为准。
func ExtractEffectiveRunModelID(message *agentv1.AgentClientMessage) string {
	if model := effectiveChildRunRequestedModel(message); model != nil {
		return extractRequestedModelIDFromRequestedModel(model)
	}
	return extractRequestedModelID(message)
}

// ApplyEffectiveChildRunModel 将 child run 的 requested_model 对齐到 subagent override。
// 仅在发生改写时返回 true。父 run（无 subagent_type_name）永不改写。
func ApplyEffectiveChildRunModel(message *agentv1.AgentClientMessage) bool {
	requested, overrides, subagentType, setRequested := childRunModelFields(message)
	if subagentType == "" || setRequested == nil {
		return false
	}
	overrideModel := lookupChildOverrideRequestedModel(subagentType, overrides)
	if overrideModel == nil {
		return false
	}
	if proto.Equal(requested, overrideModel) {
		return false
	}
	cloned, ok := proto.Clone(overrideModel).(*agentv1.RequestedModel)
	if !ok || cloned == nil {
		return false
	}
	setRequested(cloned)
	return true
}

func effectiveChildRunRequestedModel(message *agentv1.AgentClientMessage) *agentv1.RequestedModel {
	_, overrides, subagentType, _ := childRunModelFields(message)
	if subagentType == "" {
		return nil
	}
	return lookupChildOverrideRequestedModel(subagentType, overrides)
}

func childRunModelFields(message *agentv1.AgentClientMessage) (
	requested *agentv1.RequestedModel,
	overrides []*agentv1.SubagentModelOverride,
	subagentType string,
	setRequested func(*agentv1.RequestedModel),
) {
	if message == nil {
		return nil, nil, "", nil
	}
	if run := message.GetRunRequest(); run != nil {
		return run.GetRequestedModel(), run.GetSubagentModelOverrides(), strings.TrimSpace(run.GetSubagentTypeName()), func(model *agentv1.RequestedModel) {
			run.RequestedModel = model
		}
	}
	if prewarm := message.GetPrewarmRequest(); prewarm != nil {
		return prewarm.GetRequestedModel(), prewarm.GetSubagentModelOverrides(), strings.TrimSpace(prewarm.GetSubagentTypeName()), func(model *agentv1.RequestedModel) {
			prewarm.RequestedModel = model
		}
	}
	return nil, nil, "", nil
}

func lookupChildOverrideRequestedModel(subagentType string, overrides []*agentv1.SubagentModelOverride) *agentv1.RequestedModel {
	subagentType = strings.TrimSpace(subagentType)
	if subagentType == "" || len(overrides) == 0 {
		return nil
	}
	byType := make(map[string]*agentv1.SubagentModelOverride, len(overrides))
	for _, item := range overrides {
		if item == nil {
			continue
		}
		key := strings.TrimSpace(item.GetSubagentType())
		if key == "" {
			continue
		}
		if _, exists := byType[key]; !exists {
			byType[key] = item
		}
	}
	for _, key := range childSubagentOverrideLookupKeys(subagentType) {
		item := byType[key]
		if item == nil {
			continue
		}
		switch item.GetSelection().(type) {
		case *agentv1.SubagentModelOverride_Model:
			model := item.GetModel()
			if model == nil || strings.TrimSpace(model.GetModelId()) == "" {
				return nil
			}
			return model
		default:
			// inherit / disabled：明确命中后不再回落其它别名。
			return nil
		}
	}
	return nil
}

func childSubagentOverrideLookupKeys(subagentType string) []string {
	trimmed := strings.TrimSpace(subagentType)
	if trimmed == "" {
		return nil
	}
	keys := []string{trimmed}
	switch trimmed {
	case "generalPurpose":
		keys = append(keys, "explore")
	case "explore":
		keys = append(keys, "generalPurpose")
	case "browserUse":
		keys = append(keys, "browser-use")
	case "browser-use":
		keys = append(keys, "browserUse")
	}
	return keys
}
