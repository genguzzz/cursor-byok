package proxydebugger

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	agentprotocol "cursor/internal/backend/agent/protocol"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const maxConnectFrameBytes = 64 << 20

const (
	bidiAppendPath              = "/aiserver.v1.BidiService/BidiAppend"
	runSSEPath                  = "/agent.v1.AgentService/RunSSE"
	notifyConversationClonePath = "/agent.v1.AgentService/NotifyConversationClone"
	uploadConversationBlobsPath = "/agent.v1.AgentService/UploadConversationBlobs"

	runSSERequestMessageType  = "aiserver.v1.BidiRequestId"
	runSSEResponseMessageType = "agent.v1.AgentServerMessage"
)

type connectFrameDecoder struct {
	buffer      []byte
	messageType string
	codec       string
	maxFrames   int
	frameCount  int
	onFrame     func(FrameView)
}

func newConnectFrameDecoder(messageType string, codec string, maxFrames int, onFrame func(FrameView)) *connectFrameDecoder {
	return &connectFrameDecoder{
		messageType: messageType,
		codec:       strings.TrimSpace(codec),
		maxFrames:   maxFrames,
		onFrame:     onFrame,
	}
}

func (decoder *connectFrameDecoder) Write(payload []byte) {
	if len(payload) == 0 || decoder.frameCount >= decoder.maxFrames {
		return
	}
	decoder.buffer = append(decoder.buffer, payload...)
	for len(decoder.buffer) >= 5 && decoder.frameCount < decoder.maxFrames {
		flags := decoder.buffer[0]
		length := int(binary.BigEndian.Uint32(decoder.buffer[1:5]))
		if length < 0 || length > maxConnectFrameBytes {
			decoder.emit(FrameView{Flags: flags, Length: length, Error: "Connect 帧长度异常"})
			decoder.buffer = nil
			return
		}
		if len(decoder.buffer) < 5+length {
			return
		}
		framePayload := append([]byte(nil), decoder.buffer[5:5+length]...)
		decoder.buffer = decoder.buffer[5+length:]
		decoder.emit(decoder.decode(flags, framePayload))
	}
}

func (decoder *connectFrameDecoder) Close() {
	if len(decoder.buffer) > 0 && decoder.frameCount < decoder.maxFrames {
		decoder.emit(FrameView{
			Length: len(decoder.buffer),
			RawHex: clippedHex(decoder.buffer, 4096),
			Error:  "流结束时仍有不完整的 Connect 帧",
		})
	}
	decoder.buffer = nil
}

func (decoder *connectFrameDecoder) emit(frame FrameView) {
	frame.Index = decoder.frameCount
	decoder.frameCount++
	if decoder.onFrame != nil {
		decoder.onFrame(frame)
	}
}

func (decoder *connectFrameDecoder) decode(flags uint8, payload []byte) FrameView {
	frame := FrameView{
		Flags:      flags,
		Length:     len(payload),
		Compressed: flags&0x01 != 0,
		EndStream:  flags&0x02 != 0,
	}
	decoded := payload
	if frame.Compressed {
		var err error
		decoded, err = decompressPayload(payload, decoder.codec)
		if err != nil {
			frame.Error = err.Error()
			frame.RawHex = clippedHex(payload, 4096)
			return frame
		}
	}
	if frame.EndStream {
		frame.Kind = "end_stream"
		frame.MessageType = "connect.error.v1.EndStreamResponse"
		frame.JSON = prettyJSON(decoded)
		return frame
	}

	message := newMessage(decoder.messageType)
	if message == nil {
		frame.Error = "未知的 protobuf 消息类型"
		frame.RawHex = clippedHex(payload, 4096)
		return frame
	}
	if err := proto.Unmarshal(decoded, message); err != nil {
		frame.Error = fmt.Sprintf("protobuf 解码失败：%v", err)
		frame.RawHex = clippedHex(payload, 4096)
		return frame
	}
	frame.MessageType = decoder.messageType
	frame.Kind = activeOneofName(message)
	if requestID, ok := message.(*aiserverv1.BidiRequestId); ok {
		frame.RequestID = strings.TrimSpace(requestID.GetRequestId())
	}
	frame.JSON = marshalProtoJSON(message)
	return frame
}

