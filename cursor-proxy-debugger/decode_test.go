package proxydebugger

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"

	"google.golang.org/protobuf/proto"
)

func TestDecodeForkTrafficRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		message  proto.Message
		kind     string
		contains []string
	}{
		{
			name: "notify conversation clone",
			path: notifyConversationClonePath,
			message: &agentv1.NotifyConversationCloneRequest{
				ConversationId:       "new-conversation",
				SourceConversationId: "source-conversation",
				SourceRequestId:      "source-request",
			},
			kind:     "notify_conversation_clone_request",
			contains: []string{`"conversation_id":"new-conversation"`, `"source_conversation_id":"source-conversation"`},
		},
		{
			name: "upload conversation blobs",
			path: uploadConversationBlobsPath,
			message: &agentv1.UploadConversationBlobsRequest{
				ConversationId: "new-conversation",
				Blobs: []*agentv1.BlobEntry{{
					Id:    []byte{1, 2},
					Value: []byte("blob-value"),
				}},
				ChunkIndex:  1,
				TotalChunks: 2,
			},
			kind:     "upload_conversation_blobs_request",
			contains: []string{`"conversation_id":"new-conversation"`, `"total_chunks":2`, `"value":"YmxvYi12YWx1ZQ=="`},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			payload, err := proto.Marshal(test.message)
			if err != nil {
				t.Fatal(err)
			}
			decoded, kind, requestID, err := decodeUnaryRequest(test.path, payload)
			if err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if kind != test.kind {
				t.Fatalf("kind = %q, want %q", kind, test.kind)
			}
			if requestID != "" {
				t.Fatalf("request ID = %q, want empty", requestID)
			}
			compact := compactJSON(t, decoded)
			for _, expected := range test.contains {
				if !strings.Contains(compact, expected) {
					t.Errorf("decoded JSON does not contain %q:\n%s", expected, decoded)
				}
			}
		})
	}
}

func TestDecodeForkTrafficResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		message  proto.Message
		kind     string
		contains string
	}{
		{
			name:     "notify conversation clone",
			path:     notifyConversationClonePath,
			message:  &agentv1.NotifyConversationCloneResponse{},
			kind:     "notify_conversation_clone_response",
			contains: `{}`,
		},
		{
			name:     "upload conversation blobs",
			path:     uploadConversationBlobsPath,
			message:  &agentv1.UploadConversationBlobsResponse{},
			kind:     "upload_conversation_blobs_response",
			contains: `{}`,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			payload, err := proto.Marshal(test.message)
			if err != nil {
				t.Fatal(err)
			}
			decoded, kind, err := decodeUnaryResponse(test.path, payload)
			if err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if kind != test.kind {
				t.Fatalf("kind = %q, want %q", kind, test.kind)
			}
			if !strings.Contains(compactJSON(t, decoded), test.contains) {
				t.Errorf("decoded JSON does not contain %q:\n%s", test.contains, decoded)
			}
		})
	}
}

func TestFinishResponseBodyDecodesCompressedCloneResponse(t *testing.T) {
	t.Parallel()

	payload, err := proto.Marshal(&agentv1.NotifyConversationCloneResponse{})
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	server := &Server{store: newExchangeStore(defaultMaxStoreBytes, 0)}
	server.store.create(&Exchange{
		ExchangeSummary: ExchangeSummary{ID: "1", StartedAt: time.Now()},
	})
	server.finishResponseBody("1", notifyConversationClonePath, "gzip", compressed.Bytes(), int64(compressed.Len()), false, nil)

	exchange, ok := server.store.get("1")
	if !ok {
		t.Fatal("exchange was not stored")
	}
	if exchange.ResponseKind != "notify_conversation_clone_response" {
		t.Fatalf("response kind = %q", exchange.ResponseKind)
	}
	if !strings.Contains(compactJSON(t, exchange.Response.DecodedJSON), `{}`) {
		t.Fatalf("unexpected decoded response:\n%s", exchange.Response.DecodedJSON)
	}
	if exchange.Response.DecodeError != "" {
		t.Fatalf("decode error = %q", exchange.Response.DecodeError)
	}
}

