// Package promptbudget implements official-style prompt character budget trimming.
// Kept as a small dependency-free package so tests do not compile gen/aiserverv1.
package promptbudget

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Official agent-exec constant V=32e5 for max-min fair prompt truncation.
const CharBudget = 3_200_000

// Official minUsefulChars (K=200): allocations below this omit the message.
const MinUsefulChars = 200

// Message is the minimal surface needed for budget trimming.
type Message struct {
	Role    string
	Content string
}

// TrimMessages applies max-min fair character allocation to non-system messages.
// System messages are always preserved and deducted from the budget first.
// Truncation is projection-only; callers must not persist the result as history.
func TrimMessages(messages []Message, budget int) []Message {
	if budget <= 0 {
		budget = CharBudget
	}
	if len(messages) == 0 {
		return messages
	}

	systemCost := 0
	for _, message := range messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "system") {
			systemCost += utf8.RuneCountInString(message.Content)
		}
	}
	remainingBudget := budget - systemCost
	if remainingBudget < 0 {
		remainingBudget = 0
	}

	indices := make([]int, 0, len(messages))
	sizes := make([]int, 0, len(messages))
	roles := make([]string, 0, len(messages))
	for index, message := range messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "system") {
			continue
		}
		indices = append(indices, index)
		sizes = append(sizes, utf8.RuneCountInString(message.Content))
		roles = append(roles, strings.TrimSpace(message.Role))
	}
	if len(indices) == 0 {
		return messages
	}

	total := 0
	for _, size := range sizes {
		total += size
	}
	// Separators between kept parts (~official 2*(n-1)).
	separatorCost := 0
	if len(sizes) > 1 {
		separatorCost = 2 * (len(sizes) - 1)
	}
	omitNotice := omittedMessagesNotice(len(sizes))
	allocBudget := remainingBudget - separatorCost - utf8.RuneCountInString(omitNotice)
	if allocBudget < 0 {
		allocBudget = 0
	}
	if total <= allocBudget {
		return messages
	}

	allocations := maxMinFairAllocations(sizes, allocBudget)
	out := make([]Message, len(messages))
	copy(out, messages)
	dropped := 0
	for i, messageIndex := range indices {
		alloc := allocations[i]
		size := sizes[i]
		content := messages[messageIndex].Content
		role := roles[i]
		switch {
		case alloc >= size:
			continue
		case alloc < MinUsefulChars:
			dropped++
			out[messageIndex].Content = fmt.Sprintf("[omitted %s message, %d chars]", firstNonEmpty(role, "message"), size)
		default:
			out[messageIndex].Content = truncateMessageContent(role, content, alloc)
		}
	}
	if dropped > 0 {
		// Prefix a single conversation-level omit notice onto the first non-system message.
		for i := range out {
			if strings.EqualFold(strings.TrimSpace(out[i].Role), "system") {
				continue
			}
			out[i].Content = omittedMessagesNotice(dropped) + out[i].Content
			break
		}
	}
	return out
}

func maxMinFairAllocations(sizes []int, budget int) []int {
	n := len(sizes)
	if n == 0 {
		return nil
	}
	alloc := make([]int, n)
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool {
		return sizes[order[i]] < sizes[order[j]]
	})
	remaining := budget
	remainingCount := n
	for _, index := range order {
		if remainingCount <= 0 {
			break
		}
		fair := remaining / remainingCount
		if sizes[index] <= fair {
			alloc[index] = sizes[index]
			remaining -= sizes[index]
		} else {
			alloc[index] = fair
			remaining -= fair
		}
		remainingCount--
	}
	return alloc
}

func truncateMessageContent(role string, content string, limit int) string {
	runes := []rune(content)
	notice := fmt.Sprintf("\n[... truncated, %d chars]", len(runes))
	keep := limit - utf8.RuneCountInString(notice)
	if keep <= 0 {
		if limit <= 0 {
			return notice
		}
		return string(runes[:minInt(limit, len(runes))])
	}
	if strings.EqualFold(role, "user") {
		return preferUserQueryPrefix(content, keep) + notice
	}
	if keep >= len(runes) {
		return content
	}
	return string(runes[:keep]) + notice
}

// preferUserQueryPrefix keeps <user_query> blocks when possible (official X()).
func preferUserQueryPrefix(content string, limit int) string {
	if limit <= 0 {
		return ""
	}
	queries := extractUserQueryBlocks(content)
	if len(queries) == 0 {
		runes := []rune(content)
		if limit >= len(runes) {
			return content
		}
		return string(runes[:limit])
	}
	joined := strings.Join(queries, "\n")
	if utf8.RuneCountInString(joined) >= limit {
		runes := []rune(joined)
		return string(runes[:limit])
	}
	remainder := content
	for _, query := range queries {
		remainder = strings.Replace(remainder, query, "", 1)
	}
	remainder = strings.TrimSpace(collapseBlankLines(remainder))
	if remainder == "" {
		return joined
	}
	left := limit - utf8.RuneCountInString(joined) - 1
	if left <= 0 {
		return joined
	}
	runes := []rune(remainder)
	if left > len(runes) {
		left = len(runes)
	}
	return string(runes[:left]) + "\n" + joined
}

func extractUserQueryBlocks(content string) []string {
	const open = "<user_query>"
	const close = "</user_query>"
	var blocks []string
	remaining := content
	for {
		start := strings.Index(remaining, open)
		if start < 0 {
			break
		}
		end := strings.Index(remaining[start:], close)
		if end < 0 {
			break
		}
		end += start + len(close)
		blocks = append(blocks, remaining[start:end])
		remaining = remaining[end:]
	}
	return blocks
}

func collapseBlankLines(text string) string {
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return text
}

func omittedMessagesNotice(count int) string {
	if count <= 0 {
		return ""
	}
	return fmt.Sprintf("[%d message(s) omitted due to size limits; omitted content may appear anywhere in the conversation. See transcript file for full history.]\n\n", count)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
