package upstream

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// trafficCaptureQueueSize bounds how many pending hops can wait for the single
// capture worker. trafficCaptureQueueBytes additionally caps the total queued
// body bytes so a stalled debug UI cannot pin an unbounded amount of captured
// bodies behind the worker (each hop carries up to 16 MiB per side).
const (
	trafficCaptureQueueSize  = 256
	trafficCaptureQueueBytes = 64 << 20
)

// TrafficCapture 可选地把 backend→官方 的第二跳交给调试面板。
// Menubar 与 backend 同进程时由调试代理注册；standalone debugger 无 hook。
type TrafficCapture interface {
	CaptureUpstream(hop TrafficHop)
}

// TrafficHop 是一次回源的摘要（body 可能被截断）。
type TrafficHop struct {
	StartedAt      time.Time
	Duration       time.Duration
	Method         string
	URL            string
	Host           string
	Path           string
	Status         int
	RequestID      string
	RequestHeader  http.Header
	ResponseHeader http.Header
	RequestBody    []byte
	ResponseBody   []byte
	Error          string
}

var trafficCapture atomic.Pointer[trafficCaptureBox]

type trafficCaptureBox struct {
	capture     TrafficCapture
	queue       chan TrafficHop
	stop        chan struct{}
	queuedBytes atomic.Int64
	wg          sync.WaitGroup
}

// SetTrafficCapture 注册或清空（传 nil）回源抓包 sink。
// 注册时会起一个常驻 worker 顺序消费 hop 队列；不再为每个 hop 起独立 goroutine，
// 避免慢速/积压的调试 UI 让 BidiAppend/RunSSE 的请求量直接放大成同等数量的 goroutine。
func SetTrafficCapture(capture TrafficCapture) {
	previous := trafficCapture.Swap(nil)
	if previous != nil {
		close(previous.stop)
		previous.wg.Wait()
	}
	if capture == nil {
		return
	}
	box := &trafficCaptureBox{
		capture: capture,
		queue:   make(chan TrafficHop, trafficCaptureQueueSize),
		stop:    make(chan struct{}),
	}
	box.wg.Add(1)
	go box.run()
	trafficCapture.Store(box)
}

// run drains the queue. stop is checked before each dequeue so that closing the
// box drains at most one more hop instead of the full backlog, keeping
// SetTrafficCapture(nil)'s wg.Wait() fast and bounded.
func (box *trafficCaptureBox) run() {
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
			box.capture.CaptureUpstream(hop)
		}
	}
}

func emitTrafficCapture(hop TrafficHop) {
	box := trafficCapture.Load()
	if box == nil || box.capture == nil {
		return
	}
	size := int64(len(hop.RequestBody) + len(hop.ResponseBody))
	if size > 0 && box.queuedBytes.Add(size) > trafficCaptureQueueBytes {
		box.queuedBytes.Add(-size)
		return
	}
	select {
	case box.queue <- hop:
	default:
		// Queue is full: drop this hop rather than spawning another
		// goroutine or blocking the request path on debug capture.
		box.queuedBytes.Add(-size)
	}
}

// trafficCaptureEnabled reports whether a sink is currently registered so the
// upstream forward path can skip body copying when the debug toggle is off.
func trafficCaptureEnabled() bool {
	box := trafficCapture.Load()
	return box != nil && box.capture != nil
}
