// Package server implements the Cursor Tab Connect endpoints over HTTP.
package server

import (
	"bytes"
	"io"
	"net/http"
	"time"

	"github.com/leookun/cursor-byok/tab-server/src/config"
	"github.com/leookun/cursor-byok/tab-server/src/proto"
	"github.com/leookun/cursor-byok/tab-server/src/tab"
)

const (
	contentTypeProto = "application/proto"
	streamingPath1   = "/aiserver.v1.AiService/StreamCpp"
	streamingPath2   = "/aiserver.v1.AiService/StreamNextCursorPrediction"
)

// Server routes Tab methods to the handler.
type Server struct {
	handler *tab.Handler
	config  config.Config
}

// New builds a Server.
func New(cfg config.Config) *Server {
	return &Server{handler: tab.NewHandler(cfg), config: cfg}
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	started := time.Now()
	if request.Method != http.MethodPost {
		http.NotFound(writer, request)
		return
	}
	path := request.URL.Path
	handler, ok := s.route(path)
	if !ok {
		http.NotFound(writer, request)
		return
	}
	payload, err := readPayload(request)
	if err != nil {
		s.log("%s 读取请求体失败: %v", path, err)
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	handler(writer, request, path, payload, started)
}

// readPayload reads the body and unwraps the Connect unary envelope.
func readPayload(request *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(request.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return proto.DecodePayload(body)
}

type methodHandler func(http.ResponseWriter, *http.Request, string, []byte, time.Time)

func (s *Server) route(path string) (methodHandler, bool) {
	switch path {
	case streamingPath1:
		return s.streamCpp, true
	case streamingPath2:
		return s.streamNextCursorPrediction, true
	case "/aiserver.v1.AiService/CppConfig":
		return s.unary(s.cppConfig), true
	case "/aiserver.v1.AiService/CppEditHistoryStatus":
		return s.unary(s.cppEditHistoryStatus), true
	case "/aiserver.v1.AiService/CppAppend":
		return s.unary(s.cppAppend), true
	case "/aiserver.v1.AiService/CppEditHistoryAppend":
		return s.unary(s.cppEditHistoryAppend), true
	case "/aiserver.v1.AiService/RefreshTabContext":
		return s.unary(s.refreshTabContext), true
	case "/aiserver.v1.AiService/GetCppEditClassification":
		return s.unary(s.getCppEditClassification), true
	case "/aiserver.v1.CppService/AvailableModels":
		return s.unary(s.availableModels), true
	case "/aiserver.v1.CppService/RecordCppFate":
		return s.unary(s.recordCppFate), true
	case "/aiserver.v1.AiService/ReportAiCodeChangeMetrics":
		return s.unary(s.reportAiCodeChangeMetrics), true
	case "/aiserver.v1.FileSyncService/FSIsEnabledForUser":
		return s.unary(s.fsIsEnabledForUser), true
	case "/aiserver.v1.FileSyncService/FSConfig":
		return s.unary(s.fsConfig), true
	case "/aiserver.v1.FileSyncService/FSSyncFile":
		return s.unary(s.fsSyncFile), true
	case "/aiserver.v1.FileSyncService/FSUploadFile":
		return s.unary(s.fsUploadFile), true
	case "/aiserver.v1.AiService/WriteGitCommitMessage":
		return s.unary(s.writeGitCommitMessage), true
	case "/aiserver.v1.AiService/WriteGitBranchName":
		return s.unary(s.writeGitBranchName), true
	default:
		return nil, false
	}
}

// unary wraps a payload handler with Connect unary response framing.
func (s *Server) unary(handle func([]byte) ([]byte, error)) methodHandler {
	return func(writer http.ResponseWriter, request *http.Request, path string, payload []byte, started time.Time) {
		encoded, err := handle(payload)
		if err != nil {
			s.log("%s 处理失败: %v", path, err)
			http.Error(writer, err.Error(), http.StatusBadGateway)
			return
		}
		writer.Header().Set("Content-Type", contentTypeProto)
		writer.Header().Set("Content-Length", itoa(len(encoded)))
		writer.WriteHeader(http.StatusOK)
		writer.Write(encoded)
		s.log("%s ok bytes=%d elapsed=%s", path, len(encoded), time.Since(started).Round(time.Millisecond))
	}
}

// streamCpp serves inline completion as a Connect server stream.
func (s *Server) streamCpp(writer http.ResponseWriter, request *http.Request, path string, payload []byte, started time.Time) {
	decoded, err := proto.DecodeStreamCppRequest(payload)
	if err != nil {
		s.log("%s 解码失败: %v", path, err)
		s.writeStreamError(writer, proto.Internal(err))
		return
	}
	flusher, flushable := writer.(http.Flusher)
	writer.Header().Set("Content-Type", contentTypeProto)
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("connect-protocol-version", "1")
	writer.Header().Del("Content-Length")
	writer.WriteHeader(http.StatusOK)
	if flushable {
		flusher.Flush()
	}
	content, err := s.handler.Complete(decoded, func(chunk string) {
		if chunk == "" {
			return
		}
		frame := proto.EncodeMessage(proto.EncodeStreamCppResponse(&proto.StreamCppResponse{Text: chunk}))
		writer.Write(frame)
		if flushable {
			flusher.Flush()
		}
	})
	if err != nil {
		s.writeStreamError(writer, proto.Unavailable(err))
		s.log("%s 上游失败: %v", path, err)
		return
	}
	done := true
	writer.Write(proto.EncodeMessage(proto.EncodeStreamCppResponse(&proto.StreamCppResponse{
		Text:       "",
		DoneStream: &done,
	})))
	writer.Write(proto.EncodeEndStream())
	if flushable {
		flusher.Flush()
	}
	s.log("%s ok chars=%d elapsed=%s", path, len(content), time.Since(started).Round(time.Millisecond))
}

// streamNextCursorPrediction serves the jump-to-next-edit prediction.
func (s *Server) streamNextCursorPrediction(writer http.ResponseWriter, request *http.Request, path string, payload []byte, started time.Time) {
	decoded, err := proto.DecodeStreamNextCursorPredictionRequest(payload)
	if err != nil {
		s.log("%s 解码失败: %v", path, err)
		s.writeStreamError(writer, proto.Internal(err))
		return
	}
	flusher, flushable := writer.(http.Flusher)
	writer.Header().Set("Content-Type", contentTypeProto)
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("connect-protocol-version", "1")
	writer.Header().Del("Content-Length")
	writer.WriteHeader(http.StatusOK)
	if flushable {
		flusher.Flush()
	}
	response, err := s.handler.NextEdit(decoded)
	if err != nil {
		s.writeStreamError(writer, proto.Unavailable(err))
		s.log("%s 上游失败: %v", path, err)
		return
	}
	writer.Write(proto.EncodeMessage(proto.EncodeStreamNextCursorPredictionResponse(response)))
	writer.Write(proto.EncodeEndStream())
	if flushable {
		flusher.Flush()
	}
	s.log("%s ok elapsed=%s", path, time.Since(started).Round(time.Millisecond))
}

// writeStreamError appends a terminal error frame. It can only be used before
// any message frame has been written, because Connect sends errors in the
// terminal frame rather than the HTTP status.
func (s *Server) writeStreamError(writer http.ResponseWriter, err error) {
	flusher, flushable := writer.(http.Flusher)
	writer.Header().Set("Content-Type", contentTypeProto)
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("connect-protocol-version", "1")
	writer.Header().Del("Content-Length")
	writer.WriteHeader(http.StatusOK)
	writer.Write(proto.EncodeErrorEndStream(err))
	if flushable {
		flusher.Flush()
	}
}

// cppConfig reports Tab as enabled and configured for this server.
func (s *Server) cppConfig(payload []byte) ([]byte, error) {
	above := int32(80)
	below := int32(80)
	on := true
	ghost := true
	return proto.EncodeCppConfigResponse(&proto.CppConfigResponse{
		AboveRadius:                  &above,
		BelowRadius:                  &below,
		IsOn:                         &on,
		IsGhostText:                  &ghost,
		GlobalDebounceDurationMillis: 150,
		ClientDebounceDurationMillis: 100,
		CppURL:                       "",
		UseWhitespaceDiffHistory:     true,
		AllowsTabChunks:              true,
	}), nil
}

func (s *Server) cppEditHistoryStatus(payload []byte) ([]byte, error) {
	return proto.EncodeCppEditHistoryStatusResponse(&proto.CppEditHistoryStatusResponse{
		On:             true,
		OnlyIfExplicit: false,
	}), nil
}

func (s *Server) cppAppend(payload []byte) ([]byte, error) {
	return proto.EncodeCppAppendResponse(&proto.CppAppendResponse{Success: true}), nil
}

func (s *Server) cppEditHistoryAppend(payload []byte) ([]byte, error) {
	return proto.EncodeEditHistoryAppendChangesResponse(&proto.EditHistoryAppendChangesResponse{Success: true}), nil
}

func (s *Server) refreshTabContext(payload []byte) ([]byte, error) {
	return proto.EncodeRefreshTabContextResponse(&proto.RefreshTabContextResponse{}), nil
}

// getCppEditClassification declines to rank candidate edits. The client falls
// back to its own ordering, which is correct without a trained reranker.
func (s *Server) getCppEditClassification(payload []byte) ([]byte, error) {
	shouldNoop := false
	return proto.EncodeGetCppEditClassificationResponse(&proto.GetCppEditClassificationResponse{
		ShouldNoop: &shouldNoop,
	}), nil
}

func (s *Server) availableModels(payload []byte) ([]byte, error) {
	model := s.config.Upstream.Model
	return proto.EncodeAvailableCppModelsResponse(&proto.AvailableCppModelsResponse{
		Models:       []string{model},
		DefaultModel: &model,
	}), nil
}

func (s *Server) recordCppFate(payload []byte) ([]byte, error) {
	return proto.EncodeRecordCppFateResponse(&proto.RecordCppFateResponse{}), nil
}

func (s *Server) reportAiCodeChangeMetrics(payload []byte) ([]byte, error) {
	return proto.EncodeReportAiCodeChangeMetricsResponse(&proto.ReportAiCodeChangeMetricsResponse{}), nil
}

// fsIsEnabledForUser disables FileSync: this server has no remote file mirror.
func (s *Server) fsIsEnabledForUser(payload []byte) ([]byte, error) {
	return proto.EncodeFSIsEnabledForUserResponse(&proto.FSIsEnabledForUserResponse{Enabled: false}), nil
}

func (s *Server) fsConfig(payload []byte) ([]byte, error) {
	updates := int32(0)
	cacheSize := int32(0)
	fileSize := int32(0)
	attempts := int32(0)
	delay := int32(0)
	multiplier := int32(0)
	statusCache := int32(0)
	required := int32(0)
	return proto.EncodeFSConfigResponse(&proto.FSConfigResponse{
		CheckFilesyncHashPercent:           0,
		MaxRecentUpdatesStored:             &updates,
		MaxModelVersionCacheSize:           &cacheSize,
		MaxFileSizeToSyncBytes:             &fileSize,
		SyncRetryMaxAttempts:               &attempts,
		SyncRetryInitialDelayMs:            &delay,
		SyncRetryTimeMultiplier:            &multiplier,
		FileSyncStatusMaxCacheSize:         &statusCache,
		SuccessiveSyncsRequiredForReliance: &required,
	}), nil
}

func (s *Server) fsSyncFile(payload []byte) ([]byte, error) {
	return proto.EncodeFSSyncFileResponse(&proto.FSSyncFileResponse{Error: 0}), nil
}

func (s *Server) fsUploadFile(payload []byte) ([]byte, error) {
	return proto.EncodeFSUploadFileResponse(&proto.FSUploadFileResponse{Error: 0}), nil
}

func (s *Server) writeGitCommitMessage(payload []byte) ([]byte, error) {
	return s.handler.WriteGitCommitMessage(payload)
}

func (s *Server) writeGitBranchName(payload []byte) ([]byte, error) {
	return s.handler.WriteGitBranchName(payload)
}

// log writes a request line when logging is enabled.
func (s *Server) log(format string, args ...interface{}) {
	if !s.config.Server.LogEnabled {
		return
	}
	printLog(format, args...)
}

// healthz reports readiness.
func (s *Server) healthz(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	writer.Write([]byte("ok"))
}

// Routes registers health plus all Tab methods.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.healthz)
	mux.Handle("/", s)
	return mux
}

// itoa avoids importing strconv for a single conversion site.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var buffer bytes.Buffer
	negative := value < 0
	if negative {
		value = -value
	}
	for value > 0 {
		buffer.WriteByte(byte('0' + value%10))
		value /= 10
	}
	digits := buffer.Bytes()
	reverse(digits)
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

func reverse(values []byte) {
	for i, j := 0, len(values)-1; i < j; i, j = i+1, j-1 {
		values[i], values[j] = values[j], values[i]
	}
}
