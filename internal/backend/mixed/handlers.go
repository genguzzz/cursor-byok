// Package mixed 在解密之后选择 local backend 或官方 upstream。
//
// CONNECT/MITM 层看不到 model_id，CLI --backend-only 也没有 18080。
// 因此路由核心挂在 18090 HTTP 入口，本地 forwarder 仍只负责「永远本地」的 handler。
// 桌面 MITM 继续只做解密并转发到本入口；日后若要在代理进程分流，应复用这里的策略函数。
package mixed

import (
	"net/http"
	"strings"

	"cursor/internal/backend/server"
	"cursor/internal/backend/server/upstream"
)

func Catalog(enabled bool, deps upstream.Dependencies, cfg upstream.CompatRouteConfig, merge func(upstream.Dependencies, upstream.CompatRouteConfig) server.HandlerFunc) server.HandlerFunc {
	if enabled {
		return merge(deps, cfg)
	}
	return upstream.MockProtoAction(deps, cfg)
}

func ForwardOrMock(enabled bool, deps upstream.Dependencies, cfg upstream.CompatRouteConfig) server.HandlerFunc {
	if enabled {
		return upstream.ClientAuthForwardOrMockAction(deps, cfg)
	}
	return upstream.MockProtoAction(deps, cfg)
}

func ForwardOrNotFound(enabled bool, deps upstream.Dependencies, name string) server.HandlerFunc {
	if enabled {
		return upstream.ClientAuthForwardAction(deps, upstream.CompatRouteConfig{Name: name})
	}
	return func(ctx *server.Context) error {
		http.NotFound(ctx.Writer, ctx.Request)
		return nil
	}
}

func ForwardOrOAuth(enabled bool, deps upstream.Dependencies, cfg upstream.CompatRouteConfig) server.HandlerFunc {
	if enabled {
		return upstream.ClientAuthForwardAction(deps, cfg)
	}
	return upstream.MockOAuthAction(deps, cfg)
}

func ForwardOrAuthEmail(enabled bool, deps upstream.Dependencies, cfg upstream.CompatRouteConfig) server.HandlerFunc {
	if enabled {
		return upstream.ClientAuthForwardAction(deps, cfg)
	}
	return upstream.MockAuthEmailAction(deps, cfg)
}

func ForwardOrAuthFullStripe(enabled bool, deps upstream.Dependencies, cfg upstream.CompatRouteConfig) server.HandlerFunc {
	if enabled {
		return upstream.ClientAuthForwardAction(deps, cfg)
	}
	return upstream.MockAuthFullStripeProfileAction(deps, cfg)
}

func ForwardOrAuthStripe(enabled bool, deps upstream.Dependencies, cfg upstream.CompatRouteConfig) server.HandlerFunc {
	if enabled {
		return upstream.ClientAuthForwardAction(deps, cfg)
	}
	return upstream.MockAuthStripeProfileAction(deps, cfg)
}

func ForwardOrJSON(enabled bool, deps upstream.Dependencies, cfg upstream.CompatRouteConfig) server.HandlerFunc {
	if enabled {
		return upstream.ClientAuthForwardAction(deps, cfg)
	}
	return upstream.MockJSONAction(deps, cfg)
}

func ForwardOrAuthPoll(enabled bool, deps upstream.Dependencies, cfg upstream.CompatRouteConfig) server.HandlerFunc {
	if enabled {
		return upstream.ClientAuthForwardAction(deps, cfg)
	}
	return upstream.MockAuthPollAction(deps, cfg)
}

func ForwardOrFixed(enabled bool, deps upstream.Dependencies, cfg upstream.CompatRouteConfig) server.HandlerFunc {
	if enabled {
		return upstream.ClientAuthForwardAction(deps, cfg)
	}
	return upstream.FixedStatusAction(deps, cfg)
}

// AIServiceCatchAll mixed 开启时：白名单本地，其余 ClientAuth 回源。
// tabRenamerEnabled 热读 features.tabRenamer.enabled；nil 视为 false。
// NameTab/NameAgent 仅在本地 tabRenamer 开启时走 local，否则回源官方命名服务。
func AIServiceCatchAll(enabled bool, local http.Handler, deps upstream.Dependencies, tabRenamerEnabled func() bool) server.HandlerFunc {
	if !enabled {
		return server.HTTPHandlerAction(local)
	}
	if tabRenamerEnabled == nil {
		tabRenamerEnabled = func() bool { return false }
	}
	forward := upstream.ClientAuthForwardAction(deps, upstream.CompatRouteConfig{Name: "ai_service_passthrough"})
	return func(ctx *server.Context) error {
		path := ""
		if ctx != nil && ctx.Request != nil && ctx.Request.URL != nil {
			path = ctx.Request.URL.Path
		}
		if IsLocalAiServicePath(path, tabRenamerEnabled()) {
			return server.HTTPHandlerAction(local)(ctx)
		}
		return forward(ctx)
	}
}

// IsLocalAiServicePath 判断 AiService catch-all 是否应留在本地。
// tabRenamerEnabled 为 false 时，NameTab/NameAgent 不视为本地，交给 upstream。
func IsLocalAiServicePath(path string, tabRenamerEnabled bool) bool {
	switch strings.TrimSpace(path) {
	case "/aiserver.v1.AiService/CountTokens",
		"/aiserver.v1.AiService/GetThoughtAnnotation",
		"/aiserver.v1.AiService/WriteGitCommitMessage",
		"/aiserver.v1.AiService/CreateExperimentalIndex",
		"/aiserver.v1.AiService/ListExperimentalIndexFiles",
		"/aiserver.v1.AiService/ListenExperimentalIndex",
		"/aiserver.v1.AiService/RegisterFileToIndex",
		"/aiserver.v1.AiService/SetupIndexDependencies",
		"/aiserver.v1.AiService/ComputeIndexTopoSort",
		"/aiserver.v1.AiService/DocumentationQuery",
		"/aiserver.v1.AiService/AvailableDocs",
		"/aiserver.v1.AiService/KnowledgeBaseAdd",
		"/aiserver.v1.AiService/KnowledgeBaseList",
		"/aiserver.v1.AiService/KnowledgeBaseRemove",
		"/aiserver.v1.AiService/KnowledgeBaseUpdate",
		"/aiserver.v1.AiService/FetchRelevantKnowledgeForConversation":
		return true
	case "/aiserver.v1.AiService/NameTab",
		"/aiserver.v1.AiService/NameAgent":
		return tabRenamerEnabled
	default:
		return false
	}
}
