package tab

import "github.com/leookun/cursor-byok/tab-server/src/codec"

// newProtoReader returns a protobuf reader over payload.
func newProtoReader(payload []byte) *codec.Reader {
	return codec.NewReader(payload)
}
