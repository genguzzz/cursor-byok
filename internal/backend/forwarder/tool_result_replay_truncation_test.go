package forwarder

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestLimitProjectedToolResultReplayKeepsReadImage(t *testing.T) {
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01, 0xFF, 0xD9}
	// 构造超过 projectedReadReplayLimit(64KiB) 的假大图 base64，确保图片路径跳过截断。
	big := make([]byte, 80*1024)
	copy(big, jpeg)
	for i := len(jpeg); i < len(big); i++ {
		big[i] = byte(i % 251)
	}
	big[0], big[1], big[2] = 0xFF, 0xD8, 0xFF
	payload, err := json.Marshal(map[string]any{
		"success": map[string]any{
			"path": "/tmp/big.jpg",
			"data": base64.StdEncoding.EncodeToString(big),
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	content := string(payload)
	if len(content) <= projectedReadReplayLimit {
		t.Fatalf("fixture too small: %d", len(content))
	}
	got := limitProjectedToolResultReplay("Read", content, "", false, false)
	if got != strings.TrimSpace(content) {
		t.Fatalf("Read image JSON should bypass truncation; got len=%d want=%d", len(got), len(content))
	}
}

func TestLimitProjectedToolResultReplayStillTruncatesReadText(t *testing.T) {
	body := strings.Repeat("x", projectedReadReplayLimit+2048)
	content := `{"success":{"path":"big.txt","content":"` + body + `"}}`
	got := limitProjectedToolResultReplay("Read", content, "", false, false)
	if len(got) > projectedReadReplayLimit {
		t.Fatalf("text Read should still truncate, got len=%d", len(got))
	}
	if !strings.Contains(got, "[truncated:") {
		t.Fatalf("expected truncation notice, got %q", got[:min(120, len(got))])
	}
}