func decompressPayload(payload []byte, codec string) ([]byte, error) {
	if codec != "" && !strings.EqualFold(codec, "gzip") {
		return nil, fmt.Errorf("暂不支持压缩算法 %q", codec)
	}
	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("gzip 解压失败：%w", err)
	}
	defer reader.Close()
	decoded, err := io.ReadAll(io.LimitReader(reader, maxConnectFrameBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取 gzip 内容失败：%w", err)
	}
	if len(decoded) > maxConnectFrameBytes {
		return nil, fmt.Errorf("gzip 解压后超过 %d 字节限制", maxConnectFrameBytes)
	}
	return decoded, nil
}

// decodeConnectFramesOffline 把一整段 Connect 流拆成帧并解码（用于官方 upstream 整包回灌）。
// MITM 流式路径已在抓包时增量解码；offline 仅在 Frames 仍为空时使用。
func decodeConnectFramesOffline(messageType, codec string, maxFrames int, payload []byte) []FrameView {
	if len(payload) == 0 || maxFrames <= 0 {
		return nil
	}
	frames := make([]FrameView, 0, 16)
	decoder := newConnectFrameDecoder(messageType, codec, maxFrames, func(frame FrameView) {
		frames = append(frames, frame)
	})
	decoder.Write(payload)
	decoder.Close()
	return frames
}

func runSSEMessageType(path string, responseSide bool) string {
	if path != runSSEPath {
		return ""
	}
	if responseSide {
		return runSSEResponseMessageType
	}
	return runSSERequestMessageType
}

func firstNonEmptyFrameKind(frames []FrameView) string {
	for i := len(frames) - 1; i >= 0; i-- {
		kind := strings.TrimSpace(frames[i].Kind)
		if kind != "" && kind != "end_stream" {
			return kind
		}
	}
	return ""
}

// maybeUnwrapConnectUnary peels a single Connect unary envelope (5-byte header + body).
// Returns the original payload when it is not exactly one envelope.
func maybeUnwrapConnectUnary(payload []byte, codec string) ([]byte, error) {
	if len(payload) < 5 {
		return payload, nil
	}
	flags := payload[0]
	length := int(binary.BigEndian.Uint32(payload[1:5]))
	if length < 0 || length > maxConnectFrameBytes || 5+length != len(payload) {
		return payload, nil
	}
	body := payload[5 : 5+length]
	if flags&0x01 == 0 {
		return body, nil
	}
	unwrapCodec := strings.TrimSpace(codec)
	if unwrapCodec == "" || strings.EqualFold(unwrapCodec, "identity") {
		unwrapCodec = "gzip"
	}
	return decompressPayload(body, unwrapCodec)
}

func syntheticUnaryFrame(path, kind, requestID, decodedJSON string, length int) FrameView {
	messageType := "application/proto"
	switch path {
	case bidiAppendPath:
		messageType = "aiserver.v1.BidiAppendRequest+agent.v1.AgentClientMessage"
	case notifyConversationClonePath:
		messageType = "agent.v1.NotifyConversationCloneRequest"
	case uploadConversationBlobsPath:
		messageType = "agent.v1.UploadConversationBlobsRequest"
	default:
		if path != "" {
			messageType = strings.TrimPrefix(path, "/") + "#unary"
		}
	}
	if kind == "" {
		kind = "unary_message"
	}
	return FrameView{
		Index:       0,
		Length:      length,
		Kind:        kind,
		MessageType: messageType,
		RequestID:   requestID,
		JSON:        decodedJSON,
	}
}

func decodeUnaryRequest(path string, payload []byte) (decodedJSON string, kind string, requestID string, err error) {
	switch path {
	case bidiAppendPath:
		request := &aiserverv1.BidiAppendRequest{}
		if err := proto.Unmarshal(payload, request); err != nil {
			return "", "", "", err
		}
		requestID := strings.TrimSpace(request.GetRequestId().GetRequestId())
		outer := marshalProtoJSON(request)
		clientMessage, clientKind, decodeErr := agentprotocol.DecodeAgentClientMessage(request.GetData())
		if decodeErr != nil || clientMessage == nil {
			return outer, "bidi_append", requestID, decodeErr
		}
		combined := struct {
			BidiAppendRequest json.RawMessage `json:"bidi_append_request"`
			AgentClientKind   string          `json:"agent_client_kind"`
			AgentClient       json.RawMessage `json:"agent_client_message"`
		}{
			BidiAppendRequest: json.RawMessage(outer),
			AgentClientKind:   clientKind,
			AgentClient:       json.RawMessage(marshalProtoJSON(clientMessage)),
		}
		formatted, marshalErr := json.MarshalIndent(combined, "", "  ")
		return string(formatted), clientKind, requestID, marshalErr
	}
	message, kind := unaryRequestMessage(path)
	if message == nil {
		return "", "", "", nil
	}
	if err := proto.Unmarshal(payload, message); err != nil {
		return "", "", "", err
	}
	return marshalProtoJSON(message), kind, "", nil
}

