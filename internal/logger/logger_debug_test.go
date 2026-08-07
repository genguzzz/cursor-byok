package logger

import "testing"

func TestDebugLoggingGatedByDefault(t *testing.T) {
	SetDebugEnabled(false)
	defer SetDebugEnabled(false)

	if DebugEnabled() {
		t.Fatal("debug should be off")
	}
	// 关闭时必须空操作，不能因未 Init 而 panic。
	Debugf("[shell-debug] should be silenced %s", "x")
	Debug("silenced", "k", "v")

	SetDebugEnabled(true)
	if !DebugEnabled() {
		t.Fatal("debug should be on")
	}
	Debugf("[shell-debug] visible %s", "y")
	Debug("visible", "k", "v")
}
