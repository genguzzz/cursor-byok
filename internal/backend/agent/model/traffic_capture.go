package modeladapter

import (
	"sync"
	"sync/atomic"
)

// providerTrafficCaptureQueueSize bounds how many pending hops can wait for the
// single capture worker. providerTrafficCaptureQueueBytes additionally caps the
// total queued body bytes so a stalled debug UI cannot pin an unbounded amount
// of captured bodies behind the worker.
const (
	providerTrafficCaptureQueueSize  = 256
	providerTrafficCaptureQueueBytes = 64 << 20
)

var providerTrafficCapture atomic.Pointer[providerTrafficCaptureBox]

type providerTrafficCaptureBox struct {
	capture     ProviderTrafficCapture
	queue       chan ProviderTrafficHop
	stop        chan struct{}
	queuedBytes atomic.Int64
	wg          sync.WaitGroup
}

// SetProviderTrafficCapture registers or clears the local-server provider traffic sink.
func SetProviderTrafficCapture(capture ProviderTrafficCapture) {
	previous := providerTrafficCapture.Swap(nil)
	if previous != nil {
		close(previous.stop)
		previous.wg.Wait()
	}
	if capture == nil {
		return
	}
	box := &providerTrafficCaptureBox{
		capture: capture,
		queue:   make(chan ProviderTrafficHop, providerTrafficCaptureQueueSize),
		stop:    make(chan struct{}),
	}
	box.wg.Add(1)
	go box.run()
	providerTrafficCapture.Store(box)
}

// run drains the queue. stop is checked before each dequeue so that clearing the
// box drains at most one more hop instead of the full backlog, keeping
// SetProviderTrafficCapture(nil)'s wg.Wait() fast and bounded.
func (box *providerTrafficCaptureBox) run() {
	defer box.wg.Done()
	for {
		select {
		case <-box.stop:
			return
		default:
		}
		select {
		case <-box.stop:
			return
		case hop := <-box.queue:
			box.queuedBytes.Add(-int64(len(hop.RequestBody) + len(hop.ResponseBody)))
			box.capture.CaptureProvider(hop)
		}
	}
}

// providerTrafficCaptureEnabled reports whether a sink is currently registered
// so the hot request path can skip body copying entirely when debug is off.
func providerTrafficCaptureEnabled() bool {
	box := providerTrafficCapture.Load()
	return box != nil && box.capture != nil
}

func emitProviderTrafficCapture(hop ProviderTrafficHop) {
	box := providerTrafficCapture.Load()
	if box == nil || box.capture == nil {
		return
	}
	size := int64(len(hop.RequestBody) + len(hop.ResponseBody))
	if size > 0 && box.queuedBytes.Add(size) > providerTrafficCaptureQueueBytes {
		box.queuedBytes.Add(-size)
		return
	}
	select {
	case box.queue <- hop:
	default:
		// Queue full: drop this hop rather than spawning another goroutine or
		// blocking the request path on debug capture.
		box.queuedBytes.Add(-size)
	}
}
