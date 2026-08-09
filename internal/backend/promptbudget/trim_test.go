package promptbudget

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTrimMessagesProtectsSystemAndRecentSmall(t *testing.T) {
	huge := strings.Repeat("x", 100_000)
	messages := []Message{
		{Role: "system", Content: "You are a coding agent."},
		{Role: "user", Content: "<user_query>\nkeep me\n</user_query>\n" + huge},
		{Role: "tool", Content: huge},
		{Role: "tool", Content: huge},
		{Role: "user", Content: "<user_query>\nlatest\n</user_query>"},
	}
	// Small budget forces fair trim / omit of huge tool results.
	got := TrimMessages(messages, 50_000)
	if got[0].Content != "You are a coding agent." {
		t.Fatalf("system must stay intact: %q", got[0].Content)
	}
	total := 0
	for _, message := range got {
		total += utf8.RuneCountInString(message.Content)
	}
	if total > 50_000+2_000 { // allow omit-notice overhead band
		t.Fatalf("trimmed total chars=%d exceeds budget band", total)
	}
	if !strings.Contains(got[len(got)-1].Content, "latest") {
		t.Fatalf("latest small user query should remain: %q", got[len(got)-1].Content)
	}
	joined := ""
	for _, message := range got {
		joined += message.Content
	}
	if !strings.Contains(joined, "omitted") && !strings.Contains(joined, "truncated") {
		t.Fatalf("expected omit/truncate markers, got %q", joined[:minInt(400, len(joined))])
	}
}

func TestMaxMinFairAllocations(t *testing.T) {
	alloc := maxMinFairAllocations([]int{10, 1000, 1000}, 1000)
	if alloc[0] != 10 {
		t.Fatalf("small message should be fully kept: %v", alloc)
	}
	if alloc[1]+alloc[2]+alloc[0] > 1000 {
		t.Fatalf("allocations exceed budget: %v", alloc)
	}
}

func TestPreferUserQueryPrefix(t *testing.T) {
	content := "noise " + strings.Repeat("n", 500) + "\n<user_query>\nimportant\n</user_query>"
	got := preferUserQueryPrefix(content, 80)
	if !strings.Contains(got, "<user_query>") || !strings.Contains(got, "important") {
		t.Fatalf("expected user_query retained, got %q", got)
	}
}