func decodeUnaryResponse(path string, payload []byte) (decodedJSON string, kind string, err error) {
	message, kind := unaryResponseMessage(path)
	if message == nil {
		return "", "", nil
	}
	if err := proto.Unmarshal(payload, message); err != nil {
		return "", "", err
	}
	return marshalProtoJSON(message), kind, nil
}

func unaryRequestMessage(path string) (proto.Message, string) {
	switch path {
	case notifyConversationClonePath:
		return &agentv1.NotifyConversationCloneRequest{}, "notify_conversation_clone_request"
	case uploadConversationBlobsPath:
		return &agentv1.UploadConversationBlobsRequest{}, "upload_conversation_blobs_request"
	// Common Cursor aiserver unary RPCs (application/proto, not Connect streams).
	case "/aiserver.v1.AiService/ServerTime":
		return &aiserverv1.ServerTimeRequest{}, "server_time_request"
	case "/aiserver.v1.AnalyticsService/SubmitLogs":
		return &aiserverv1.SubmitLogsRequest{}, "submit_logs_request"
	case "/aiserver.v1.AnalyticsService/Batch":
		return &aiserverv1.BatchRequest{}, "analytics_batch_request"
	case "/aiserver.v1.DashboardService/GetUsageLimitStatusAndActiveGrants":
		return &aiserverv1.GetUsageLimitStatusAndActiveGrantsRequest{}, "get_usage_limit_status_request"
	case "/aiserver.v1.DashboardService/GetTeamAdminSettingsOrEmptyIfNotInTeam":
		return &aiserverv1.GetTeamAdminSettingsRequest{}, "get_team_admin_settings_request"
	case "/aiserver.v1.RepositoryService/FastRepoInitHandshakeV2":
		return &aiserverv1.FastRepoInitHandshakeV2Request{}, "fast_repo_init_handshake_v2_request"
	case "/aiserver.v1.RepositoryService/FastRepoSyncComplete":
		return &aiserverv1.FastRepoSyncCompleteRequest{}, "fast_repo_sync_complete_request"
	case "/aiserver.v1.BackgroundComposerService/GetGithubAccessTokenForRepos":
		return &aiserverv1.GetGithubAccessTokenForReposRequest{}, "get_github_access_token_for_repos_request"
	case "/aiserver.v1.BackgroundComposerService/ListPersonalEnvironments":
		return &aiserverv1.ListPersonalEnvironmentsRequest{}, "list_personal_environments_request"
	case "/aiserver.v1.BackgroundComposerService/ListTeamEnvironments":
		return &aiserverv1.ListTeamEnvironmentsRequest{}, "list_team_environments_request"
	default:
		return nil, ""
	}
}

func unaryResponseMessage(path string) (proto.Message, string) {
	switch path {
	case notifyConversationClonePath:
		return &agentv1.NotifyConversationCloneResponse{}, "notify_conversation_clone_response"
	case uploadConversationBlobsPath:
		return &agentv1.UploadConversationBlobsResponse{}, "upload_conversation_blobs_response"
	case "/aiserver.v1.AiService/ServerTime":
		return &aiserverv1.ServerTimeResponse{}, "server_time_response"
	case "/aiserver.v1.AnalyticsService/SubmitLogs":
		return &aiserverv1.SubmitLogsResponse{}, "submit_logs_response"
	case "/aiserver.v1.AnalyticsService/Batch":
		return &aiserverv1.BatchResponse{}, "analytics_batch_response"
	case "/aiserver.v1.DashboardService/GetUsageLimitStatusAndActiveGrants":
		return &aiserverv1.GetUsageLimitStatusAndActiveGrantsResponse{}, "get_usage_limit_status_response"
	case "/aiserver.v1.DashboardService/GetTeamAdminSettingsOrEmptyIfNotInTeam":
		return &aiserverv1.GetTeamAdminSettingsResponse{}, "get_team_admin_settings_response"
	case "/aiserver.v1.RepositoryService/FastRepoInitHandshakeV2":
		return &aiserverv1.FastRepoInitHandshakeV2Response{}, "fast_repo_init_handshake_v2_response"
	case "/aiserver.v1.RepositoryService/FastRepoSyncComplete":
		return &aiserverv1.FastRepoSyncCompleteResponse{}, "fast_repo_sync_complete_response"
	case "/aiserver.v1.BackgroundComposerService/GetGithubAccessTokenForRepos":
		return &aiserverv1.GetGithubAccessTokenForReposResponse{}, "get_github_access_token_for_repos_response"
	case "/aiserver.v1.BackgroundComposerService/ListPersonalEnvironments":
		return &aiserverv1.ListPersonalEnvironmentsResponse{}, "list_personal_environments_response"
	case "/aiserver.v1.BackgroundComposerService/ListTeamEnvironments":
		return &aiserverv1.ListTeamEnvironmentsResponse{}, "list_team_environments_response"
	default:
		return nil, ""
	}
}

