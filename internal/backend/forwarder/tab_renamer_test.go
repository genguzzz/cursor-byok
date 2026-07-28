package forwarder

import (
	"testing"

	"cursor/gen/aiserverv1"
)

func TestCleanGeneratedTabName(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "plain single line", raw: "重构 Go 服务的网络代理层", want: "重构 Go 服务的网络代理层"},
		{name: "with prefix", raw: "标题：会话性能调优", want: "会话性能调优"},
		{name: "with english prefix", raw: "Title: Refactor NameTab handler", want: "Refactor NameTab handler"},
		{name: "strips code fence", raw: "```\n会话标题\n```", want: "会话标题"},
		{name: "truncates long output", raw: "一个非常非常长的标题用来测试截断逻辑xxxxxxxxxxxxxxx", want: "一个非常非常长的标题用来测试截断逻辑xxxxxx"},
		{name: "drops trailing punctuation", raw: "\"Hello world!\"", want: "Hello world!"},
		{name: "empty after cleaning", raw: "```\n```", want: ""},
		{name: "only newlines", raw: "\n\n\n", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanGeneratedTabName(tc.raw, 24)
			if got != tc.want {
				t.Fatalf("cleanGeneratedTabName(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestFlattenNameTabConversationMessages(t *testing.T) {
	t.Run("empty list returns empty", func(t *testing.T) {
		got := flattenNameTabConversationMessages(nil, 1000)
		if got != "" {
			t.Fatalf("want empty, got %q", got)
		}
	})
	t.Run("joins human and ai with role label", func(t *testing.T) {
		human := &aiserverv1.ConversationMessage{Type: aiserverv1.ConversationMessage_MESSAGE_TYPE_HUMAN, Text: "帮我看看这段 Go 代码哪里有 bug"}
		ai := &aiserverv1.ConversationMessage{Type: aiserverv1.ConversationMessage_MESSAGE_TYPE_AI, Text: "请把文件贴出来"}
		got := flattenNameTabConversationMessages([]*aiserverv1.ConversationMessage{human, ai}, 1000)
		want := "user: 帮我看看这段 Go 代码哪里有 bug\nassistant: 请把文件贴出来"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})
	t.Run("respects max chars tail truncation", func(t *testing.T) {
		human := &aiserverv1.ConversationMessage{Type: aiserverv1.ConversationMessage_MESSAGE_TYPE_HUMAN, Text: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
		got := flattenNameTabConversationMessages([]*aiserverv1.ConversationMessage{human}, 20)
		want := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"[44:]
		if got != want {
			t.Fatalf("truncate tail failed: got %q want %q", got, want)
		}
	})
}

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "shorter than max", in: "abc", max: 5, want: "abc"},
		{name: "equal to max", in: "abcde", max: 5, want: "abcde"},
		{name: "longer than max keeps tail", in: "abcdefghij", max: 4, want: "ghij"},
		{name: "max zero passthrough", in: "abc", max: 0, want: "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateRunes(tc.in, tc.max)
			if got != tc.want {
				t.Fatalf("truncateRunes(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}

func TestNormalizeNonNegativeTabRuneCount(t *testing.T) {
	cases := []struct {
		name     string
		value    int
		fallback int
		want     int
	}{
		{name: "zero uses fallback", value: 0, fallback: 8, want: 8},
		{name: "positive kept", value: 12, fallback: 8, want: 12},
		{name: "negative treated as zero", value: -1, fallback: 8, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeNonNegativeTabRuneCount(tc.value, tc.fallback)
			if got != tc.want {
				t.Fatalf("normalizeNonNegativeTabRuneCount(%d, %d) = %d, want %d", tc.value, tc.fallback, got, tc.want)
			}
		})
	}
}