package upstream

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	"cursor/internal/backend/server"
	"cursor/internal/logger"
	legacyruntime "cursor/internal/runtime"

	"google.golang.org/protobuf/proto"
)

func MergeAvailableModelsAction(deps Dependencies, cfg CompatRouteConfig) server.HandlerFunc {
	fallback := MockProtoAction(deps, cfg)
	return func(ctx *server.Context) error {
		reqCtx, _, err := newCompatRouteObjects(ctx, deps, cfg)
		if err != nil {
			return err
		}
		if reqCtx == nil {
			return fallback(ctx)
		}
		merged, err := mergeAvailableModelsResponse(reqCtx)
		if err != nil {
			logger.Infof("available models merge fallback name=%s err=%v", cfg.Name, err)
			return fallback(ctx)
		}
		return writeProtoResponse(reqCtx, merged)
	}
}

func MergeUsableModelsAction(deps Dependencies, cfg CompatRouteConfig) server.HandlerFunc {
	fallback := MockProtoAction(deps, cfg)
	return func(ctx *server.Context) error {
		reqCtx, _, err := newCompatRouteObjects(ctx, deps, cfg)
		if err != nil {
			return err
		}
		if reqCtx == nil {
			return fallback(ctx)
		}
		merged, err := mergeUsableModelsResponse(reqCtx)
		if err != nil {
			logger.Infof("usable models merge fallback name=%s err=%v", cfg.Name, err)
			return fallback(ctx)
		}
		return writeProtoResponse(reqCtx, merged)
	}
}

func MergeDefaultModelNudgeAction(deps Dependencies, cfg CompatRouteConfig) server.HandlerFunc {
	fallback := MockProtoAction(deps, cfg)
	return func(ctx *server.Context) error {
		reqCtx, _, err := newCompatRouteObjects(ctx, deps, cfg)
		if err != nil {
			return err
		}
		if reqCtx == nil {
			return fallback(ctx)
		}
		merged, err := mergeDefaultModelNudgeResponse(reqCtx)
		if err != nil {
			logger.Infof("default model nudge merge fallback name=%s err=%v", cfg.Name, err)
			return fallback(ctx)
		}
		return writeProtoResponse(reqCtx, merged)
	}
}

func mergeAvailableModelsResponse(reqCtx *RequestContext) (*aiserverv1.AvailableModelsResponse, error) {
	adapters, err := loadConfiguredModelAdapters(reqCtx)
	if err != nil {
		return nil, err
	}
	status, headers, body, err := FetchUpstream(reqCtx, ForwardOptions{PreserveClientAuth: true})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("upstream status %d ct=%s body=%s", status, headerGet(headers, "content-type"), trimBodyForLog(body))
	}
	body = maybeHTTPDecompress(headers, body)
	upstreamModels := &aiserverv1.AvailableModelsResponse{}
	if err := decodeProtoPayload(headerGet(headers, "content-type"), body, upstreamModels); err != nil {
		return nil, fmt.Errorf("%w ct=%s ce=%s n=%d head=%x", err, headerGet(headers, "content-type"), headerGet(headers, "content-encoding"), len(body), bodyHead(body, 24))
	}
	injected, err := injectedAvailableModelProtos(adapters)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(upstreamModels.GetModels())+len(injected))
	for _, model := range upstreamModels.GetModels() {
		if model == nil {
			continue
		}
		name := strings.TrimSpace(model.GetName())
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	for _, model := range injected {
		if model == nil {
			continue
		}
		name := strings.TrimSpace(model.GetName())
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		upstreamModels.Models = append(upstreamModels.Models, model)
		seen[name] = struct{}{}
	}
	return upstreamModels, nil
}

