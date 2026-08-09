package interaction

import (
	neturl "net/url"
	"strings"

	"cursor/gen/agentv1"
)

func normalizeSearchURLKey(rawURL string) string {
	parsed, err := neturl.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	query := parsed.Query()
	for _, key := range collectQueryKeys(query) {
		lower := strings.ToLower(key)
		if lower == "utm" || strings.HasPrefix(lower, "utm_") ||
			lower == "fbclid" || lower == "gclid" || lower == "spm" {
			query.Del(key)
		}
	}
	if encoded := query.Encode(); encoded != "" {
		return host + path + "?" + encoded
	}
	return host + path
}

func collectQueryKeys(values neturl.Values) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func mergeWebSearchReferences(primary []*agentv1.WebSearchReference, secondary []*agentv1.WebSearchReference, limit int) []*agentv1.WebSearchReference {
	if limit <= 0 {
		limit = webSearchMergedLimit
	}
	merged := make([]*agentv1.WebSearchReference, 0, limit)
	seen := map[string]struct{}{}
	appendAll := func(items []*agentv1.WebSearchReference) {
		for _, item := range items {
			if item == nil || len(merged) >= limit {
				return
			}
			key := normalizeSearchURLKey(item.GetUrl())
			if key == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			next := *item
			merged = append(merged, &next)
		}
	}
	appendAll(primary)
	appendAll(secondary)
	return merged
}
