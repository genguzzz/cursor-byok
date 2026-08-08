package upstream

import (
	"sync"
	"testing"
	"time"
)

type captureStub struct {
	mu   sync.Mutex
	hops []TrafficHop
}

func (c *captureStub) CaptureUpstream(hop TrafficHop) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hops = append(c.hops, hop)
}

func TestSetTrafficCaptureEmitsAsync(t *testing.T) {
	stub := &captureStub{}
	SetTrafficCapture(stub)
	t.Cleanup(func() { SetTrafficCapture(nil) })

	emitTrafficCapture(TrafficHop{Method: "POST", Path: "/aiserver.v1.CmdKService/StreamCmdK", Status: 200})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stub.mu.Lock()
		n := len(stub.hops)
		stub.mu.Unlock()
		if n == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected capture hop")
}
