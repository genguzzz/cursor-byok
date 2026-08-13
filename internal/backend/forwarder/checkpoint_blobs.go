package forwarder

import (
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

const (
	checkpointBlobWriteBaseTimeout = 5 * time.Second
	checkpointBlobWritePerBlob     = 500 * time.Millisecond
	checkpointBlobWritePerMegabyte = 2 * time.Second
	checkpointBlobWriteMaxTimeout  = 30 * time.Second
)

const checkpointBlobMegabyte int64 = 1 << 20

type pendingCheckpointBlobWrite struct {
	requestID uint32
	blob      CheckpointBlob
}

// checkpointBlobWriteTimeoutFor scales the checkpoint acknowledgement deadline
// with the number and total size of blobs still waiting for the client to
// confirm. Large codebase file-tree snapshots take the client much longer to
// write back, so a fixed deadline rejects healthy checkpoints too early.
func checkpointBlobWriteTimeoutFor(pending map[uint32]pendingCheckpointBlobWrite) time.Duration {
	var totalBytes int64
	for _, write := range pending {
		totalBytes += int64(len(write.blob.Data))
	}
	timeout := checkpointBlobWriteBaseTimeout +
		time.Duration(len(pending))*checkpointBlobWritePerBlob +
		time.Duration(totalBytes/checkpointBlobMegabyte)*checkpointBlobWritePerMegabyte
	if timeout > checkpointBlobWriteMaxTimeout {
		return checkpointBlobWriteMaxTimeout
	}
	return timeout
}

func clonePendingTurnCompletion(completion *pendingTurnCompletion) *pendingTurnCompletion {
	if completion == nil {
		return nil
	}
	cloned := *completion
	return &cloned
}

func (service *Service) queueCheckpointProjection(stream *ActiveStream, projection *CheckpointProjection, completion *pendingTurnCompletion) error {
	if service == nil || stream == nil || projection == nil || projection.State == nil {
		return nil
	}
	state, ok := proto.Clone(projection.State).(*agentv1.ConversationStateStructure)
	if !ok || state == nil {
		return fmt.Errorf("clone checkpoint state")
	}

	stream.mu.Lock()
	if stream.PendingCheckpointBlobWrites == nil {
		stream.PendingCheckpointBlobWrites = make(map[uint32]pendingCheckpointBlobWrite)
	}
	if stream.ConfirmedCheckpointBlobs == nil {
		stream.ConfirmedCheckpointBlobs = make(map[string]struct{})
	}
	if completion == nil && stream.PendingCheckpoint != nil {
		completion = stream.PendingCheckpoint.Completion
	}
	required := make(map[string]struct{}, len(projection.Blobs))
	pendingKeys := make(map[string]struct{}, len(stream.PendingCheckpointBlobWrites))
	for _, write := range stream.PendingCheckpointBlobWrites {
		pendingKeys[string(write.blob.ID)] = struct{}{}
	}
	toWrite := make([]pendingCheckpointBlobWrite, 0, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		key := string(blob.ID)
		if key == "" {
			continue
		}
		required[key] = struct{}{}
		if _, confirmed := stream.ConfirmedCheckpointBlobs[key]; confirmed {
			continue
		}
		if _, pending := pendingKeys[key]; pending {
			continue
		}
		stream.NextCheckpointBlobRequestID++
		if stream.NextCheckpointBlobRequestID == 0 {
			stream.NextCheckpointBlobRequestID++
		}
		requestID := stream.NextCheckpointBlobRequestID
		stream.PendingCheckpointBlobWrites[requestID] = pendingCheckpointBlobWrite{requestID: requestID, blob: blob}
		pendingKeys[key] = struct{}{}
		toWrite = append(toWrite, pendingCheckpointBlobWrite{requestID: requestID, blob: blob})
	}
	stream.PendingCheckpoint = &pendingCheckpointPublish{
		State:      state,
		Required:   required,
		Completion: clonePendingTurnCompletion(completion),
	}
	if completion != nil {
		stream.Phase = TurnPhaseCheckpointing
	}
	checkpointTimeout := checkpointBlobWriteTimeoutFor(stream.PendingCheckpointBlobWrites)
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()

	for _, write := range toWrite {
		if err := service.broker.Publish(stream.RequestID, StreamEvent{
			Message: buildSetCheckpointBlobMessage(write.requestID, write.blob),
		}); err != nil {
			return service.finishAfterCheckpointSyncFailure(stream, fmt.Errorf("publish checkpoint blob: %w", err))
		}
	}
	if service.checkpointProjectionReady(stream) {
		return service.publishReadyCheckpoint(stream)
	}
	// Keep the latest live UI state ahead of an immediate client abort. Blob writes are
	// ordered before this snapshot; acknowledgements still gate terminal completion.
	if completion == nil {
		if err := service.publishPendingCheckpoint(stream); err != nil {
			return service.finishAfterCheckpointSyncFailure(stream, fmt.Errorf("publish pending checkpoint: %w", err))
		}
	}
	service.scheduleStreamTimer(
		stream,
		providerTimerKey(streamTimerCheckpointBlobs, ""),
		checkpointTimeout,
		streamTimerCheckpointBlobs,
		"",
		0,
		"checkpoint blob write timeout",
	)
	return nil
}

func (service *Service) publishPendingCheckpoint(stream *ActiveStream) error {
	if service == nil || stream == nil {
		return nil
	}
	stream.mu.Lock()
	pending := stream.PendingCheckpoint
	if pending == nil || pending.Published {
		stream.mu.Unlock()
		return nil
	}
	pending.Published = true
	state := pending.State
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	if err := service.broker.Publish(stream.RequestID, StreamEvent{Message: buildCheckpointMessage(state)}); err != nil {
		stream.mu.Lock()
		if stream.PendingCheckpoint == pending {
			pending.Published = false
		}
		stream.mu.Unlock()
		return err
	}
	return nil
}

func (service *Service) checkpointProjectionReady(stream *ActiveStream) bool {
	if stream == nil {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.PendingCheckpoint == nil {
		return false
	}
	for key := range stream.PendingCheckpoint.Required {
		if _, confirmed := stream.ConfirmedCheckpointBlobs[key]; !confirmed {
			return false
		}
	}
	return true
}

func (service *Service) handleCheckpointBlobResult(stream *ActiveStream, message *agentv1.KvClientMessage) error {
	if service == nil || stream == nil || message == nil || message.GetSetBlobResult() == nil {
		return nil
	}
	stream.mu.Lock()
	write, ok := stream.PendingCheckpointBlobWrites[message.GetId()]
	if ok {
		delete(stream.PendingCheckpointBlobWrites, message.GetId())
	}
	key := string(write.blob.ID)
	required := false
	if ok && stream.PendingCheckpoint != nil {
		_, required = stream.PendingCheckpoint.Required[key]
	}
	if ok && message.GetSetBlobResult().GetError() == nil {
		stream.ConfirmedCheckpointBlobs[key] = struct{}{}
	}
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	if !ok {
		return nil
	}
	if blobErr := message.GetSetBlobResult().GetError(); blobErr != nil && required {
		return service.finishAfterCheckpointSyncFailure(stream, fmt.Errorf(
			"client rejected checkpoint blob %s: %s",
			hex.EncodeToString([]byte(key)),
			firstNonEmpty(strings.TrimSpace(blobErr.GetMessage()), "unknown error"),
		))
	}
	if service.checkpointProjectionReady(stream) {
		return service.publishReadyCheckpoint(stream)
	}
	return nil
}

func (service *Service) publishReadyCheckpoint(stream *ActiveStream) error {
	if service == nil || stream == nil {
		return nil
	}
	stream.mu.Lock()
	pending := stream.PendingCheckpoint
	if pending == nil {
		stream.mu.Unlock()
		return nil
	}
	for key := range pending.Required {
		if _, confirmed := stream.ConfirmedCheckpointBlobs[key]; !confirmed {
			stream.mu.Unlock()
			return nil
		}
	}
	stream.PendingCheckpoint = nil
	state := pending.State
	completion := clonePendingTurnCompletion(pending.Completion)
	published := pending.Published
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	clearStreamTimer(stream, providerTimerKey(streamTimerCheckpointBlobs, ""))
	if !published {
		if err := service.broker.Publish(stream.RequestID, StreamEvent{Message: buildCheckpointMessage(state)}); err != nil {
			if completion != nil {
				log.Printf("forwarder checkpoint publish skipped before successful terminal request_id=%s err=%v", stream.RequestID, err)
				return service.finishSuccessfulTurnAfterCheckpoint(stream, *completion)
			}
			return err
		}
	}
	if completion != nil {
		return service.finishSuccessfulTurnAfterCheckpoint(stream, *completion)
	}
	return nil
}

func (service *Service) handleCheckpointBlobTimeout(stream *ActiveStream) error {
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	pendingCount := len(stream.PendingCheckpointBlobWrites)
	stream.mu.Unlock()
	return service.finishAfterCheckpointSyncFailure(stream, fmt.Errorf("%d checkpoint blob writes timed out", pendingCount))
}

func (service *Service) finishAfterCheckpointSyncFailure(stream *ActiveStream, cause error) error {
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	pending := stream.PendingCheckpoint
	stream.PendingCheckpoint = nil
	stream.PendingCheckpointBlobWrites = make(map[uint32]pendingCheckpointBlobWrite)
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	clearStreamTimer(stream, providerTimerKey(streamTimerCheckpointBlobs, ""))
	if cause != nil {
		log.Printf("forwarder checkpoint blob sync skipped request_id=%s conversation_id=%s err=%v", stream.RequestID, stream.ConversationID, cause)
	}
	if pending != nil && pending.Completion != nil {
		// The turn already finished, so a checkpoint tail-sync timeout is a
		// harmless miss. Complete the turn successfully instead of failing it.
		return service.finishSuccessfulTurnAfterCheckpoint(stream, *pending.Completion)
	}
	// Intermediate checkpoint while the model is still running: drop the
	// snapshot and let the provider loop continue. Never fail a healthy
	// in-flight request over a checkpoint acknowledgement deadline.
	return nil
}

func (service *Service) discardPendingCheckpoint(stream *ActiveStream, reason string) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	stream.PendingCheckpoint = nil
	stream.PendingCheckpointBlobWrites = make(map[uint32]pendingCheckpointBlobWrite)
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	clearStreamTimer(stream, providerTimerKey(streamTimerCheckpointBlobs, ""))
	if strings.TrimSpace(reason) != "" {
		log.Printf("forwarder pending checkpoint discarded request_id=%s conversation_id=%s reason=%s", stream.RequestID, stream.ConversationID, strings.TrimSpace(reason))
	}
}
