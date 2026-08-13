//go:build darwin

package main

import (
	proxydebugger "cursor/cursor-proxy-debugger"
	modeladapter "cursor/internal/backend/agent/model"
)

type debugProviderCapture struct {
	server *proxydebugger.Server
}

func (capture *debugProviderCapture) CaptureProvider(hop modeladapter.ProviderTrafficHop) {
	if capture == nil || capture.server == nil {
		return
	}
	capture.server.IngestProviderHop(proxydebugger.ProviderHop{
		StartedAt: hop.StartedAt, Duration: hop.Duration, Method: hop.Method, URL: hop.URL,
		Host: hop.Host, Path: hop.Path, Status: hop.Status, RequestID: hop.RequestID,
		ModelCallID: hop.ModelCallID, Provider: hop.Provider, RequestHeader: hop.RequestHeader,
		ResponseHeader: hop.ResponseHeader, RequestBody: hop.RequestBody, ResponseBody: hop.ResponseBody,
		Error: hop.Error,
	})
}