func TestFinishRequestBodyDecodesCompressedCloneRequest(t *testing.T) {
	t.Parallel()

	payload, err := proto.Marshal(&agentv1.NotifyConversationCloneRequest{
		ConversationId:       "new-conversation",
		SourceConversationId: "source-conversation",
		SourceRequestId:      "source-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	server := &Server{store: newExchangeStore(defaultMaxStoreBytes, 0)}
	server.store.create(&Exchange{
		ExchangeSummary: ExchangeSummary{ID: "1", StartedAt: time.Now()},
	})
	server.finishRequestBody("1", notifyConversationClonePath, "gzip", compressed.Bytes(), int64(compressed.Len()), false, nil)

	exchange, ok := server.store.get("1")
	if !ok {
		t.Fatal("exchange was not stored")
	}
	if exchange.RequestKind != "notify_conversation_clone_request" {
		t.Fatalf("request kind = %q", exchange.RequestKind)
	}
	if !strings.Contains(compactJSON(t, exchange.Request.DecodedJSON), `"conversation_id":"new-conversation"`) {
		t.Fatalf("unexpected decoded request:\n%s", exchange.Request.DecodedJSON)
	}
	if exchange.FrameCount != 1 || len(exchange.Request.Frames) != 1 {
		t.Fatalf("unary request should synthesize one frame: frameCount=%d frames=%d", exchange.FrameCount, len(exchange.Request.Frames))
	}
}

func TestDecodeCommonAiserverUnaryRPCs(t *testing.T) {
	t.Parallel()
	req, err := proto.Marshal(&aiserverv1.ServerTimeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	decoded, kind, _, err := decodeUnaryRequest("/aiserver.v1.AiService/ServerTime", req)
	if err != nil || kind != "server_time_request" || decoded == "" {
		t.Fatalf("ServerTime request: kind=%q err=%v decoded=%q", kind, err, decoded)
	}
	resp, err := proto.Marshal(&aiserverv1.GetGithubAccessTokenForReposResponse{})
	if err != nil {
		t.Fatal(err)
	}
	decoded, kind, err = decodeUnaryResponse("/aiserver.v1.BackgroundComposerService/GetGithubAccessTokenForRepos", resp)
	if err != nil || kind != "get_github_access_token_for_repos_response" || decoded == "" {
		t.Fatalf("github token response: kind=%q err=%v", kind, err)
	}

	handshake, err := proto.Marshal(&aiserverv1.FastRepoInitHandshakeV2Request{})
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(handshake); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: newExchangeStore(defaultMaxStoreBytes, 0), config: Config{}.normalized()}
	server.store.create(&Exchange{ExchangeSummary: ExchangeSummary{ID: "repo", StartedAt: time.Now()}})
	server.finishRequestBody("repo", "/aiserver.v1.RepositoryService/FastRepoInitHandshakeV2", "gzip", compressed.Bytes(), int64(compressed.Len()), false, nil)
	ex, ok := server.store.get("repo")
	if !ok {
		t.Fatal("missing")
	}
	if ex.RequestKind != "fast_repo_init_handshake_v2_request" || ex.Request.DecodedJSON == "" || ex.FrameCount < 1 {
		t.Fatalf("unary aiserver decode failed: kind=%q frames=%d err=%q", ex.RequestKind, ex.FrameCount, ex.Request.DecodeError)
	}
}

func TestMaybeUnwrapConnectUnaryExactEnvelope(t *testing.T) {
	t.Parallel()
	inner := []byte("hello-proto")
	envelope := make([]byte, 5+len(inner))
	envelope[0] = 0
	envelope[1] = 0
	envelope[2] = 0
	envelope[3] = 0
	envelope[4] = byte(len(inner))
	copy(envelope[5:], inner)
	got, err := maybeUnwrapConnectUnary(envelope, "")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(inner) {
		t.Fatalf("got %q want %q", got, inner)
	}
	// non-envelope raw body stays unchanged
	raw := []byte{0x0a, 0x01, 0x61}
	got, err = maybeUnwrapConnectUnary(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatalf("raw body should pass through")
	}
}

func TestFinishRequestBodySynthesizesBidiAppendFrame(t *testing.T) {
	t.Parallel()
	payload, err := proto.Marshal(&aiserverv1.BidiAppendRequest{
		RequestId: &aiserverv1.BidiRequestId{RequestId: "req-12"},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: newExchangeStore(defaultMaxStoreBytes, 0), config: Config{}.normalized()}
	server.store.create(&Exchange{ExchangeSummary: ExchangeSummary{ID: "bidi", StartedAt: time.Now()}})
	server.finishRequestBody("bidi", bidiAppendPath, "identity", payload, int64(len(payload)), false, nil)
	exchange, ok := server.store.get("bidi")
	if !ok {
		t.Fatal("missing exchange")
	}
	if exchange.RequestID != "req-12" {
		t.Fatalf("requestID=%q", exchange.RequestID)
	}
	if exchange.Request.DecodedJSON == "" {
		t.Fatal("expected decodedJson for BidiAppend unary")
	}
	if exchange.FrameCount != 1 || len(exchange.Request.Frames) != 1 {
		t.Fatalf("expected synthetic unary frame, frameCount=%d frames=%d", exchange.FrameCount, len(exchange.Request.Frames))
	}
	if exchange.Request.Frames[0].Kind == "" {
		t.Fatal("synthetic frame kind empty")
	}
}

func TestFinishRequestBodyUnwrapsConnectUnaryEnvelope(t *testing.T) {
	t.Parallel()
	inner, err := proto.Marshal(&aiserverv1.BidiAppendRequest{
		RequestId: &aiserverv1.BidiRequestId{RequestId: "env-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope := make([]byte, 5+len(inner))
	binary.BigEndian.PutUint32(envelope[1:5], uint32(len(inner)))
	copy(envelope[5:], inner)

	server := &Server{store: newExchangeStore(defaultMaxStoreBytes, 0), config: Config{}.normalized()}
	server.store.create(&Exchange{ExchangeSummary: ExchangeSummary{ID: "env", StartedAt: time.Now()}})
	// Force the envelope path: corrupt raw decode by... actually raw envelope fails proto.Unmarshal of BidiAppend,
	// then unwrap retries.
	server.finishRequestBody("env", bidiAppendPath, "identity", envelope, int64(len(envelope)), false, nil)
	exchange, ok := server.store.get("env")
	if !ok {
		t.Fatal("missing exchange")
	}
	if exchange.RequestID != "env-1" {
		t.Fatalf("requestID=%q want env-1 (decodedJson err=%q)", exchange.RequestID, exchange.Request.DecodeError)
	}
	if exchange.FrameCount < 1 {
		t.Fatal("expected synthetic frame after unwrap")
	}
}

func compactJSON(t *testing.T, raw string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(raw)); err != nil {
		t.Fatalf("compact json: %v\n%s", err, raw)
	}
	return buf.String()
}