func decodesUnaryRequest(path string) bool {
	if path == bidiAppendPath {
		return true
	}
	message, _ := unaryRequestMessage(path)
	return message != nil
}

func decodesUnaryResponse(path string) bool {
	message, _ := unaryResponseMessage(path)
	return message != nil
}

func newMessage(messageType string) proto.Message {
	switch messageType {
	case "aiserver.v1.BidiRequestId":
		return &aiserverv1.BidiRequestId{}
	case "agent.v1.AgentServerMessage":
		return &agentv1.AgentServerMessage{}
	default:
		return nil
	}
}

func marshalProtoJSON(message proto.Message) string {
	if message == nil {
		return ""
	}
	payload, err := (protojson.MarshalOptions{
		UseProtoNames:   true,
		EmitUnpopulated: false,
		Indent:          "  ",
	}).Marshal(message)
	if err != nil {
		return ""
	}
	return string(payload)
}

func activeOneofName(message proto.Message) string {
	if message == nil {
		return ""
	}
	reflected := message.ProtoReflect()
	oneofs := reflected.Descriptor().Oneofs()
	for index := 0; index < oneofs.Len(); index++ {
		oneof := oneofs.Get(index)
		field := reflected.WhichOneof(oneof)
		if field != nil {
			return string(field.Name())
		}
	}
	return string(reflected.Descriptor().Name())
}

func prettyJSON(payload []byte) string {
	var target any
	if err := json.Unmarshal(payload, &target); err != nil {
		return string(payload)
	}
	formatted, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return string(payload)
	}
	return string(formatted)
}

func clippedHex(payload []byte, max int) string {
	if len(payload) > max {
		return hex.EncodeToString(payload[:max]) + "..."
	}
	return hex.EncodeToString(payload)
}

// fallbackDisplayBody turns non-protobuf (or failed-proto) bodies into UI-visible JSON.
// Covers text/plain error pages, application/json, and UTF-8 text mistaken as proto.
func fallbackDisplayBody(contentType string, payload []byte) (decodedJSON string, kind string, ok bool) {
	if len(payload) == 0 {
		return "", "", false
	}
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if strings.Contains(ct, "json") {
		if trimmed := bytes.TrimSpace(payload); json.Valid(trimmed) {
			return prettyJSON(trimmed), "json", true
		}
		wrapped, err := json.MarshalIndent(map[string]any{
			"content_type": contentType,
			"text":         string(payload),
		}, "", "  ")
		if err != nil {
			return "", "", false
		}
		return string(wrapped), "json_text", true
	}
	if strings.Contains(ct, "text/") || strings.Contains(ct, "xml") || strings.Contains(ct, "javascript") || looksLikeUTF8Text(payload) {
		wrapped, err := json.MarshalIndent(map[string]any{
			"content_type": contentType,
			"text":         string(payload),
		}, "", "  ")
		if err != nil {
			return "", "", false
		}
		kind = "text"
		if strings.Contains(ct, "html") {
			kind = "html"
		}
		return string(wrapped), kind, true
	}
	return "", "", false
}

func looksLikeUTF8Text(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	sample := payload
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	if !utf8.Valid(sample) {
		return false
	}
	printable := 0
	for _, b := range sample {
		if b == '\n' || b == '\r' || b == '\t' || (b >= 32 && b < 127) || b >= 0x80 {
			printable++
		}
	}
	return printable*10 >= len(sample)*8
}

func decompressIfNeeded(captured []byte, codec string) ([]byte, error) {
	if len(captured) == 0 || codec == "" || strings.EqualFold(codec, "identity") {
		return captured, nil
	}
	return decompressPayload(captured, codec)
}
