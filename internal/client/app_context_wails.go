//go:build !cli

package client

import (
	"context"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func init() {
	getAppContext = func() context.Context {
		if app := application.Get(); app != nil {
			return app.Context()
		}
		return context.Background()
	}

	emitStateEvent = func(name string, data any) {
		if app := application.Get(); app != nil {
			app.Event.Emit(name, data)
		}
	}
}
