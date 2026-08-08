package mixed

import "testing"

func TestIsLocalAiServicePath(t *testing.T) {
	if !IsLocalAiServicePath("/aiserver.v1.AiService/NameTab") {
		t.Fatal("NameTab should stay local")
	}
	if IsLocalAiServicePath("/aiserver.v1.AiService/AvailableModels") {
		t.Fatal("AvailableModels is mixed-catalog, not catch-all local")
	}
}
