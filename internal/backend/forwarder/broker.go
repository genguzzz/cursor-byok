// broker.go 负责 request 维度活动流的订阅、广播、取消和终态收口。
package forwarder

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cursor/gen/agentv1"
	"cursor/internal/appdata"
	runtimecore "cursor/internal/backend/agent/core"
)

const subscriberSignalBufferSize = 1
const orphanSubscriberGracePeriod = 30 * time.Second
const terminalStreamRetentionPeriod = 30 * time.Second

type StreamBroker struct {
	mu      sync.RWMutex
	streams map[string]*ActiveStream
	nextID  atomic.Uint64
	store   *backlogStore
}

// NewStreamBroker 创建活动流注册表，backlog 落到 SQLite 文件。
func NewStreamBroker() *StreamBroker {
	return newStreamBrokerWithPath(backlogDBPath())
}

// newStreamBrokerWithPath 供测试指定隔离的 store 路径。
func newStreamBrokerWithPath(dbPath string) *StreamBroker {
	return &StreamBroker{
		streams: make(map[string]*ActiveStream),
		store:   newBacklogStore(dbPath),
	}
}

// backlogDBPath 返回 backlog 库路径。
func backlogDBPath() string {
	return filepath.Join(appdata.DataRootPath(), "backlog.db")
}

