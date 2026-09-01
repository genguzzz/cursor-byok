// Package proto implements the Connect protocol framing and the minimal
// protobuf wire codec used by the Cursor Tab surface.
//
// Connect frames a unary call as a 5-byte prefix (1 flag byte + 4-byte
// big-endian length) followed by the payload. A server-streaming call sends
// the same prefix with flag 0 for each message, then a final frame with flag
// 0x02 carrying an end-stream JSON object ({} on success, {"error":{...}} on
// failure).
package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// EndStreamFlag marks the terminal frame of a Connect stream.
const EndStreamFlag = 0x02

// ErrTruncatedEnvelope is returned when a frame prefix is shorter than 5 bytes.
var ErrTruncatedEnvelope = errors.New("truncated connect envelope")

// ErrTruncatedPayload is returned when a frame body is shorter than its length.
var ErrTruncatedPayload = errors.New("truncated connect payload")

// EncodeUnary wraps a protobuf payload in a Connect unary envelope.
func EncodeUnary(payload []byte) []byte {
	return append(encodePrefix(0, uint32(len(payload))), payload...)
}

// EncodeMessage wraps a protobuf payload in a Connect stream message frame.
func EncodeMessage(payload []byte) []byte {
	return append(encodePrefix(0, uint32(len(payload))), payload...)
}

// EncodeEndStream builds the terminal frame of a successful stream.
func EncodeEndStream() []byte {
	return EncodeErrorEndStream(nil)
}

// EncodeErrorEndStream builds the terminal frame of a stream. A nil error
// produces the success payload {}; otherwise the frame carries the Connect
// error code and message.
func EncodeErrorEndStream(err error) []byte {
	payload := []byte("{}")
	if err != nil {
		payload = encodeErrorPayload(err)
	}
	return append(encodePrefix(EndStreamFlag, uint32(len(payload))), payload...)
}

func encodeErrorPayload(err error) []byte {
	streamErr, ok := err.(*StreamError)
	if !ok {
		streamErr = &StreamError{Code: CodeInternal, Message: err.Error()}
	}
	return []byte(fmt.Sprintf(
		`{"error":{"code":%q,"message":%q}}`,
		streamErr.Code,
		streamErr.Message,
	))
}

func encodePrefix(flags byte, length uint32) []byte {
	prefix := make([]byte, 5)
	prefix[0] = flags
	binary.BigEndian.PutUint32(prefix[1:], length)
	return prefix
}

// DecodeEnvelope splits a Connect stream body into (flags, payload) frames.
func DecodeEnvelope(body []byte) ([]Frame, error) {
	var frames []Frame
	for len(body) > 0 {
		if len(body) < 5 {
			return nil, ErrTruncatedEnvelope
		}
		flags := body[0]
		length := binary.BigEndian.Uint32(body[1:5])
		body = body[5:]
		if uint32(len(body)) < length {
			return nil, ErrTruncatedPayload
		}
		frames = append(frames, Frame{Flags: flags, Payload: body[:length]})
		body = body[length:]
	}
	return frames, nil
}

// Frame is one Connect frame.
type Frame struct {
	Flags   byte
	Payload []byte
}

// IsEndStream reports whether this frame terminates the stream.
func (f Frame) IsEndStream() bool {
	return f.Flags&EndStreamFlag != 0
}

// DecodePayload extracts the protobuf payload of a unary request, tolerating
// both framed and raw bodies.
func DecodePayload(body []byte) ([]byte, error) {
	if len(body) >= 5 {
		flags := body[0]
		length := binary.BigEndian.Uint32(body[1:5])
		if flags&EndStreamFlag == 0 && int(length) == len(body)-5 {
			return body[5:], nil
		}
	}
	return body, nil
}

// StreamError is a Connect stream error.
type StreamError struct {
	Code    Code
	Message string
}

// Error implements the error interface.
func (e *StreamError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Code is a Connect error code.
type Code string

// Connect error codes used by this server.
const (
	CodeCanceled        Code = "canceled"
	CodeInvalidArgument Code = "invalid_argument"
	CodeNotFound        Code = "not_found"
	CodeUnavailable     Code = "unavailable"
	CodeInternal        Code = "internal"
)

// Unavailable wraps err as a Connect unavailable stream error.
func Unavailable(err error) error {
	return &StreamError{Code: CodeUnavailable, Message: err.Error()}
}

// Internal wraps err as a Connect internal stream error.
func Internal(err error) error {
	return &StreamError{Code: CodeInternal, Message: err.Error()}
}
