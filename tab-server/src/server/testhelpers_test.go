package server

import (
	"bytes"
	"io"
	"testing"

	"github.com/leookun/cursor-byok/tab-server/src/codec"
	"github.com/leookun/cursor-byok/tab-server/src/proto"
)

func bytesReader(payload []byte) io.Reader {
	return bytes.NewReader(payload)
}

// encodeCurrentFileInfoForTest encodes a CurrentFileInfo submessage.
func encodeCurrentFileInfoForTest(info *proto.CurrentFileInfo) ([]byte, error) {
	writer := codec.NewWriter(256)
	writer.String(1, info.RelativeWorkspacePath)
	writer.String(2, info.Contents)
	if info.CursorPosition != nil {
		position := codec.NewWriter(16)
		position.Int32(1, info.CursorPosition.Line)
		position.Int32(2, info.CursorPosition.Column)
		writer.Nested(3, position.Bytes())
	}
	writer.String(5, info.LanguageID)
	writer.Int32(8, info.TotalNumberOfLines)
	writer.Int32(9, info.ContentsStartAtLine)
	return writer.Bytes(), nil
}

// encodeStreamCppRequestForTest encodes a StreamCppRequest carrying current_file.
func encodeStreamCppRequestForTest(currentFile []byte) ([]byte, error) {
	writer := codec.NewWriter(512)
	writer.Nested(1, currentFile)
	return writer.Bytes(), nil
}

// decodeStreamCppResponseForTest reads the text field of one response chunk.
func decodeStreamCppResponseForTest(t *testing.T, payload []byte) *proto.StreamCppResponse {
	t.Helper()
	response := &proto.StreamCppResponse{}
	reader := codec.NewReader(payload)
	for {
		field, ok, err := reader.Next()
		if err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if !ok {
			return response
		}
		if field.Number == 1 {
			response.Text = field.String()
		}
	}
}