func mergeUsableModelsResponse(reqCtx *RequestContext) (*agentv1.GetUsableModelsResponse, error) {
	adapters, err := loadConfiguredModelAdapters(reqCtx)
	if err != nil {
		return nil, err
	}
	status, headers, body, err := FetchUpstream(reqCtx, ForwardOptions{PreserveClientAuth: true})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("upstream status %d ct=%s body=%s", status, headerGet(headers, "content-type"), trimBodyForLog(body))
	}
	body = maybeHTTPDecompress(headers, body)
	upstreamModels := &agentv1.GetUsableModelsResponse{}
	if err := decodeProtoPayload(headerGet(headers, "content-type"), body, upstreamModels); err != nil {
		return nil, fmt.Errorf("%w ct=%s ce=%s n=%d head=%x", err, headerGet(headers, "content-type"), headerGet(headers, "content-encoding"), len(body), bodyHead(body, 24))
	}
	injectedJSON, err := encodeMockProto("aiserver.v1.GetUsableModelsResponse", map[string]any{
		"models": buildCLIModelDetails(adapters),
	})
	if err != nil {
		return nil, err
	}
	injected := &agentv1.GetUsableModelsResponse{}
	if err := proto.Unmarshal(injectedJSON, injected); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(upstreamModels.GetModels())+len(injected.GetModels()))
	for _, model := range upstreamModels.GetModels() {
		if model == nil {
			continue
		}
		id := strings.TrimSpace(model.GetModelId())
		if id != "" {
			seen[id] = struct{}{}
		}
	}
	for _, model := range injected.GetModels() {
		if model == nil {
			continue
		}
		id := strings.TrimSpace(model.GetModelId())
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		upstreamModels.Models = append(upstreamModels.Models, model)
		seen[id] = struct{}{}
	}
	return upstreamModels, nil
}

func mergeDefaultModelNudgeResponse(reqCtx *RequestContext) (*aiserverv1.GetDefaultModelNudgeDataResponse, error) {
	adapters, err := loadConfiguredModelAdapters(reqCtx)
	if err != nil {
		return nil, err
	}
	status, headers, body, err := FetchUpstream(reqCtx, ForwardOptions{PreserveClientAuth: true})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("upstream status %d", status)
	}
	nudge := &aiserverv1.GetDefaultModelNudgeDataResponse{}
	if err := decodeProtoPayload(headerGet(headers, "content-type"), body, nudge); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(nudge.GetModelsWithNoDefaultSwitch())+len(adapters))
	for _, modelID := range nudge.GetModelsWithNoDefaultSwitch() {
		id := strings.TrimSpace(modelID)
		if id != "" {
			seen[id] = struct{}{}
		}
	}
	for _, channelID := range collectModelAdapterRefs(adapters) {
		if _, exists := seen[channelID]; exists {
			continue
		}
		nudge.ModelsWithNoDefaultSwitch = append(nudge.ModelsWithNoDefaultSwitch, channelID)
		seen[channelID] = struct{}{}
	}
	return nudge, nil
}

func injectedAvailableModelProtos(adapters []legacyruntime.ModelAdapterConfig) ([]*aiserverv1.AvailableModelsResponse_AvailableModel, error) {
	encoded, err := encodeMockProto("aiserver.v1.AvailableModelsResponse", map[string]any{
		"models": buildAvailableModelEntries(adapters),
	})
	if err != nil {
		return nil, err
	}
	wrapper := &aiserverv1.AvailableModelsResponse{}
	if err := proto.Unmarshal(encoded, wrapper); err != nil {
		return nil, err
	}
	return wrapper.GetModels(), nil
}

func writeProtoResponse(reqCtx *RequestContext, message proto.Message) error {
	if reqCtx == nil || reqCtx.ResponseWriter == nil {
		return fmt.Errorf("response writer is nil")
	}
	body, err := proto.Marshal(message)
	if err != nil {
		return err
	}
	reqCtx.ResponseWriter.Header().Set("content-type", "application/proto")
	reqCtx.ResponseWriter.Header().Del("content-encoding")
	reqCtx.ResponseWriter.Header().Set("content-length", strconv.Itoa(len(body)))
	reqCtx.ResponseWriter.WriteHeader(http.StatusOK)
	_, err = reqCtx.ResponseWriter.Write(body)
	return err
}

func headerGet(headers http.Header, key string) string {
	if headers == nil {
		return ""
	}
	return strings.TrimSpace(headers.Get(key))
}
