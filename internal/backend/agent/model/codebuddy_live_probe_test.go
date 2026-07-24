package modeladapter

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// 真实出站探测：验证新增 header/gzip 后 CodeBuddy 后端仍接受请求。
// 默认跳过；设置 CB_LIVE=1 时跑。
func TestCodeBuddyLiveRequestAccepted(t *testing.T) {
	if os.Getenv("CB_LIVE") != "1" {
		t.Skip("set CB_LIVE=1 to run live probe")
	}
	apiKey := strings.TrimSpace(os.Getenv("CB_API_KEY"))
	base := strings.TrimSpace(os.Getenv("CB_BASE"))
	model := strings.TrimSpace(os.Getenv("CB_MODEL"))
	proxy := strings.TrimSpace(os.Getenv("CB_PROXY"))
	if apiKey == "" {
		t.Fatal("CB_API_KEY required")
	}
	if base == "" {
		base = "https://copilot.tencent.com/v2"
	}
	if model == "" {
		model = "deepseek-v4-flash-ioa"
	}

	adapter := NewCodeBuddyAdapter()
	req := StreamRequest{
		BaseURL:                   base,
		APIKey:                    apiKey,
		ProviderModelID:           model,
		ModelID:                   model,
		OpenAIEndpoint:            "/custom",
		Proxy:                     proxy,
		ConversationID:            "live-probe-conv-0001",
		RequestID:                 "live-probe-req-0001",
		ModelCallID:               "live-probe-call-0001",
		Messages:                  []Message{{Role: "user", Content: "只回复一个字：好"}},
		MaxTokens:                 32,
		ProviderStreamIdleTimeout: 45 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var events int
	err := adapter.Stream(ctx, req, func(ev ModelEvent) error {
		events++
		if events <= 8 {
			t.Logf("event#%d kind=%v text=%q finish=%q", events, ev.Kind, ev.Text, ev.FinishReason)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("live Stream rejected: %v", err)
	}
	if events == 0 {
		t.Fatal("no events received")
	}
	t.Logf("live OK events=%d", events)
}
