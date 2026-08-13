package modeladapter

import "sync/atomic"

var providerTrafficCapture atomic.Pointer[providerTrafficCaptureBox]

type providerTrafficCaptureBox struct {
	capture ProviderTrafficCapture
}

// SetProviderTrafficCapture registers or clears the local-server provider traffic sink.
func SetProviderTrafficCapture(capture ProviderTrafficCapture) {
	if capture == nil {
		providerTrafficCapture.Store(nil)
		return
	}
	providerTrafficCapture.Store(&providerTrafficCaptureBox{capture: capture})
}

func emitProviderTrafficCapture(hop ProviderTrafficHop) {
	box := providerTrafficCapture.Load()
	if box == nil || box.capture == nil {
		return
	}
	go box.capture.CaptureProvider(hop)
}
