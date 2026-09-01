package tab

import (
	"strconv"
	"strings"
)

// firstLineNumber extracts the first integer from a model reply, tolerating
// surrounding prose, punctuation and markdown. It returns 0 when no number is
// present, which callers treat as "no prediction".
func firstLineNumber(content string) int32 {
	fields := strings.FieldsFunc(content, func(r rune) bool {
		return r < '0' || r > '9'
	})
	for _, field := range fields {
		value, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		if value > 0 && value <= 1<<30 {
			return int32(value)
		}
	}
	return 0
}
