// retry.go 保留 provider HTTP 请求入口的历史命名；provider 错误交给客户端重连链路处理。
package modeladapter

import (
	"context"
	"io"
	"net/http"
	"time"
)

// DoProviderRequestWithRetry 保留旧入口名；本地模式不在服务端重试 provider 请求。
func DoProviderRequestWithRetry(
	ctx context.Context,
	client *http.Client,
	provider string,
	requestID string,
	modelCallID string,
	buildRequest func(context.Context) (*http.Request, error),
) (*http.Response, error) {
	return doProviderRequestWithRetry(ctx, client, provider, requestID, modelCallID, buildRequest)
}

func doProviderRequestWithRetry(
	ctx context.Context,
	client *http.Client,
	provider string,
	requestID string,
	modelCallID string,
	buildRequest func(context.Context) (*http.Request, error),
) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	startedAt := time.Now().UTC()
	httpReq, err := buildRequest(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if providerTrafficCaptureEnabled() {
			emitProviderTrafficCapture(ProviderTrafficHop{
				StartedAt:     startedAt,
				Duration:      time.Since(startedAt),
				Method:        httpReq.Method,
				URL:           httpReq.URL.String(),
				Host:          httpReq.URL.Host,
				Path:          httpReq.URL.Path,
				RequestID:     requestID,
				ModelCallID:   modelCallID,
				Provider:      provider,
				RequestHeader: cloneHeader(httpReq.Header),
				Error:         err.Error(),
			})
		}
		return nil, err
	}
	// Skip the hop build, request-body read, and response-body wrapping when no
	// capture sink is registered. Otherwise every LLM request would copy its
	// full request body and buffer up to 16 MiB of the response stream even with
	// the debug toggle off, which is exactly the always-on capture overhead this
	// path is meant to avoid.
	if !providerTrafficCaptureEnabled() {
		return resp, nil
	}
	hop := ProviderTrafficHop{
		StartedAt:      startedAt,
		Method:         httpReq.Method,
		URL:            httpReq.URL.String(),
		Host:           httpReq.URL.Host,
		Path:           httpReq.URL.Path,
		RequestID:      requestID,
		ModelCallID:    modelCallID,
		Provider:       provider,
		Status:         resp.StatusCode,
		RequestHeader:  cloneHeader(httpReq.Header),
		ResponseHeader: cloneHeader(resp.Header),
	}
	if httpReq.GetBody != nil {
		if body, bodyErr := httpReq.GetBody(); bodyErr == nil {
			hop.RequestBody, _ = io.ReadAll(io.LimitReader(body, providerTrafficCaptureLimit))
			_ = body.Close()
		}
	}
	resp.Body = &providerResponseCapture{
		ReadCloser: resp.Body,
		hop:        hop,
	}
	return resp, nil
}

// ProviderRetryAttemptSummary 返回空值；provider 请求不再有服务端内部重试摘要。
func ProviderRetryAttemptSummary(resp *http.Response) string {
	return ""
}
