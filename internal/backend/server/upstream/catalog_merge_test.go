package upstream

import (
	"testing"

	"cursor/gen/aiserverv1"
	legacyruntime "cursor/internal/runtime"

	"google.golang.org/protobuf/proto"
)

func TestMergeAvailableModelsAppendsInjectedAndSkipsCollision(t *testing.T) {
	upstreamResp := &aiserverv1.AvailableModelsResponse{
		Models: []*aiserverv1.AvailableModelsResponse_AvailableModel{
			{Name: "claude-4-sonnet", ClientDisplayName: proto.String("Claude")},
			{Name: "dup-channel", ClientDisplayName: proto.String("Upstream Dup")},
		},
		ComposerModelConfig: &aiserverv1.AvailableModelsResponse_FeatureModelConfig{DefaultModel: "claude-4-sonnet"},
	}
	adapters := []legacyruntime.ModelAdapterConfig{
		{ID: "abc123def4567890", ModelID: "gpt-test", DisplayName: "Local GPT", Type: "openai"},
		{ID: "dup-channel", ModelID: "other", DisplayName: "Local Dup", Type: "openai"},
	}
	injected, err := injectedAvailableModelProtos(adapters)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]struct{}{"claude-4-sonnet": {}, "dup-channel": {}}
	for _, model := range injected {
		name := model.GetName()
		if _, exists := seen[name]; exists {
			continue
		}
		upstreamResp.Models = append(upstreamResp.Models, model)
		seen[name] = struct{}{}
	}
	if len(upstreamResp.Models) != 3 {
		t.Fatalf("merged count = %d want 3", len(upstreamResp.Models))
	}
	if upstreamResp.GetComposerModelConfig().GetDefaultModel() != "claude-4-sonnet" {
		t.Fatal("composer default was overwritten")
	}
	names := map[string]bool{}
	for _, model := range upstreamResp.Models {
		names[model.GetName()] = true
	}
	if !names["abc123def4567890"] || !names["claude-4-sonnet"] || !names["dup-channel"] {
		t.Fatalf("unexpected names %#v", names)
	}
}
