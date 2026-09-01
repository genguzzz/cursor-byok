package tab

import "github.com/leookun/cursor-byok/tab-server/src/proto"

// Complete forwards an inline completion request to the service.
func (h *Handler) Complete(request *proto.StreamCppRequest, emit func(chunk string)) (string, error) {
	return h.service.Complete(request, emit)
}

// NextEdit forwards a next-edit request to the service.
func (h *Handler) NextEdit(request *proto.StreamNextCursorPredictionRequest) (*proto.StreamNextCursorPredictionResponse, error) {
	return h.service.NextEdit(request)
}
