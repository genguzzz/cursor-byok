package runtimecore

import (
	"testing"
)

func TestReadStringArgCoercesJSONNumber(t *testing.T) {
	t.Parallel()

	t.Run("json number coerced to string", func(t *testing.T) {
		args, err := DecodeArgsMap([]byte(`{"shell_id": 340173, "block_until_ms": 15000}`))
		if err != nil {
			t.Fatalf("DecodeArgsMap failed: %v", err)
		}
		got := ReadStringArg(args, "shell_id")
		if got != "340173" {
			t.Fatalf("ReadStringArg shell_id = %q, want %q", got, "340173")
		}
	})

	t.Run("string value unchanged", func(t *testing.T) {
		args, err := DecodeArgsMap([]byte(`{"shell_id": "340173"}`))
		if err != nil {
			t.Fatalf("DecodeArgsMap failed: %v", err)
		}
		got := ReadStringArg(args, "shell_id")
		if got != "340173" {
			t.Fatalf("ReadStringArg shell_id = %q, want %q", got, "340173")
		}
	})

	t.Run("missing key returns empty", func(t *testing.T) {
		args, err := DecodeArgsMap([]byte(`{"other": 1}`))
		if err != nil {
			t.Fatalf("DecodeArgsMap failed: %v", err)
		}
		got := ReadStringArg(args, "shell_id")
		if got != "" {
			t.Fatalf("ReadStringArg shell_id = %q, want empty", got)
		}
	})

	t.Run("nil value returns empty", func(t *testing.T) {
		args, err := DecodeArgsMap([]byte(`{"shell_id": null}`))
		if err != nil {
			t.Fatalf("DecodeArgsMap failed: %v", err)
		}
		got := ReadStringArg(args, "shell_id")
		if got != "" {
			t.Fatalf("ReadStringArg shell_id = %q, want empty for null", got)
		}
	})

	t.Run("fallback to second key", func(t *testing.T) {
		args, err := DecodeArgsMap([]byte(`{"task_id": 340173}`))
		if err != nil {
			t.Fatalf("DecodeArgsMap failed: %v", err)
		}
		got := ReadStringArg(args, "shell_id", "task_id")
		if got != "340173" {
			t.Fatalf("ReadStringArg fallback = %q, want %q", got, "340173")
		}
	})
}
