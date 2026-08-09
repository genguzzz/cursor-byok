package upstream

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"testing"

	"cursor/gen/aiserverv1"

	"google.golang.org/protobuf/proto"
)

func TestUnwrapConnectUnaryAndRawProto(t *testing.T) {
	message := &aiserverv1.BidiRequestId{RequestId: "req-1"}
	raw, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}

	decoded := &aiserverv1.BidiRequestId{}
	if err := decodeProtoPayload("application/proto", raw, decoded); err != nil {
		t.Fatalf("raw proto: %v", err)
	}
	if decoded.GetRequestId() != "req-1" {
		t.Fatalf("raw request id = %q", decoded.GetRequestId())
	}

	frame := make([]byte, 5+len(raw))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(raw)))
	copy(frame[5:], raw)
	decoded = &aiserverv1.BidiRequestId{}
	if err := decodeProtoPayload("application/connect+proto", frame, decoded); err != nil {
		t.Fatalf("connect proto: %v", err)
	}
	if decoded.GetRequestId() != "req-1" {
		t.Fatalf("connect request id = %q", decoded.GetRequestId())
	}

	trailer := []byte(`{"error":null}`)
	framed := make([]byte, 0, len(frame)+5+len(trailer))
	framed = append(framed, frame...)
	framed = append(framed, 0x02)
	framed = append(framed, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(framed[len(frame)+1:len(frame)+5], uint32(len(trailer)))
	framed = append(framed, trailer...)
	decoded = &aiserverv1.BidiRequestId{}
	if err := decodeProtoPayload("application/proto", framed, decoded); err != nil {
		t.Fatalf("connect with trailer: %v", err)
	}
	if decoded.GetRequestId() != "req-1" {
		t.Fatalf("trailer request id = %q", decoded.GetRequestId())
	}
}

func TestDecodeProtoPayloadGzipRawProto(t *testing.T) {
	message := &aiserverv1.BidiRequestId{RequestId: "req-gzip"}
	raw, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	decoded := &aiserverv1.BidiRequestId{}
	if err := decodeProtoPayload("application/proto", buf.Bytes(), decoded); err != nil {
		t.Fatalf("gzip proto: %v", err)
	}
	if decoded.GetRequestId() != "req-gzip" {
		t.Fatalf("gzip request id = %q", decoded.GetRequestId())
	}
}

func TestDecodeProtoPayloadConnectGzipPayloadWithoutFlag(t *testing.T) {
	message := &aiserverv1.BidiRequestId{RequestId: "req-connect-gzip"}
	raw, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	compressed := buf.Bytes()
	frame := make([]byte, 5+len(compressed))
	// flags=0 故意不标 gzip，只靠 magic 嗅探。
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(compressed)))
	copy(frame[5:], compressed)

	decoded := &aiserverv1.BidiRequestId{}
	if err := decodeProtoPayload("application/proto", frame, decoded); err != nil {
		t.Fatalf("connect gzip without flag: %v", err)
	}
	if decoded.GetRequestId() != "req-connect-gzip" {
		t.Fatalf("request id = %q", decoded.GetRequestId())
	}
}

func TestDecodeProtoPayloadConnectJSON(t *testing.T) {
	jsonBody := []byte(`{"requestId":"req-json"}`)
	frame := make([]byte, 5+len(jsonBody))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(jsonBody)))
	copy(frame[5:], jsonBody)
	decoded := &aiserverv1.BidiRequestId{}
	if err := decodeProtoPayload("application/connect+json", frame, decoded); err != nil {
		t.Fatalf("connect json: %v", err)
	}
	if decoded.GetRequestId() != "req-json" {
		t.Fatalf("json request id = %q", decoded.GetRequestId())
	}
}

func TestEncodeRequestProtoPayloadConnectRoundTrip(t *testing.T) {
	message := &aiserverv1.BidiRequestId{RequestId: "req-encode"}
	raw, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	frame := wrapConnectUnary(raw)
	encoded, contentType, err := encodeRequestProtoPayload("application/connect+proto", frame, message)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "application/connect+proto" {
		t.Fatalf("content-type = %q", contentType)
	}
	decoded := &aiserverv1.BidiRequestId{}
	if err := decodeProtoPayload(contentType, encoded, decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.GetRequestId() != "req-encode" {
		t.Fatalf("request id = %q", decoded.GetRequestId())
	}
}
