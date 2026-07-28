// module.go 负责把 forwarder service 装配成 legacy HTTP/Connect handler。
package forwarder

import (
	"net/http"

	"connectrpc.com/connect"

	modeladapter "cursor/internal/backend/agent/model"
	serverconfig "cursor/internal/backend/server/config"
)

type Module struct {
	Service                  *Service
	LocalBidiHandler         http.Handler
	LocalRunSSE              http.Handler
	AiHandler                http.Handler
	RepositoryServiceHandler http.Handler
	UploadServiceHandler     http.Handler
}

// NewModule 创建 forwarder 模块，并导出本地 Bidi / RunSSE 处理器。
func NewModule(historyRoot string, channelService modeladapter.ChannelResolver) *Module {
	return NewModuleWithConfig(historyRoot, channelService, nil)
}

// NewModuleWithConfig 在 NewModule 基础上注入 yaml 配置加载器。
// 传入 nil 时 TabRenamer 维持 disabled 状态（按上一版默认行为）。
func NewModuleWithConfig(historyRoot string, channelService modeladapter.ChannelResolver, configLoader func() serverconfig.Config) *Module {
	service := NewService(historyRoot, channelService)
	if configLoader != nil {
		service.tabRenamerConfigLoader = func() serverconfig.TabRenamerConfig {
			return configLoader().Features.TabRenamer
		}
		service.tabRenamerFullConfigLoader = configLoader
	}
	legacyBidiAppendProcedure := "/aiserver.v1.BidiService/BidiAppend"
	legacyRunSSEProcedure := "/agent.v1.AgentService/RunSSE"
	return &Module{
		Service:                  service,
		LocalBidiHandler:         connect.NewUnaryHandler(legacyBidiAppendProcedure, service.BidiAppend),
		LocalRunSSE:              NewLegacyRunSSEHandler(legacyRunSSEProcedure, service.RunSSE),
		AiHandler:                newAIHandler(service),
		RepositoryServiceHandler: newRepositoryServiceHandler(service),
		UploadServiceHandler:     newUploadServiceHandler(service),
	}
}