// OpenStream 打开或复用指定 request 的活动流，并刷新其最新上下文。
func (broker *StreamBroker) OpenStream(requestID string, conversationID string, turnSeq int64, modelID string, modelName string, mode agentv1.AgentMode, latestUserText string) (*ActiveStream, error) {
	normalizedRequestID := strings.TrimSpace(requestID)
	if normalizedRequestID == "" {
		return nil, nil
	}
	normalizedMode, err := validateSupportedActiveMode(mode)
	if err != nil {
		return nil, err
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if existing, ok := broker.streams[normalizedRequestID]; ok {
		existing.mu.Lock()
		existing.ConversationID = strings.TrimSpace(conversationID)
		existing.TurnSeq = turnSeq
		existing.ModelID = strings.TrimSpace(modelID)
		existing.ModelName = strings.TrimSpace(modelName)
		existing.Mode = normalizedMode
		existing.LatestUserText = strings.TrimSpace(latestUserText)
		if existing.Status == "" {
			existing.Status = StreamStatusCreated
		}
		if existing.PendingExecs == nil {
			existing.PendingExecs = make(map[string]runtimecore.PendingExec)
		}
		if existing.PendingInteractions == nil {
			existing.PendingInteractions = make(map[string]runtimecore.PendingInteraction)
		}
		if existing.PartialToolCallIDs == nil {
			existing.PartialToolCallIDs = make(map[string]struct{})
		}
		if existing.PatchEditQueues == nil {
			existing.PatchEditQueues = make(map[string][]queuedPatchEditOperation)
		}
		if existing.BackgroundShells == nil {
			existing.BackgroundShells = make(map[string]*BackgroundShellState)
		}
		if existing.BackgroundShellsByMessageID == nil {
			existing.BackgroundShellsByMessageID = make(map[uint32]string)
		}
		if existing.BackgroundShellsByExecID == nil {
			existing.BackgroundShellsByExecID = make(map[string]string)
		}
		if existing.BackgroundShellActions == nil {
			existing.BackgroundShellActions = make(map[string]time.Time)
		}
		if existing.PendingCheckpointBlobWrites == nil {
			existing.PendingCheckpointBlobWrites = make(map[uint32]pendingCheckpointBlobWrite)
		}
		if existing.ConfirmedCheckpointBlobs == nil {
			existing.ConfirmedCheckpointBlobs = make(map[string]struct{})
		}
		existing.UpdatedAt = time.Now().UTC()
		existing.mu.Unlock()
		return existing, nil
	}
	now := time.Now().UTC()
	stream := &ActiveStream{
		RequestID:                   normalizedRequestID,
		ConversationID:              strings.TrimSpace(conversationID),
		TurnSeq:                     turnSeq,
		ModelID:                     strings.TrimSpace(modelID),
		ModelName:                   strings.TrimSpace(modelName),
		Mode:                        normalizedMode,
		LatestUserText:              strings.TrimSpace(latestUserText),
		Status:                      StreamStatusCreated,
		Subscribers:                 make(map[string]*StreamSubscriber),
		PendingExecs:                make(map[string]runtimecore.PendingExec),
		PendingInteractions:         make(map[string]runtimecore.PendingInteraction),
		PartialToolCallIDs:          make(map[string]struct{}),
		PatchEditQueues:             make(map[string][]queuedPatchEditOperation),
		MCPToolServers:              make(map[string]string),
		RecentCompletedExecs:        make(map[uint32]time.Time),
		BackgroundShells:            make(map[string]*BackgroundShellState),
		BackgroundShellsByMessageID: make(map[uint32]string),
		BackgroundShellsByExecID:    make(map[string]string),
		BackgroundShellActions:      make(map[string]time.Time),
		PendingCheckpointBlobWrites: make(map[uint32]pendingCheckpointBlobWrite),
		ConfirmedCheckpointBlobs:    make(map[string]struct{}),
		CreatedAt:                   now,
		UpdatedAt:                   now,
	}
	broker.streams[normalizedRequestID] = stream
	return stream, nil
}

// Get 返回指定 request 对应的活动流句柄。
func (broker *StreamBroker) Get(requestID string) (*ActiveStream, bool) {
	if broker == nil {
		return nil, false
	}
	broker.mu.RLock()
	defer broker.mu.RUnlock()
	stream, ok := broker.streams[strings.TrimSpace(requestID)]
	return stream, ok
}

// Subscribe 为指定 request 注册一个新订阅者，并返回用于唤醒 backlog 消费的信号通道。
func (broker *StreamBroker) Subscribe(requestID string) (string, <-chan struct{}, error) {
	normalizedRequestID := strings.TrimSpace(requestID)
	if normalizedRequestID == "" {
		return "", nil, fmt.Errorf("request_id is required")
	}
	stream, ok := broker.Get(normalizedRequestID)
	if !ok || stream == nil {
		// RunSSE 可能先于 BidiAppend 到达。此时先创建一个占位活动流，
		// 等待后续上行把真实 conversation/model/mode 信息补齐。
		var err error
		stream, err = broker.OpenStream(normalizedRequestID, "", 0, "", "", agentv1.AgentMode_AGENT_MODE_AGENT, "")
		if err != nil {
			return "", nil, err
		}
	}
	subscriberID := fmt.Sprintf("sub-%d", broker.nextID.Add(1))
	subscriber := &StreamSubscriber{Signal: make(chan struct{}, subscriberSignalBufferSize)}

	stream.mu.Lock()
	broker.stopTerminalCleanupTimerLocked(stream)
	stream.Subscribers[subscriberID] = subscriber
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()

	return subscriberID, subscriber.Signal, nil
}

// SubscriberCount 返回指定 request 当前的订阅者数量。
func (broker *StreamBroker) SubscriberCount(requestID string) int {
	stream, ok := broker.Get(requestID)
	if !ok || stream == nil {
		return 0
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return len(stream.Subscribers)
}

func (broker *StreamBroker) stopTerminalCleanupTimerLocked(stream *ActiveStream) {
	if stream == nil {
		return
	}
	stream.TerminalCleanupSeq.Add(1)
	if stream.TerminalCleanupTimer != nil {
		stream.TerminalCleanupTimer.Stop()
		stream.TerminalCleanupTimer = nil
	}
}

// Unsubscribe 移除并关闭指定订阅者，并返回移除后的剩余订阅者数量。
func (broker *StreamBroker) Unsubscribe(requestID string, subscriberID string) int {
	stream, ok := broker.Get(requestID)
	if !ok || stream == nil {
		return 0
	}
	remaining := 0
	stream.mu.Lock()
	if _, ok := stream.Subscribers[strings.TrimSpace(subscriberID)]; ok {
		delete(stream.Subscribers, strings.TrimSpace(subscriberID))
	}
	remaining = len(stream.Subscribers)
	stream.mu.Unlock()
	return remaining
}

func (broker *StreamBroker) OtherConversationRequestIDs(conversationID string, keepRequestID string) []string {
	normalizedConversationID := strings.TrimSpace(conversationID)
	normalizedKeepRequestID := strings.TrimSpace(keepRequestID)
	if normalizedConversationID == "" {
		return nil
	}
	type requestStream struct {
		requestID string
		stream    *ActiveStream
	}
	candidates := make([]requestStream, 0, 2)
	broker.mu.RLock()
	for requestID, stream := range broker.streams {
		if stream == nil || strings.TrimSpace(requestID) == normalizedKeepRequestID {
			continue
		}
		candidates = append(candidates, requestStream{
			requestID: requestID,
			stream:    stream,
		})
	}
	broker.mu.RUnlock()
	requestIDs := make([]string, 0, 2)
	for _, candidate := range candidates {
		stream := candidate.stream
		stream.mu.Lock()
		sameConversation := strings.TrimSpace(stream.ConversationID) == normalizedConversationID
		status := stream.Status
		phase := stream.Phase
		stream.mu.Unlock()
		terminalPhase := phase == TurnPhaseCanceled || phase == TurnPhaseCompleted || phase == TurnPhaseFailed
		if !sameConversation || isTerminalStreamStatus(status) || terminalPhase {
			continue
		}
		requestIDs = append(requestIDs, candidate.requestID)
	}
	return requestIDs
}

func (broker *StreamBroker) scheduleTerminalCleanup(requestID string) bool {
	stream, ok := broker.Get(requestID)
	if !ok || stream == nil {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if len(stream.Subscribers) > 0 {
		broker.stopTerminalCleanupTimerLocked(stream)
		return false
	}
	if stream.Status != StreamStatusCompleted && stream.Status != StreamStatusCanceled && stream.Status != StreamStatusFailed {
		broker.stopTerminalCleanupTimerLocked(stream)
		return false
	}
	sequence := stream.TerminalCleanupSeq.Add(1)
	if stream.TerminalCleanupTimer != nil {
		stream.TerminalCleanupTimer.Stop()
	}
	stream.TerminalCleanupTimer = time.AfterFunc(terminalStreamRetentionPeriod, func() {
		broker.runScheduledTerminalCleanup(requestID, sequence)
	})
	stream.UpdatedAt = time.Now().UTC()
	return true
}

func (broker *StreamBroker) runScheduledTerminalCleanup(requestID string, sequence uint64) {
	stream, ok := broker.Get(requestID)
	if !ok || stream == nil {
		return
	}
	stream.mu.Lock()
	if stream.TerminalCleanupSeq.Load() != sequence {
		stream.mu.Unlock()
		return
	}
	stream.TerminalCleanupTimer = nil
	if len(stream.Subscribers) > 0 {
		stream.mu.Unlock()
		return
	}
	if stream.Status != StreamStatusCompleted && stream.Status != StreamStatusCanceled && stream.Status != StreamStatusFailed {
		stream.mu.Unlock()
		return
	}
	stream.mu.Unlock()
	broker.RemoveIfIdle(requestID)
}

// RemoveIfIdle 在没有订阅者时移除终态流，或移除仍为空壳的占位流。
func (broker *StreamBroker) RemoveIfIdle(requestID string) bool {
	normalizedRequestID := strings.TrimSpace(requestID)
	if normalizedRequestID == "" {
		return false
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	stream, ok := broker.streams[normalizedRequestID]
	if !ok || stream == nil {
		return false
	}
	stream.mu.Lock()
	subscriberCount := len(stream.Subscribers)
	isActive := stream.ProviderActive
	hasConversation := strings.TrimSpace(stream.ConversationID) != ""
	status := stream.Status
	if status == StreamStatusCompleted || status == StreamStatusCanceled || status == StreamStatusFailed {
		broker.stopTerminalCleanupTimerLocked(stream)
	}
	stream.mu.Unlock()
	if subscriberCount > 0 {
		return false
	}
	if status == StreamStatusCompleted || status == StreamStatusCanceled || status == StreamStatusFailed {
		delete(broker.streams, normalizedRequestID)
		_ = broker.store.remove(normalizedRequestID)
		return true
	}
	hasBacklog := broker.store.count(normalizedRequestID) > 0
	if isActive || hasBacklog || hasConversation {
		return false
	}
	delete(broker.streams, normalizedRequestID)
	_ = broker.store.remove(normalizedRequestID)
	return true
}

// Publish 把一个事件写入 backlog，并唤醒当前所有订阅者读取 backlog。
func (broker *StreamBroker) Publish(requestID string, event StreamEvent) error {
	stream, ok := broker.Get(requestID)
	if !ok || stream == nil {
		return fmt.Errorf("request is not active: %s", strings.TrimSpace(requestID))
	}
	stream.mu.Lock()
	if !event.End && isTerminalStreamStatus(stream.Status) {
		if !allowShellUIPublishAfterTerminalLocked(stream, event) {
			stream.mu.Unlock()
			return nil
		}
	}
	event.PublishedAt = time.Now().UTC()
	stream.backlogNextSeq++
	event.Seq = stream.backlogNextSeq
	stream.mu.Unlock()
	if err := broker.store.enqueue(requestID, event); err != nil {
		return err
	}
	stream.mu.Lock()
	stream.UpdatedAt = time.Now().UTC()
	subscribers := make([]*StreamSubscriber, 0, len(stream.Subscribers))
	for _, subscriber := range stream.Subscribers {
		subscribers = append(subscribers, subscriber)
	}
	stream.mu.Unlock()

	for _, subscriber := range subscribers {
		if subscriber == nil {
			continue
		}
		select {
		case subscriber.Signal <- struct{}{}:
		default:
		}
	}
	return nil
}

// allowShellUIPublishAfterTerminalLocked 在 stream 已终态时，仍允许投递 shell UI delta
// （调用方必须已持有 stream.mu）。
func allowShellUIPublishAfterTerminalLocked(stream *ActiveStream, event StreamEvent) bool {
	if stream == nil || len(stream.Subscribers) == 0 || event.Message == nil {
		return false
	}
	iu := event.Message.GetInteractionUpdate()
	if iu == nil {
		return false
	}
	return iu.GetToolCallDelta() != nil
}

// ReadFromCursor 返回序号大于 cursorSeq 的未消费 backlog 事件，按 seq 升序，
// 最多 backlogReadBatchSize 条。cursorSeq 是「最后已消费事件的 Seq」，0 表示从头开始
// （含重连 replay）。事件本体从 SQLite 顺序读取，不常驻内存。
func (broker *StreamBroker) ReadFromCursor(requestID string, cursorSeq uint64) ([]StreamEvent, error) {
	if _, ok := broker.Get(requestID); !ok {
		return nil, fmt.Errorf("request is not active: %s", strings.TrimSpace(requestID))
	}
	return broker.store.readAfter(requestID, cursorSeq)
}

// backlogEmpty 判断指定 request 的 backlog 是否为空（用于占位流判定）。
func (broker *StreamBroker) backlogEmpty(requestID string) bool {
	if broker == nil || broker.store == nil {
		return true
	}
	return broker.store.count(requestID) == 0
}

func (broker *StreamBroker) Close() error {
	if broker == nil || broker.store == nil {
		return nil
	}
	return broker.store.close()
}

// Complete 把活动流标记为成功完成，并发布一个成功 endstream 事件。
func (broker *StreamBroker) Complete(requestID string, terminalCode string, terminalMessage string) error {
	stream, ok := broker.Get(requestID)
	if !ok || stream == nil {
		return fmt.Errorf("request is not active: %s", strings.TrimSpace(requestID))
	}
	stream.mu.Lock()
	broker.stopTerminalCleanupTimerLocked(stream)
	stream.Status = StreamStatusCompleted
	subscriberCount := len(stream.Subscribers)
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	if err := broker.Publish(requestID, StreamEvent{
		End:                  true,
		TerminalErrorCode:    strings.TrimSpace(terminalCode),
		TerminalErrorMessage: strings.TrimSpace(terminalMessage),
	}); err != nil {
		stream.mu.Lock()
		stream.Status = StreamStatusFailed
		stream.mu.Unlock()
		return err
	}
	if err := broker.store.flushBarrier(); err != nil {
		stream.mu.Lock()
		stream.Status = StreamStatusFailed
		stream.mu.Unlock()
		return err
	}
	if subscriberCount == 0 {
		broker.scheduleTerminalCleanup(requestID)
	}
	return nil
}

// Fail 把活动流标记为失败，并发布一个失败 endstream 事件。
func (broker *StreamBroker) Fail(requestID string, terminalCode string, terminalMessage string) error {
	stream, ok := broker.Get(requestID)
	if !ok || stream == nil {
		return fmt.Errorf("request is not active: %s", strings.TrimSpace(requestID))
	}
	stream.mu.Lock()
	broker.stopTerminalCleanupTimerLocked(stream)
	stream.Status = StreamStatusFailed
	subscriberCount := len(stream.Subscribers)
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	if err := broker.Publish(requestID, StreamEvent{
		End:                  true,
		TerminalErrorCode:    strings.TrimSpace(terminalCode),
		TerminalErrorMessage: strings.TrimSpace(terminalMessage),
	}); err != nil {
		stream.mu.Lock()
		stream.Status = StreamStatusFailed
		stream.mu.Unlock()
		return err
	}
	if err := broker.store.flushBarrier(); err != nil {
		stream.mu.Lock()
		stream.Status = StreamStatusFailed
		stream.mu.Unlock()
		return err
	}
	if subscriberCount == 0 {
		broker.scheduleTerminalCleanup(requestID)
	}
	return nil
}

// Cancel 主动取消活动流，并发布 canceled endstream。
func (broker *StreamBroker) Cancel(requestID string, terminalMessage string) error {
	stream, ok := broker.Get(requestID)
	if !ok || stream == nil {
		return fmt.Errorf("request is not active: %s", strings.TrimSpace(requestID))
	}
	stream.mu.Lock()
	broker.stopTerminalCleanupTimerLocked(stream)
	if stream.ProviderCancel != nil {
		stream.ProviderCancel()
		stream.ProviderCancel = nil
	}
	stream.ProviderActive = false
	stream.Status = StreamStatusCanceled
	subscriberCount := len(stream.Subscribers)
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	if err := broker.Publish(requestID, StreamEvent{
		End:                  true,
		TerminalErrorCode:    "canceled",
		TerminalErrorMessage: firstNonEmpty(strings.TrimSpace(terminalMessage), "[canceled] User aborted request"),
	}); err != nil {
		return err
	}
	if subscriberCount == 0 {
		broker.scheduleTerminalCleanup(requestID)
	}
	return nil
}

// firstNonEmpty 返回第一个非空白字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
