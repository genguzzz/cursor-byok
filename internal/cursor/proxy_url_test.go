package cursor

import "testing"

func TestIsLocalAssistantProxyURL(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"http://127.0.0.1:18080", true},
		{"http://localhost:18080", true},
		{"http://127.0.0.1:9092", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsLocalAssistantProxyURL(tc.in); got != tc.want {
			t.Fatalf("IsLocalAssistantProxyURL(%q)=%v want %v", tc.in, got, tc.want)
		}
	}
}

func TestIsDebugProxyURL(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"http://127.0.0.1:9092", true},
		{"http://localhost:9092", true},
		{"http://127.0.0.1:18080", false},
		{"http://127.0.0.1:9090", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsDebugProxyURL(tc.in); got != tc.want {
			t.Fatalf("IsDebugProxyURL(%q)=%v want %v", tc.in, got, tc.want)
		}
	}
}
