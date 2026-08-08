package upstream

import (
	"net/http"
	"sync/atomic"
	"time"
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
	capture TrafficCapture
}

// SetTrafficCapture 注册或清空（传 nil）回源抓包 sink。
func SetTrafficCapture(capture TrafficCapture) {
	if capture == nil {
		trafficCapture.Store(nil)
		return
	}
	trafficCapture.Store(&trafficCaptureBox{capture: capture})
}

func emitTrafficCapture(hop TrafficHop) {
	box := trafficCapture.Load()
	if box == nil || box.capture == nil {
		return
	}
	go box.capture.CaptureUpstream(hop)
}
