package forwarder

import (
	"testing"

	"cursor/gen/agentv1"

	"google.golang.org/protobuf/proto"
)

func TestApplyEffectiveChildRunModelExploreOverride(t *testing.T) {
	message := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{
				ConversationId:   proto.String("child"),
				SubagentTypeName: proto.String("explore"),
				RequestedModel:   &agentv1.RequestedModel{ModelId: "grok-4.5"},
				SubagentModelOverrides: []*agentv1.SubagentModelOverride{
					{
						SubagentType: "explore",
						Selection: &agentv1.SubagentModelOverride_Model{
							Model: &agentv1.RequestedModel{
								ModelId: "752cecad0d652981",
								Parameters: []*agentv1.RequestedModel_ModelParameterValue{
									{Id: "thinking_effort", Value: "high"},
								},
							},
						},
					},
				},
			},
		},
	}
	if ExtractEffectiveRunModelID(message) != "752cecad0d652981" {
		t.Fatalf("effective = %q", ExtractEffectiveRunModelID(message))
	}
	if !ApplyEffectiveChildRunModel(message) {
		t.Fatal("expected rewrite")
	}
	if got := message.GetRunRequest().GetRequestedModel().GetModelId(); got != "752cecad0d652981" {
		t.Fatalf("requested_model = %q", got)
	}
	if ExtractRequestedModelID(message) != "752cecad0d652981" {
		t.Fatalf("after apply extract = %q", ExtractRequestedModelID(message))
	}
	if ApplyEffectiveChildRunModel(message) {
		t.Fatal("second apply should be no-op")
	}
}

func TestApplyEffectiveChildRunModelInheritAndParent(t *testing.T) {
	inherit := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{
				SubagentTypeName: proto.String("explore"),
				RequestedModel:   &agentv1.RequestedModel{ModelId: "752cecad0d652981"},
				SubagentModelOverrides: []*agentv1.SubagentModelOverride{
					{SubagentType: "explore", Selection: &agentv1.SubagentModelOverride_Inherit{Inherit: true}},
				},
			},
		},
	}
	if ExtractEffectiveRunModelID(inherit) != "752cecad0d652981" {
		t.Fatalf("inherit effective = %q", ExtractEffectiveRunModelID(inherit))
	}
	if ApplyEffectiveChildRunModel(inherit) {
		t.Fatal("inherit must not rewrite")
	}

	parent := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{
				RequestedModel: &agentv1.RequestedModel{ModelId: "grok-4.5"},
				SubagentModelOverrides: []*agentv1.SubagentModelOverride{
					{
						SubagentType: "explore",
						Selection: &agentv1.SubagentModelOverride_Model{
							Model: &agentv1.RequestedModel{ModelId: "752cecad0d652981"},
						},
					},
				},
			},
		},
	}
	if ExtractEffectiveRunModelID(parent) != "grok-4.5" {
		t.Fatalf("parent effective = %q", ExtractEffectiveRunModelID(parent))
	}
	if ApplyEffectiveChildRunModel(parent) {
		t.Fatal("parent run must not rewrite from explore override")
	}
}

func TestApplyEffectiveChildRunModelAliasGeneralPurpose(t *testing.T) {
	message := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{
				SubagentTypeName: proto.String("generalPurpose"),
				RequestedModel:   &agentv1.RequestedModel{ModelId: "grok-4.5"},
				SubagentModelOverrides: []*agentv1.SubagentModelOverride{
					{
						SubagentType: "explore",
						Selection: &agentv1.SubagentModelOverride_Model{
							Model: &agentv1.RequestedModel{ModelId: "83fd8bfc917e5492"},
						},
					},
				},
			},
		},
	}
	if ExtractEffectiveRunModelID(message) != "83fd8bfc917e5492" {
		t.Fatalf("alias effective = %q", ExtractEffectiveRunModelID(message))
	}
}
