package client

import "context"

// getAppContext returns a context for configuration operations.
// In GUI builds it returns the Wails application context;
// in CLI builds it returns context.Background().
var getAppContext = func() context.Context {
	return context.Background()
}

// emitStateEvent emits a state event to the UI.
// In GUI builds it broadcasts via Wails events;
// in CLI builds it is a no-op.
var emitStateEvent = func(name string, data any) {
	// no-op in CLI build
}
