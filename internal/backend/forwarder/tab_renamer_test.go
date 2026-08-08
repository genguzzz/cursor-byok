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
		{name: "drops trailing quotes", raw: "\"Hello world!\"", want: "Hello world!"},
		{name: "empty after cleaning", raw: "```\n```", want: ""},
		{name: "only newlines", raw: "\n\n\n", want: ""},
		// 新增：对话式输出 - 取非对话行中最长的
		{
			name: "conversational prefix stripped",
			raw:  "好的，这个对话的标题是：重构 API 鉴权模块",
			want: "好的，这个对话的标题是：重构 API 鉴权模块",
		},
		{
			name: "multi-line picks best title",
			raw:  "好的，让我看看这个对话的内容。\n重构 API 鉴权模块\n这个标题应该可以",
			want: "重构 API 鉴权模块",
		},
		{
			name: "all conversational picks last",
			raw:  "好的，这是一个关于 Python 脚本的对话。\n这个对话主要讨论网络代理层重构。",
			want: "这个对话主要讨论网络代理层重构。",
		},
		{
			name: "prefix with conversational header",
			raw:  "对话标题：\n重构 Go 服务网络代理层",
			want: "重构 Go 服务网络代理层",
		},
		{
			name: "english conversational prefixes",
			raw:  "Sure! The title is: Refactor Auth Module\nHere is the conversation summary.",
			want: "Here is the conversation",
		},
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

func TestCleanGeneratedTabNameMaxChars(t *testing.T) {
	// 验证 maxChars=0 时使用默认值
	got := cleanGeneratedTabName("一个非常非常长的标题用来测试截断逻辑超出默认值xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", 0)
	if len([]rune(got)) != tabRenamerDefaultMaxNameChars {
		t.Fatalf("cleanGeneratedTabName with maxChars=0 should use default %d, got %d chars: %q", tabRenamerDefaultMaxNameChars, len([]rune(got)), got)
	}
}

func TestCleanGeneratedTabNameNoTruncation(t *testing.T) {
	// 验证不带 max 约束时不会被截断
	got := cleanGeneratedTabName("Sure! The title is: Refactor Auth Module\nHere is the conversation summary.", 100)
	want := "Here is the conversation summary."
	if got != want {
		t.Fatalf("cleanGeneratedTabName with large max = %q, want %q", got, want)
	}
}

func TestExtractBestTitleLine(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "single line passthrough",
			raw:  "重构 Go 服务",
			want: "重构 Go 服务",
		},
		{
			name: "picks longest non-conversational",
			raw:  "好的\n重构 API 鉴权模块\n嗯",
			want: "重构 API 鉴权模块",
		},
		{
			name: "all conversational picks last",
			raw:  "好的\n这个对话是",
			want: "这个对话是",
		},
		{
			name: "empty input",
			raw:  "",
			want: "",
		},
		{
			name: "empty lines filtered",
			raw:  "\n\n重构 API 鉴权模块\n\n",
			want: "重构 API 鉴权模块",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractBestTitleLine(tc.raw)
			if got != tc.want {
				t.Fatalf("extractBestTitleLine(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestStripTitlePrefixes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "chinese colon", raw: "标题：重构 API", want: "重构 API"},
		{name: "chinese semi", raw: "标题:重构 API", want: "重构 API"},
		{name: "english colon", raw: "Title: Refactor", want: "Refactor"},
		{name: "lowercase", raw: "title: Refactor", want: "Refactor"},
		{name: "conversation title", raw: "会话标题：重构 API", want: "重构 API"},
		{name: "no prefix", raw: "重构 API", want: "重构 API"},
		{name: "uppercase", raw: "TITLE: Refactor", want: "Refactor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripTitlePrefixes(tc.raw)
			if got != tc.want {
				t.Fatalf("stripTitlePrefixes(%q) = %q, want %q", tc.raw, got, tc.want)
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
	t.Run("wraps with delimiter", func(t *testing.T) {
		human := &aiserverv1.ConversationMessage{Type: aiserverv1.ConversationMessage_MESSAGE_TYPE_HUMAN, Text: "帮我看看这段 Go 代码哪里有 bug"}
		ai := &aiserverv1.ConversationMessage{Type: aiserverv1.ConversationMessage_MESSAGE_TYPE_AI, Text: "请把文件贴出来"}
		got := flattenNameTabConversationMessages([]*aiserverv1.ConversationMessage{human, ai}, 1000)
		want := "对话内容：\n---\nuser: 帮我看看这段 Go 代码哪里有 bug\nassistant: 请把文件贴出来\n---"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})
	t.Run("respects max chars tail truncation", func(t *testing.T) {
		human := &aiserverv1.ConversationMessage{Type: aiserverv1.ConversationMessage_MESSAGE_TYPE_HUMAN, Text: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
		got := flattenNameTabConversationMessages([]*aiserverv1.ConversationMessage{human}, 80)
		if len([]rune(got)) > 80 {
			t.Fatalf("should not exceed 80 chars, got %d: %q", len([]rune(got)), got)
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