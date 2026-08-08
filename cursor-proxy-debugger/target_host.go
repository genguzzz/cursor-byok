package proxydebugger

import (
	"net"
	"strings"
)

// matchesCursorRelayHost 与 internal/mitm.isWhitelistedRelayHost 对齐：
// api2/api3.cursor.sh 以及任意 *.cursor.sh。
func matchesCursorRelayHost(host string) bool {
	host = normalizeHost(host)
	if host == "" {
		return false
	}
	if host == "api2.cursor.sh" || host == "api3.cursor.sh" {
		return true
	}
	return strings.HasSuffix(host, ".cursor.sh")
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "[") {
		host = strings.TrimPrefix(host, "[")
		host = strings.TrimSuffix(host, "]")
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return strings.ToLower(strings.TrimSpace(h))
	}
	if strings.Count(host, ":") > 1 {
		return strings.Trim(host, "[]")
	}
	return host
}

func parseTargetHostPatterns(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{"*.cursor.sh"}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(strings.ToLower(part))
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return []string{"*.cursor.sh"}
	}
	return out
}

func hostMatchesPatterns(host string, patterns []string) bool {
	host = normalizeHost(host)
	if host == "" {
		return false
	}
	for _, pattern := range patterns {
		switch {
		case pattern == "*.cursor.sh":
			if matchesCursorRelayHost(host) {
				return true
			}
		case strings.HasPrefix(pattern, "*.") && strings.HasSuffix(host, strings.TrimPrefix(pattern, "*")):
			return true
		case host == pattern:
			return true
		}
	}
	return false
}
