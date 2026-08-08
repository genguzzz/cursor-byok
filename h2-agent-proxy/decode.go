package h2agentproxy

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	agentv1 "cursor/gen/agentv1"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const maxConnectFrameBytes = 64 << 20

type decodedFrame struct {
	Index       int             `json:"index"`
	Flags       uint8           `json:"flags"`
	Length      int             `json:"length"`
	Compressed  bool            `json:"compressed"`
	EndStream   bool            `json:"endStream"`
	Kind        string          `json:"kind,omitempty"`
	MessageType string          `json:"messageType,omitempty"`
	JSON        json.RawMessage `json:"json,omitempty"`
	Error       string          `json:"error,omitempty"`
}

func DecodeCaptureDirectory(dir string) error {
	matches, err := filepath.Glob(filepath.Join(dir, "*_request.body.bin"))
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return fmt.Errorf("no *_request.body.bin in %s", dir)
	}
	for _, reqPath := range matches {
		base := strings.TrimSuffix(filepath.Base(reqPath), "_request.body.bin")
		reqHeaders := filepath.Join(dir, base+"_request.headers.json")
		respBody := filepath.Join(dir, base+"_response.body.bin")
		if err := decodeCapturePair(dir, base, reqPath, reqHeaders, respBody); err != nil {
			return err
		}
	}
	return nil
}

func decodeCapturePair(dir, base, reqBody, reqHeaders, respBody string) error {
	codec := connectCodecFromHeadersFile(reqHeaders)
	reqFrames, err := decodeConnectFile(reqBody, codec, &agentv1.AgentClientMessage{})
	if err != nil {
		return fmt.Errorf("%s request: %w", base, err)
	}
	if err := writeFramesJSONL(filepath.Join(dir, base+"_request.frames.jsonl"), reqFrames); err != nil {
		return err
	}
	var respFrames []decodedFrame
	if _, statErr := os.Stat(respBody); statErr == nil {
		respFrames, err = decodeConnectFile(respBody, codec, &agentv1.AgentServerMessage{})
		if err != nil {
			return fmt.Errorf("%s response: %w", base, err)
		}
		if err := writeFramesJSONL(filepath.Join(dir, base+"_response.frames.jsonl"), respFrames); err != nil {
			return err
		}
	}
	summary := map[string]any{
		"id":             base,
		"requestFrames":  summarizeKinds(reqFrames),
		"responseFrames": summarizeKinds(respFrames),
	}
	payload, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, base+"_summary.json"), payload, 0o644)
}

func decodeConnectFile(path, codec string, prototype proto.Message) ([]decodedFrame, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decodeConnectFrames(raw, codec, prototype)
}

func decodeConnectFrames(raw []byte, codec string, prototype proto.Message) ([]decodedFrame, error) {
	frames := make([]decodedFrame, 0, 8)
	offset := 0
	index := 0
	for offset+5 <= len(raw) {
		flags := raw[offset]
		length := int(binary.BigEndian.Uint32(raw[offset+1 : offset+5]))
		offset += 5
		if length < 0 || length > maxConnectFrameBytes {
			frames = append(frames, decodedFrame{Index: index, Flags: flags, Length: length, Error: "connect frame length invalid"})
			break
		}
		if offset+length > len(raw) {
			frames = append(frames, decodedFrame{
				Index:  index,
				Flags:  flags,
				Length: length,
				Error:  fmt.Sprintf("truncated: need %d bytes, have %d", length, len(raw)-offset),
			})
			break
		}
		payload := append([]byte(nil), raw[offset:offset+length]...)
		offset += length
		frames = append(frames, decodeOneFrame(index, flags, payload, codec, prototype))
		index++
	}
	if offset < len(raw) {
		frames = append(frames, decodedFrame{
			Index:  index,
			Length: len(raw) - offset,
			Error:  fmt.Sprintf("trailing %d bytes after last frame", len(raw)-offset),
		})
	}
	return frames, nil
}

func decodeOneFrame(index int, flags uint8, payload []byte, codec string, prototype proto.Message) decodedFrame {
	frame := decodedFrame{
		Index:      index,
		Flags:      flags,
		Length:     len(payload),
		Compressed: flags&0x01 != 0,
		EndStream:  flags&0x02 != 0,
	}
	body := payload
	if frame.Compressed {
		decoded, err := gunzipPayload(payload)
		if err != nil {
			frame.Error = err.Error()
			return frame
		}
		body = decoded
	}
	if frame.EndStream {
		frame.Kind = "end_stream"
		frame.MessageType = "connect.error.v1.EndStreamResponse"
		frame.JSON = json.RawMessage(prettyRawJSON(body))
		return frame
	}
	message := proto.Clone(prototype)
	if err := proto.Unmarshal(body, message); err != nil {
		frame.Error = fmt.Sprintf("protobuf unmarshal: %v", err)
		return frame
	}
	frame.MessageType = string(message.ProtoReflect().Descriptor().FullName())
	frame.Kind = activeOneofName(message)
	encoded, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: false}).Marshal(message)
	if err != nil {
		frame.Error = err.Error()
		return frame
	}
	frame.JSON = json.RawMessage(encoded)
	return frame
}

func gunzipPayload(payload []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer reader.Close()
	decoded, err := io.ReadAll(io.LimitReader(reader, maxConnectFrameBytes+1))
	if err != nil {
		return nil, fmt.Errorf("gzip read: %w", err)
	}
	if len(decoded) > maxConnectFrameBytes {
		return nil, fmt.Errorf("gzip payload too large")
	}
	return decoded, nil
}

func writeFramesJSONL(path string, frames []decodedFrame) error {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	for _, frame := range frames {
		if err := encoder.Encode(frame); err != nil {
			return err
		}
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func summarizeKinds(frames []decodedFrame) []map[string]any {
	out := make([]map[string]any, 0, len(frames))
	for _, frame := range frames {
		item := map[string]any{
			"index":      frame.Index,
			"kind":       frame.Kind,
			"length":     frame.Length,
			"compressed": frame.Compressed,
		}
		if frame.Error != "" {
			item["error"] = frame.Error
		}
		out = append(out, item)
	}
	return out
}

func connectCodecFromHeadersFile(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "gzip"
	}
	var parsed struct {
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "gzip"
	}
	for key, value := range parsed.Headers {
		if strings.EqualFold(key, "Connect-Content-Encoding") {
			if strings.TrimSpace(value) != "" {
				return value
			}
		}
	}
	return "gzip"
}

func activeOneofName(message proto.Message) string {
	if message == nil {
		return ""
	}
	reflected := message.ProtoReflect()
	oneofs := reflected.Descriptor().Oneofs()
	for i := 0; i < oneofs.Len(); i++ {
		field := reflected.WhichOneof(oneofs.Get(i))
		if field != nil {
			return string(field.Name())
		}
	}
	return string(reflected.Descriptor().Name())
}

func prettyRawJSON(payload []byte) []byte {
	var target any
	if err := json.Unmarshal(payload, &target); err != nil {
		encoded, _ := json.Marshal(string(payload))
		return encoded
	}
	formatted, err := json.Marshal(target)
	if err != nil {
		return payload
	}
	return formatted
}

func encodeConnectFrame(flags uint8, payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	frame[0] = flags
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}
