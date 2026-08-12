package forwarder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cursor/gen/agentv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
	"cursor/internal/logger"
)

const awaitShellOutputLimit = 16 * 1024

const (
	backgroundShellStatusBackgrounded     = "backgrounded"
	backgroundShellStatusRunning          = "running"
	backgroundShellStatusCompleted        = "completed"
	backgroundShellStatusRejected         = "rejected"
	backgroundShellStatusPermissionDenied = "permission_denied"
	backgroundShellStatusTransportClosed  = "transport_closed"
	backgroundShellStatusUnknown          = "unknown"

	backgroundShellActionSourceClient            = "client"
	backgroundShellActionSourceLocalBackgrounded = "local_shell_backgrounded"
)

type awaitShellArgs struct {
	ShellID      string `json:"shell_id,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
	BlockUntilMS *int64 `json:"block_until_ms,omitempty"`
	Pattern      string `json:"pattern,omitempty"`
}

type awaitShellResult struct {
	ShellID        string  `json:"shell_id,omitempty"`
	Status         string  `json:"status"`
	Matched        bool    `json:"matched"`
	TimedOut       bool    `json:"timed_out"`
	ExitCode       *int64  `json:"exit_code,omitempty"`
	Stdout         string  `json:"stdout,omitempty"`
	Stderr         string  `json:"stderr,omitempty"`
	StdoutOffset   int     `json:"stdout_offset,omitempty"`
	StderrOffset   int     `json:"stderr_offset,omitempty"`
	RuntimeMS      uint64  `json:"runtime_ms,omitempty"`
	OutputLength   uint64  `json:"output_length,omitempty"`
	RegexRequested bool    `json:"regex_requested,omitempty"`
	RegexMatch     *string `json:"regex_match,omitempty"`
	Message        string  `json:"message,omitempty"`
}

func (service *Service) handleAwaitShellToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	args, err := decodeAwaitShellArgs(invocation.ArgsJSON)
	if err != nil {
		result := awaitShellResult{Status: "error", Message: err.Error()}
		payload, encodeErr := json.Marshal(result)
		if encodeErr != nil {
			return encodeErr
		}
		return service.completeImmediateToolResult(stream, invocation, string(payload), buildAwaitShellToolCall(buildAwaitArgsFromAwaitShellArgs(args), buildAwaitShellProtoResult(result)))
	}
	result := service.awaitShellSnapshot(stream, args)

	// 当 shell 仍在运行、block_until_ms > 0 且未匹配 pattern 时，延迟交付结果。
	// 客户端已在 handleToolInvocation 中收到 tool_call_started（含 block_until_ms 参数），
	// 因此会显示倒计时。定时器到期后再 snapshot 并交付结果，避免模型 tight-loop 轮询。
	if service.shouldDeferAwaitShellResult(args, result) {
		return service.deferAwaitShellResult(stream, invocation, args)
	}

	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return service.completeImmediateToolResult(stream, invocation, string(payload), buildAwaitShellToolCall(buildAwaitArgsFromAwaitShellArgs(args), buildAwaitShellProtoResult(result)))
}

const awaitShellMaxDeferMS = int64(60_000)
const awaitShellPollInterval = 500 * time.Millisecond

func (service *Service) shouldDeferAwaitShellResult(args awaitShellArgs, result awaitShellResult) bool {
	blockUntilMS := int64(0)
	if args.BlockUntilMS != nil {
		blockUntilMS = *args.BlockUntilMS
	}
	if blockUntilMS <= 0 {
		return false
	}
	if isBackgroundShellTerminalStatus(result.Status) || result.Matched {
		return false
	}
	return true
}

func (service *Service) deferAwaitShellResult(stream *ActiveStream, invocation runtimecore.ToolInvocation, args awaitShellArgs) error {
	blockUntilMS := int64(30000)
	if args.BlockUntilMS != nil && *args.BlockUntilMS > 0 {
		blockUntilMS = *args.BlockUntilMS
	}
	if blockUntilMS > awaitShellMaxDeferMS {
		blockUntilMS = awaitShellMaxDeferMS
	}
	deadline := time.Now().Add(time.Duration(blockUntilMS) * time.Millisecond)
	key := "await_shell:" + strings.TrimSpace(invocation.CallID)
	stream.mu.Lock()
	stream.PendingAwaitShell = &pendingAwaitShell{
		Invocation: invocation,
		Args:       args,
		Deadline:   deadline,
	}
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	logger.Debugf("[shell-debug] AwaitShell defer request_id=%s shell_id=%s block_until_ms=%d poll_interval=%s call_id=%s",
		strings.TrimSpace(stream.RequestID), strings.TrimSpace(args.ShellID), blockUntilMS, awaitShellPollInterval, strings.TrimSpace(invocation.CallID))
	service.scheduleStreamTimer(stream, key, awaitShellPollInterval, streamTimerAwaitShell, "", 0, "await_shell_poll")
	return nil
}

func (service *Service) handleAwaitShellTimerFired(stream *ActiveStream) error {
	stream.mu.Lock()
	pending := stream.PendingAwaitShell
	stream.mu.Unlock()
	if pending == nil {
		return nil
	}
	result := service.awaitShellSnapshot(stream, pending.Args)
	if service.shouldDeferAwaitShellResult(pending.Args, result) && time.Now().Before(pending.Deadline) {
		remaining := time.Until(pending.Deadline)
		nextPoll := awaitShellPollInterval
		if remaining < nextPoll {
			nextPoll = remaining
		}
		key := "await_shell:" + strings.TrimSpace(pending.Invocation.CallID)
		logger.Debugf("[shell-debug] AwaitShell poll reschedule request_id=%s shell_id=%s status=%s remaining=%s next_poll=%s",
			strings.TrimSpace(stream.RequestID), strings.TrimSpace(result.ShellID), result.Status, remaining, nextPoll)
		service.scheduleStreamTimer(stream, key, nextPoll, streamTimerAwaitShell, "", 0, "await_shell_poll")
		return nil
	}
	stream.mu.Lock()
	stream.PendingAwaitShell = nil
	stream.mu.Unlock()
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	logger.Debugf("[shell-debug] AwaitShell deliver request_id=%s shell_id=%s status=%s matched=%v timed_out=%v stdout_len=%d",
		strings.TrimSpace(stream.RequestID), strings.TrimSpace(result.ShellID), result.Status, result.Matched, result.TimedOut, len(result.Stdout))
	return service.completeImmediateToolResult(stream, pending.Invocation, string(payload), buildAwaitShellToolCall(buildAwaitArgsFromAwaitShellArgs(pending.Args), buildAwaitShellProtoResult(result)))
}

func decodeAwaitShellArgs(raw []byte) (awaitShellArgs, error) {
	argsMap, err := runtimecore.DecodeArgsMap(raw)
	if err != nil {
		return awaitShellArgs{}, fmt.Errorf("decode AwaitShell args failed: %w", err)
	}
	result := awaitShellArgs{
		ShellID: strings.TrimSpace(runtimecore.ReadStringArg(argsMap, "shell_id")),
		TaskID:  strings.TrimSpace(runtimecore.ReadStringArg(argsMap, "task_id")),
		Pattern: strings.TrimSpace(runtimecore.ReadStringArg(argsMap, "pattern")),
	}
	logger.Debugf("[shell-debug] decodeAwaitShellArgs raw_len=%d shell_id=%q task_id=%q pattern=%q block_until_ms_present=%v",
		len(raw), result.ShellID, result.TaskID, result.Pattern, result.BlockUntilMS != nil)
	if result.ShellID == "" {
		result.ShellID = result.TaskID
	}
	if value, found, err := runtimecore.ReadInt64Arg(argsMap, "block_until_ms"); err != nil {
		return result, err
	} else if found {
		if value < 0 {
			value = 0
		}
		result.BlockUntilMS = &value
	}
	if result.BlockUntilMS != nil && *result.BlockUntilMS == 0 && strings.TrimSpace(result.ShellID) == "" {
		return result, fmt.Errorf("AwaitShell shell_id is required when block_until_ms is 0")
	}
	logger.Debugf("[shell-debug] decodeAwaitShellArgs raw_len=%d shell_id=%q task_id=%q pattern=%q block_until_ms=%v",
		len(raw), result.ShellID, result.TaskID, result.Pattern, result.BlockUntilMS)
	return result, nil
}

func (service *Service) awaitShellSnapshot(stream *ActiveStream, args awaitShellArgs) awaitShellResult {
	blockUntilMS := int64(30000)
	if args.BlockUntilMS != nil {
		blockUntilMS = *args.BlockUntilMS
	}
	shellID := strings.TrimSpace(args.ShellID)
	if shellID == "" {
		return awaitShellResult{
			Status:   "waited",
			TimedOut: false,
			Message:  fmt.Sprintf("waited %dms", blockUntilMS),
		}
	}

	service.refreshBackgroundShellFromTerminalFile(stream, shellID)

	stream.mu.Lock()
	state, ok := stream.BackgroundShells[shellID]
	if !ok || state == nil {
		stream.mu.Unlock()
		logger.Debugf("[shell-debug] AwaitShell unknown_shell request_id=%s shell_id=%s", strings.TrimSpace(stream.RequestID), shellID)
		return awaitShellResult{
			ShellID:  shellID,
			Status:   backgroundShellStatusUnknown,
			TimedOut: false,
			Message:  "unknown or expired shell_id",
		}
	}
	uiToolCallID := strings.TrimSpace(state.OriginalToolCallID)
	uiModelCallID := strings.TrimSpace(state.ModelCallID)
	uiArgsJSON := append([]byte(nil), state.ArgsJSON...)
	uiDelta := takeBackgroundShellUIStdoutDeltaLocked(state)
	uiEnsureStarted := false
	if uiDelta != "" && uiToolCallID != "" && !state.UIStartedEnsured {
		state.UIStartedEnsured = true
		uiEnsureStarted = true
	}
	stdoutStart := clampOffset(state.AwaitStdoutOffset, len(state.StdoutBuffer))
	stderrStart := clampOffset(state.AwaitStderrOffset, len(state.StderrBuffer))
	stdout := state.StdoutBuffer[stdoutStart:]
	stderr := state.StderrBuffer[stderrStart:]
	stdoutEnd := len(state.StdoutBuffer)
	stderrEnd := len(state.StderrBuffer)
	state.AwaitStdoutOffset = stdoutEnd
	state.AwaitStderrOffset = stderrEnd
	status := strings.TrimSpace(state.Status)
	if status == "" {
		status = backgroundShellStatusUnknown
	}
	var exitCode *int64
	if state.ExitCode != nil {
		value := int64(*state.ExitCode)
		exitCode = &value
	}
	createdAt := state.CreatedAt
	completedAt := state.CompletedAt
	combinedOutput := state.StdoutBuffer + "\n" + state.StderrBuffer
	requestID := strings.TrimSpace(stream.RequestID)
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()

	if uiDelta != "" && uiToolCallID != "" {
		_ = service.publishShellToolCallUIDeltas(requestID, uiToolCallID, uiModelCallID, uiArgsJSON, uiDelta, "stdout", uiEnsureStarted, "await_shell")
	} else if uiDelta != "" && uiToolCallID == "" {
		logger.Debugf("[shell-debug] AwaitShell UI delta dropped missing_tool_call_id request_id=%s shell_id=%s delta_len=%d",
			requestID, shellID, len(uiDelta))
	}

	logger.Debugf("[shell-debug] AwaitShell request_id=%s shell_id=%s status=%s block_until_ms=%d stdout_offset=%d->%d stderr_offset=%d->%d new_stdout_len=%d new_stderr_len=%d total_stdout_len=%d total_stderr_len=%d stdout_tail=%q",
		strings.TrimSpace(stream.RequestID), shellID, status, blockUntilMS,
		stdoutStart, stdoutEnd, stderrStart, stderrEnd,
		len(stdout), len(stderr), stdoutEnd, stderrEnd,
		truncateForLog(stdout, 500))

	matched, matchText, patternErr := awaitShellPatternMatched(args.Pattern, combinedOutput)
	message := ""
	if patternErr != nil {
		message = patternErr.Error()
	}
	timedOut := blockUntilMS > 0 && !matched && !isBackgroundShellTerminalStatus(status)
	stdout = truncateAwaitShellOutput(stdout)
	stderr = truncateAwaitShellOutput(stderr)

	if len(stdout) > 0 || len(stderr) > 0 || isBackgroundShellTerminalStatus(status) {
		logger.Debugf("[shell-debug] AwaitShell result request_id=%s shell_id=%s status=%s matched=%v timed_out=%v exit_code=%v stdout_returned=%d stderr_returned=%d stdout_tail=%q",
			strings.TrimSpace(stream.RequestID), shellID, status, matched, timedOut, exitCode,
			len(stdout), len(stderr), truncateForLog(stdout, 500))
	}

	return awaitShellResult{
		ShellID:        shellID,
		Status:         status,
		Matched:        matched,
		TimedOut:       timedOut,
		ExitCode:       exitCode,
		Stdout:         stdout,
		Stderr:         stderr,
		StdoutOffset:   stdoutEnd,
		StderrOffset:   stderrEnd,
		RuntimeMS:      backgroundShellRuntimeMS(createdAt, completedAt),
		OutputLength:   uint64(len(combinedOutput)),
		RegexRequested: strings.TrimSpace(args.Pattern) != "",
		RegexMatch:     matchText,
		Message:        message,
	}
}

func buildAwaitArgsFromAwaitShellArgs(args awaitShellArgs) *agentv1.AwaitArgs {
	awaitArgs := &agentv1.AwaitArgs{TaskId: strings.TrimSpace(firstNonEmpty(args.ShellID, args.TaskID))}
	if args.BlockUntilMS != nil {
		value := uint32(0)
		if *args.BlockUntilMS > 0 {
			value = uint32(*args.BlockUntilMS)
		}
		awaitArgs.BlockUntilMs = &value
	}
	if pattern := strings.TrimSpace(args.Pattern); pattern != "" {
		awaitArgs.Regex = &pattern
	}
	return awaitArgs
}

func buildAwaitShellToolCall(args *agentv1.AwaitArgs, result *agentv1.AwaitResult) *agentv1.ToolCall {
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_AwaitToolCall{
			AwaitToolCall: &agentv1.AwaitToolCall{
				Args:   args,
				Result: result,
			},
		},
	}
}

func buildAwaitShellProtoResult(result awaitShellResult) *agentv1.AwaitResult {
	switch strings.TrimSpace(result.Status) {
	case "error", backgroundShellStatusUnknown:
		message := firstNonEmpty(strings.TrimSpace(result.Message), "unknown or expired shell_id")
		return &agentv1.AwaitResult{Result: &agentv1.AwaitResult_Error{Error: &agentv1.AwaitError{Error: message}}}
	}
	if isBackgroundShellTerminalStatus(result.Status) || result.Matched {
		task := &agentv1.AwaitTaskComplete{
			TaskId:         strings.TrimSpace(result.ShellID),
			RuntimeMs:      result.RuntimeMS,
			OutputLength:   result.OutputLength,
			RegexRequested: result.RegexRequested,
			RegexMatch:     result.RegexMatch,
		}
		if result.ExitCode != nil {
			exitCode := int32(*result.ExitCode)
			task.ExitCode = &exitCode
		}
		return &agentv1.AwaitResult{Result: &agentv1.AwaitResult_Complete{Complete: task}}
	}
	task := &agentv1.AwaitTaskStillRunning{
		TaskId:         strings.TrimSpace(result.ShellID),
		RuntimeMs:      result.RuntimeMS,
		OutputLength:   result.OutputLength,
		RegexRequested: result.RegexRequested,
		RegexMatch:     result.RegexMatch,
	}
	return &agentv1.AwaitResult{Result: &agentv1.AwaitResult_StillRunning{StillRunning: task}}
}

func backgroundShellRuntimeMS(createdAt time.Time, completedAt time.Time) uint64 {
	if createdAt.IsZero() {
		return 0
	}
	end := completedAt
	if end.IsZero() {
		end = time.Now().UTC()
	}
	if end.Before(createdAt) {
		return 0
	}
	return uint64(end.Sub(createdAt).Milliseconds())
}

func awaitShellPatternMatched(pattern string, output string) (bool, *string, error) {
	trimmed := strings.TrimSpace(pattern)
	if trimmed == "" {
		return false, nil, nil
	}
	expr, err := regexp.Compile("(?m)" + trimmed)
	if err != nil {
		return false, nil, fmt.Errorf("invalid AwaitShell pattern: %w", err)
	}
	match := expr.FindString(output)
	if match == "" {
		return false, nil, nil
	}
	return true, &match, nil
}

func truncateAwaitShellOutput(value string) string {
	if len(value) <= awaitShellOutputLimit {
		return value
	}
	return value[len(value)-awaitShellOutputLimit:]
}

func clampOffset(offset int, length int) int {
	if offset < 0 {
		return 0
	}
	if offset > length {
		return length
	}
	return offset
}

type terminalShellFileSnapshot struct {
	PID       *uint32
	Command   string
	CWD       string
	StartedAt time.Time
	Output    string
	ExitCode  *int32
	EndedAt   time.Time
}

func (service *Service) refreshBackgroundShellFromTerminalFile(stream *ActiveStream, shellID string) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	terminalsFolder := strings.TrimSpace(stream.TerminalsFolder)
	requestID := strings.TrimSpace(stream.RequestID)
	stream.mu.Unlock()
	terminalSnapshot, ok := readTerminalShellFileSnapshot(terminalsFolder, shellID)
	if !ok {
		logger.Debugf("[shell-debug] TerminalFileReadFail request_id=%s shell_id=%s terminals_folder=%q",
			requestID, shellID, terminalsFolder)
		return
	}
	now := time.Now().UTC()
	stream.mu.Lock()
	defer stream.mu.Unlock()
	state := ensureBackgroundShellStateLocked(stream, shellID, runtimecore.PendingExec{}, firstNonZeroTime(terminalSnapshot.StartedAt, now))
	if state == nil {
		return
	}
	if state.Command == "" {
		state.Command = terminalSnapshot.Command
	}
	if state.WorkingDirectory == "" {
		state.WorkingDirectory = terminalSnapshot.CWD
	}
	if state.PID == nil && terminalSnapshot.PID != nil {
		pid := *terminalSnapshot.PID
		state.PID = &pid
	}
	if state.CreatedAt.IsZero() {
		state.CreatedAt = firstNonZeroTime(terminalSnapshot.StartedAt, now)
	}
	// 终端文件是完整快照：始终以其为 stdout 真源，才能在后台任务运行期间持续增长。
	// 旧逻辑只在 buffer 全空时写入一次，导致 AwaitShell / UI 轮询拿不到后续行。
	beforeStdoutLen := len(state.StdoutBuffer)
	beforeUIOffset := state.UIStdoutOffset
	grew := false
	if terminalSnapshot.Output != "" {
		grew = mergeBackgroundShellStdoutFromTerminalOutput(state, terminalSnapshot.Output)
		if !isBackgroundShellTerminalStatus(state.Status) {
			state.Status = backgroundShellStatusRunning
		}
		state.LastActivityAt = now
	}
	if !isBackgroundShellTerminalStatus(state.Status) && terminalSnapshot.ExitCode != nil {
		exitCode := *terminalSnapshot.ExitCode
		state.ExitCode = &exitCode
		state.Status = backgroundShellStatusCompleted
		state.CompletedAt = firstNonZeroTime(terminalSnapshot.EndedAt, now)
		state.LastActivityAt = state.CompletedAt
	}
	if strings.TrimSpace(state.Status) == "" {
		state.Status = backgroundShellStatusBackgrounded
	}
	if state.LastActivityAt.IsZero() {
		state.LastActivityAt = now
	}
	afterStdoutLen := len(state.StdoutBuffer)
	if grew || afterStdoutLen != beforeStdoutLen || terminalSnapshot.ExitCode != nil {
		logger.Debugf("[shell-debug] TerminalFileMerged request_id=%s shell_id=%s status=%s stdout_len=%d->%d grew=%v file_output_len=%d ui_stdout_offset=%d->%d stderr_len=%d exit_code=%v",
			strings.TrimSpace(stream.RequestID), shellID, state.Status,
			beforeStdoutLen, afterStdoutLen, grew, len(terminalSnapshot.Output),
			beforeUIOffset, state.UIStdoutOffset, len(state.StderrBuffer), state.ExitCode)
	}
	stream.UpdatedAt = now
}

func takeBackgroundShellUIStdoutDeltaLocked(state *BackgroundShellState) string {
	if state == nil {
		return ""
	}
	if state.UIStdoutOffset < 0 {
		state.UIStdoutOffset = 0
	}
	if state.UIStdoutOffset > len(state.StdoutBuffer) {
		state.UIStdoutOffset = len(state.StdoutBuffer)
	}
	delta := state.StdoutBuffer[state.UIStdoutOffset:]
	state.UIStdoutOffset = len(state.StdoutBuffer)
	return delta
}

func mergeBackgroundShellStdoutFromTerminalOutput(state *BackgroundShellState, output string) bool {
	if state == nil || output == "" {
		return false
	}
	before := len(state.StdoutBuffer)
	if state.StdoutBuffer == "" {
		state.StdoutBuffer = output
		return len(state.StdoutBuffer) > before
	}
	if strings.HasPrefix(output, state.StdoutBuffer) || len(output) >= len(state.StdoutBuffer) {
		state.StdoutBuffer = output
		if state.UIStdoutOffset > len(state.StdoutBuffer) {
			state.UIStdoutOffset = len(state.StdoutBuffer)
		}
	} else {
		logger.Debugf("[shell-debug] TerminalOutputMergeSkipped shell_id=%s existing_len=%d new_output_len=%d (shorter and not a prefix — possible terminal file truncation)",
			state.ShellID, len(state.StdoutBuffer), len(output))
	}
	return len(state.StdoutBuffer) > before
}

func backgroundShellUIPollerKey(requestID string, shellID string) string {
	return strings.TrimSpace(requestID) + "\x00" + strings.TrimSpace(shellID)
}

func (service *Service) scheduleBackgroundShellUIPoll(requestID string, shellID string) {
	if service == nil {
		return
	}
	requestID = strings.TrimSpace(requestID)
	shellID = strings.TrimSpace(shellID)
	if requestID == "" || shellID == "" {
		logger.Debugf("[shell-debug] BackgroundShellUI schedule skip reason=missing_ids request_id=%q shell_id=%q",
			requestID, shellID)
		return
	}
	if _, ok := service.broker.Get(requestID); !ok {
		logger.Debugf("[shell-debug] BackgroundShellUI schedule skip reason=stream_missing request_id=%s shell_id=%s",
			requestID, shellID)
		return
	}
	key := backgroundShellUIPollerKey(requestID, shellID)
	if _, loaded := service.backgroundShellUIPollers.LoadOrStore(key, true); loaded {
		logger.Debugf("[shell-debug] BackgroundShellUI schedule skip reason=already_running request_id=%s shell_id=%s",
			requestID, shellID)
		return
	}
	logger.Debugf("[shell-debug] BackgroundShellUI schedule request_id=%s shell_id=%s interval_ms=%d mode=detached_goroutine",
		requestID, shellID, int(backgroundShellUIPollInterval/time.Millisecond))
	go service.runBackgroundShellUIPoller(requestID, shellID, key)
}

func (service *Service) runBackgroundShellUIPoller(requestID string, shellID string, key string) {
	defer service.backgroundShellUIPollers.Delete(key)
	for {
		timer := time.NewTimer(backgroundShellUIPollInterval)
		<-timer.C
		timer.Stop()

		stream, ok := service.broker.Get(requestID)
		if !ok || stream == nil {
			logger.Debugf("[shell-debug] BackgroundShellUI poll stop request_id=%s shell_id=%s reason=stream_missing",
				requestID, shellID)
			return
		}
		cont, err := service.pollBackgroundShellUI(stream, shellID)
		if err != nil {
			logger.Debugf("[shell-debug] BackgroundShellUI poll stop request_id=%s shell_id=%s reason=publish_error err=%v",
				requestID, shellID, err)
			return
		}
		if !cont {
			return
		}
	}
}

func (service *Service) pollBackgroundShellUI(stream *ActiveStream, shellID string) (bool, error) {
	if stream == nil {
		logger.Debugf("[shell-debug] BackgroundShellUI poll skip reason=stream_nil shell_id=%s", shellID)
		return false, nil
	}
	shellID = strings.TrimSpace(shellID)
	if shellID == "" {
		logger.Debugf("[shell-debug] BackgroundShellUI poll skip reason=empty_shell_id request_id=%s",
			strings.TrimSpace(stream.RequestID))
		return false, nil
	}
	service.refreshBackgroundShellFromTerminalFile(stream, shellID)

	stream.mu.Lock()
	state := stream.BackgroundShells[shellID]
	var (
		toolCallID    string
		modelCallID   string
		argsJSON      []byte
		delta         string
		status        string
		terminal      bool
		tick          int
		stdoutLen     int
		uiOffset      int
		ensureStarted bool
		stateNil      bool
		subscribers   int
	)
	if state == nil {
		stateNil = true
	} else {
		state.UIPollTicks++
		tick = state.UIPollTicks
		toolCallID = strings.TrimSpace(state.OriginalToolCallID)
		modelCallID = strings.TrimSpace(state.ModelCallID)
		argsJSON = append([]byte(nil), state.ArgsJSON...)
		stdoutLen = len(state.StdoutBuffer)
		uiOffset = state.UIStdoutOffset
		delta = takeBackgroundShellUIStdoutDeltaLocked(state)
		if delta != "" && toolCallID != "" && !state.UIStartedEnsured {
			state.UIStartedEnsured = true
			ensureStarted = true
		}
		status = strings.TrimSpace(state.Status)
		terminal = isBackgroundShellTerminalStatus(status)
	}
	requestID := strings.TrimSpace(stream.RequestID)
	streamStatus := stream.Status
	subscribers = len(stream.Subscribers)
	stream.mu.Unlock()

	if stateNil {
		logger.Debugf("[shell-debug] BackgroundShellUI poll state_nil request_id=%s shell_id=%s stream_status=%s",
			requestID, shellID, streamStatus)
	} else if delta != "" && toolCallID == "" {
		logger.Debugf("[shell-debug] BackgroundShellUI delta dropped missing_tool_call_id request_id=%s shell_id=%s delta_len=%d status=%s tick=%d stdout_len=%d ui_offset=%d",
			requestID, shellID, len(delta), status, tick, stdoutLen, uiOffset)
	} else if delta != "" && toolCallID != "" {
		logger.Debugf("[shell-debug] BackgroundShellUI delta request_id=%s shell_id=%s tool_call_id=%s delta_len=%d status=%s tick=%d stdout_len=%d ui_offset_before=%d ensure_started=%v",
			requestID, shellID, toolCallID, len(delta), status, tick, stdoutLen, uiOffset, ensureStarted)
		if err := service.publishShellToolCallUIDeltas(requestID, toolCallID, modelCallID, argsJSON, delta, "stdout", ensureStarted, "background_poll"); err != nil {
			return false, err
		}
	} else if tick == 1 || tick%20 == 0 {
		// 空转降噪：首 tick + 每约 5s（250ms*20）打一次 heartbeat，确认轮询仍在跑。
		logger.Debugf("[shell-debug] BackgroundShellUI poll idle request_id=%s shell_id=%s tool_call_id=%s status=%s tick=%d stdout_len=%d ui_offset=%d",
			requestID, shellID, toolCallID, status, tick, stdoutLen, uiOffset)
	}
	if terminal {
		logger.Debugf("[shell-debug] BackgroundShellUI poll stop request_id=%s shell_id=%s status=%s stream_status=%s tick=%d",
			requestID, shellID, status, streamStatus, tick)
		return false, nil
	}
	// turn/cancel 后若仍有订阅者，继续推 UI delta；无订阅者再停。
	if isTerminalStreamStatus(streamStatus) && subscribers == 0 {
		logger.Debugf("[shell-debug] BackgroundShellUI poll stop request_id=%s shell_id=%s status=%s stream_status=%s tick=%d reason=stream_terminal_no_subscribers",
			requestID, shellID, status, streamStatus, tick)
		return false, nil
	}
	return true, nil
}

func readTerminalShellFileSnapshot(terminalsFolder string, shellID string) (terminalShellFileSnapshot, bool) {
	path, ok := terminalShellFilePath(terminalsFolder, shellID)
	if !ok {
		logger.Debugf("[shell-debug] TerminalFilePathInvalid terminals_folder=%q shell_id=%q", terminalsFolder, shellID)
		return terminalShellFileSnapshot{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		logger.Debugf("[shell-debug] TerminalFileReadError path=%q err=%v", path, err)
		return terminalShellFileSnapshot{}, false
	}
	if len(data) == 0 {
		logger.Debugf("[shell-debug] TerminalFileEmpty path=%q", path)
		return terminalShellFileSnapshot{}, false
	}
	snapshot := parseTerminalShellFileSnapshot(string(data))
	logger.Debugf("[shell-debug] TerminalFileParsed path=%q raw_len=%d output_len=%d output_tail=%q",
		path, len(data), len(snapshot.Output), truncateForLog(snapshot.Output, 300))
	return snapshot, true
}

func terminalShellFilePath(terminalsFolder string, shellID string) (string, bool) {
	folder := filepath.Clean(strings.TrimSpace(terminalsFolder))
	if folder == "." || !filepath.IsAbs(folder) {
		return "", false
	}
	id, err := strconv.ParseUint(strings.TrimSpace(shellID), 10, 32)
	if err != nil {
		return "", false
	}
	filename := strconv.FormatUint(id, 10) + ".txt"
	return filepath.Join(folder, filename), true
}

func parseTerminalShellFileSnapshot(raw string) terminalShellFileSnapshot {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	separators := terminalShellSeparatorIndexes(lines)
	snapshot := terminalShellFileSnapshot{}
	if len(separators) >= 2 {
		parseTerminalShellMetadata(lines[separators[0]+1:separators[1]], &snapshot)
	}
	outputStart := 0
	if len(separators) >= 2 {
		outputStart = separators[1] + 1
	}
	outputEnd := len(lines)
	for index := len(separators) - 2; index >= 1; index-- {
		block := lines[separators[index]+1 : separators[index+1]]
		if terminalMetadataBlockHasKey(block, "exit_code") || terminalMetadataBlockHasKey(block, "ended_at") {
			parseTerminalShellMetadata(block, &snapshot)
			outputEnd = separators[index]
			break
		}
	}
	if outputStart < outputEnd {
		snapshot.Output = strings.Join(lines[outputStart:outputEnd], "\n")
		snapshot.Output = strings.TrimSuffix(snapshot.Output, "\n")
	}
	return snapshot
}

func terminalShellSeparatorIndexes(lines []string) []int {
	indexes := make([]int, 0, 4)
	for index, line := range lines {
		if strings.TrimSpace(line) == "---" {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func parseTerminalShellMetadata(lines []string, snapshot *terminalShellFileSnapshot) {
	if snapshot == nil {
		return
	}
	for _, line := range lines {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = parseTerminalShellMetadataValue(value)
		switch key {
		case "pid":
			if parsed, err := strconv.ParseUint(value, 10, 32); err == nil {
				pid := uint32(parsed)
				snapshot.PID = &pid
			}
		case "cwd":
			snapshot.CWD = value
		case "command":
			snapshot.Command = value
		case "started_at":
			snapshot.StartedAt = parseTerminalShellTime(value)
		case "exit_code":
			if parsed, err := strconv.ParseInt(value, 10, 32); err == nil {
				exitCode := int32(parsed)
				snapshot.ExitCode = &exitCode
			}
		case "ended_at":
			snapshot.EndedAt = parseTerminalShellTime(value)
		}
	}
}

func parseTerminalShellMetadataValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if unquoted, err := strconv.Unquote(trimmed); err == nil {
		return unquoted
	}
	return trimmed
}

func parseTerminalShellTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func terminalMetadataBlockHasKey(lines []string, key string) bool {
	for _, line := range lines {
		current, _, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(current) == key {
			return true
		}
	}
	return false
}

func isBackgroundShellTerminalStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case backgroundShellStatusCompleted, backgroundShellStatusRejected, backgroundShellStatusPermissionDenied, backgroundShellStatusTransportClosed:
		return true
	default:
		return false
	}
}

func (service *Service) observeBackgroundShellExecClientMessage(stream *ActiveStream, pending runtimecore.PendingExec, message *agentv1.ExecClientMessage) {
	if stream == nil || message == nil {
		return
	}
	now := time.Now().UTC()
	stream.mu.Lock()
	defer stream.mu.Unlock()
	service.observeBackgroundShellSpawnResultLocked(stream, pending, message.GetBackgroundShellSpawnResult(), now)
	service.observeForceBackgroundShellResultLocked(stream, pending, message.GetForceBackgroundShellResult(), now)
}

func (service *Service) observeMissingBackgroundShellExecClientMessage(stream *ActiveStream, message *agentv1.ExecClientMessage) bool {
	if stream == nil || message == nil {
		return false
	}
	if message.GetBackgroundShellSpawnResult() == nil && message.GetForceBackgroundShellResult() == nil {
		return false
	}
	now := time.Now().UTC()
	stream.mu.Lock()
	defer stream.mu.Unlock()
	pending := runtimecore.PendingExec{
		MessageID: message.GetId(),
		ExecID:    strings.TrimSpace(message.GetExecId()),
		ExecKind:  "shell",
	}
	return service.observeBackgroundShellSpawnResultLocked(stream, pending, message.GetBackgroundShellSpawnResult(), now) || service.observeForceBackgroundShellResultLocked(stream, pending, message.GetForceBackgroundShellResult(), now)
}

func (service *Service) observeBackgroundShellSpawnResultLocked(stream *ActiveStream, pending runtimecore.PendingExec, result *agentv1.BackgroundShellSpawnResult, now time.Time) bool {
	if stream == nil || result == nil {
		return false
	}
	if stream.BackgroundShells == nil {
		stream.BackgroundShells = make(map[string]*BackgroundShellState)
	}
	if stream.BackgroundShellsByMessageID == nil {
		stream.BackgroundShellsByMessageID = make(map[uint32]string)
	}
	if stream.BackgroundShellsByExecID == nil {
		stream.BackgroundShellsByExecID = make(map[string]string)
	}
	success := result.GetSuccess()
	if success != nil {
		shellID := strconv.FormatUint(uint64(success.GetShellId()), 10)
		state := stream.BackgroundShells[shellID]
		if state == nil {
			state = &BackgroundShellState{ShellID: shellID, CreatedAt: now}
			stream.BackgroundShells[shellID] = state
		}
		pid := success.Pid
		state.Command = firstNonEmpty(strings.TrimSpace(success.GetCommand()), state.Command)
		state.WorkingDirectory = firstNonEmpty(strings.TrimSpace(success.GetWorkingDirectory()), state.WorkingDirectory)
		state.PID = pid
		state.Status = backgroundShellStatusBackgrounded
		state.OriginalToolCallID = firstNonEmpty(strings.TrimSpace(pending.ToolCallID), state.OriginalToolCallID)
		state.OriginalExecID = firstNonEmpty(strings.TrimSpace(pending.ExecID), state.OriginalExecID)
		if state.OriginalMessageID == 0 {
			state.OriginalMessageID = pending.MessageID
		}
		if len(state.ArgsJSON) == 0 && len(pending.ArgsJSON) > 0 {
			state.ArgsJSON = append([]byte(nil), pending.ArgsJSON...)
		}
		state.ModelCallID = firstNonEmpty(strings.TrimSpace(pending.ModelCallID), state.ModelCallID)
		state.LastActivityAt = now
		if pending.MessageID != 0 {
			stream.BackgroundShellsByMessageID[pending.MessageID] = shellID
		}
		if strings.TrimSpace(pending.ExecID) != "" {
			stream.BackgroundShellsByExecID[strings.TrimSpace(pending.ExecID)] = shellID
		}
		stream.UpdatedAt = now
		return true
	}
	shellID := backgroundShellIDForMessageLocked(stream, pending.MessageID, pending.ExecID)
	if shellID == "" {
		return false
	}
	state := ensureBackgroundShellStateLocked(stream, shellID, pending, now)
	if state == nil {
		return false
	}
	switch {
	case result.GetRejected() != nil:
		state.Status = backgroundShellStatusRejected
		state.StderrBuffer += strings.TrimSpace(result.GetRejected().GetReason())
	case result.GetPermissionDenied() != nil:
		state.Status = backgroundShellStatusPermissionDenied
		state.StderrBuffer += strings.TrimSpace(result.GetPermissionDenied().GetError())
	case result.GetError() != nil:
		state.Status = backgroundShellStatusTransportClosed
		state.StderrBuffer += strings.TrimSpace(result.GetError().GetError())
	default:
		return false
	}
	state.LastActivityAt = now
	state.CompletedAt = now
	stream.UpdatedAt = now
	return true
}

func (service *Service) observeForceBackgroundShellResultLocked(stream *ActiveStream, pending runtimecore.PendingExec, result *agentv1.ForceBackgroundShellResult, now time.Time) bool {
	if stream == nil || result == nil || result.GetShellResult() == nil {
		return false
	}
	shellResult := result.GetShellResult()
	shellIDValue := uint32(0)
	if success := shellResult.GetSuccess(); success != nil {
		shellIDValue = success.GetShellId()
	}
	if shellIDValue == 0 {
		return false
	}
	shellID := strconv.FormatUint(uint64(shellIDValue), 10)
	state := ensureBackgroundShellStateLocked(stream, shellID, pending, now)
	if state == nil {
		return false
	}
	state.Status = backgroundShellStatusBackgrounded
	state.LastActivityAt = now
	stream.UpdatedAt = now
	return true
}

func (service *Service) observeShellExecClientMessage(stream *ActiveStream, pending runtimecore.PendingExec, message *agentv1.ExecClientMessage) {
	if stream == nil || message == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return
	}
	shellStream := message.GetShellStream()
	if shellStream == nil {
		return
	}
	now := time.Now().UTC()
	stream.mu.Lock()
	defer stream.mu.Unlock()
	service.observeShellStreamLocked(stream, pending, shellStream, now)
}

func (service *Service) observeMissingShellExecClientMessage(stream *ActiveStream, message *agentv1.ExecClientMessage) bool {
	if stream == nil || message == nil || message.GetShellStream() == nil {
		return false
	}
	now := time.Now().UTC()
	stream.mu.Lock()
	defer stream.mu.Unlock()
	shellID := backgroundShellIDForMessageLocked(stream, message.GetId(), message.GetExecId())
	if shellID == "" {
		return false
	}
	pending := runtimecore.PendingExec{
		MessageID: message.GetId(),
		ExecID:    strings.TrimSpace(message.GetExecId()),
		ExecKind:  "shell",
	}
	if state := stream.BackgroundShells[shellID]; state != nil {
		pending.ToolCallID = state.OriginalToolCallID
		pending.ArgsJSON = append([]byte(nil), state.ArgsJSON...)
		pending.ModelCallID = state.ModelCallID
	}
	service.observeShellStreamLocked(stream, pending, message.GetShellStream(), now)
	return true
}

func (service *Service) observeShellStreamLocked(stream *ActiveStream, pending runtimecore.PendingExec, shellStream *agentv1.ShellStream, now time.Time) {
	if stream.BackgroundShells == nil {
		stream.BackgroundShells = make(map[string]*BackgroundShellState)
	}
	if stream.BackgroundShellsByMessageID == nil {
		stream.BackgroundShellsByMessageID = make(map[uint32]string)
	}
	if stream.BackgroundShellsByExecID == nil {
		stream.BackgroundShellsByExecID = make(map[string]string)
	}

	shellID := backgroundShellIDForMessageLocked(stream, pending.MessageID, pending.ExecID)
	switch event := shellStream.GetEvent().(type) {
	case *agentv1.ShellStream_Start:
		// 前台 Start 不进入 BackgroundShellState；显式忽略避免 UnknownShellEvent 噪音。
		return
	case *agentv1.ShellStream_Backgrounded:
		shellID = strconv.FormatUint(uint64(event.Backgrounded.GetShellId()), 10)
		state := stream.BackgroundShells[shellID]
		if state == nil {
			state = &BackgroundShellState{ShellID: shellID, CreatedAt: now}
			stream.BackgroundShells[shellID] = state
		}
		state.Command = firstNonEmpty(strings.TrimSpace(event.Backgrounded.GetCommand()), state.Command)
		state.WorkingDirectory = firstNonEmpty(strings.TrimSpace(event.Backgrounded.GetWorkingDirectory()), state.WorkingDirectory)
		state.PID = event.Backgrounded.Pid
		state.Status = backgroundShellStatusBackgrounded
		state.OriginalToolCallID = strings.TrimSpace(pending.ToolCallID)
		state.OriginalExecID = strings.TrimSpace(pending.ExecID)
		state.OriginalMessageID = pending.MessageID
		state.ArgsJSON = append([]byte(nil), pending.ArgsJSON...)
		state.ModelCallID = strings.TrimSpace(pending.ModelCallID)
		// 前台阶段已通过 tool_call_delta 推过的输出：并入 buffer，并把 UI 偏移设到末尾，避免后台轮询重复推送。
		if state.StdoutBuffer == "" && strings.TrimSpace(pending.StdoutBuffer) != "" {
			state.StdoutBuffer = pending.StdoutBuffer
		}
		if state.StderrBuffer == "" && strings.TrimSpace(pending.StderrBuffer) != "" {
			state.StderrBuffer = pending.StderrBuffer
		}
		if state.UIStdoutOffset < len(state.StdoutBuffer) {
			state.UIStdoutOffset = len(state.StdoutBuffer)
		}
		if state.UIStderrOffset < len(state.StderrBuffer) {
			state.UIStderrOffset = len(state.StderrBuffer)
		}
		// OpenExec 时已发过 tool_call_started；background 后客户端靠
		// startBackgroundTerminalStreaming(shellId) 订阅终端。
		// 若此处 UIStartedEnsured=false，轮询首包会再次 ensure started，
		// 发生在 tool_call_completed 之后，会打乱 bubble / 打断终端订阅。
		state.UIStartedEnsured = true
		state.LastActivityAt = now
		if pending.MessageID != 0 {
			stream.BackgroundShellsByMessageID[pending.MessageID] = shellID
		}
		if strings.TrimSpace(pending.ExecID) != "" {
			stream.BackgroundShellsByExecID[strings.TrimSpace(pending.ExecID)] = shellID
		}
		logger.Debugf("[shell-debug] Backgrounded request_id=%s shell_id=%s exec_id=%s command=%q cwd=%q pid=%v ui_stdout_offset=%d ui_started_ensured=%v",
			strings.TrimSpace(stream.RequestID), shellID, strings.TrimSpace(pending.ExecID),
			strings.TrimSpace(event.Backgrounded.GetCommand()),
			strings.TrimSpace(event.Backgrounded.GetWorkingDirectory()), event.Backgrounded.GetPid(),
			state.UIStdoutOffset, state.UIStartedEnsured)
	case *agentv1.ShellStream_Stdout:
		state := ensureBackgroundShellStateLocked(stream, shellID, pending, now)
		if state == nil {
			logger.Debugf("[shell-debug] Stdout state_nil_drop request_id=%s shell_id=%s exec_id=%s",
				strings.TrimSpace(stream.RequestID), shellID, strings.TrimSpace(pending.ExecID))
			return
		}
		decodedStdout := execbridge.DecodeShellStdout(event.Stdout)
		state.StdoutBuffer += decodedStdout
		state.Status = backgroundShellStatusRunning
		state.LastActivityAt = now
		logger.Debugf("[shell-debug] Stdout request_id=%s shell_id=%s exec_id=%s delta_len=%d total_stdout_len=%d delta_content=%q",
			strings.TrimSpace(stream.RequestID), shellID, strings.TrimSpace(pending.ExecID),
			len(decodedStdout), len(state.StdoutBuffer),
			truncateForLog(decodedStdout, 500))
	case *agentv1.ShellStream_Stderr:
		state := ensureBackgroundShellStateLocked(stream, shellID, pending, now)
		if state == nil {
			logger.Debugf("[shell-debug] Stderr state_nil_drop request_id=%s shell_id=%s exec_id=%s",
				strings.TrimSpace(stream.RequestID), shellID, strings.TrimSpace(pending.ExecID))
			return
		}
		stderrData := event.Stderr.GetData()
		state.StderrBuffer += stderrData
		state.Status = backgroundShellStatusRunning
		state.LastActivityAt = now
		logger.Debugf("[shell-debug] Stderr request_id=%s shell_id=%s exec_id=%s delta_len=%d total_stderr_len=%d delta_content=%q",
			strings.TrimSpace(stream.RequestID), shellID, strings.TrimSpace(pending.ExecID),
			len(stderrData), len(state.StderrBuffer),
			truncateForLog(stderrData, 500))
	case *agentv1.ShellStream_Exit:
		state := ensureBackgroundShellStateLocked(stream, shellID, pending, now)
		if state == nil {
			logger.Debugf("[shell-debug] Exit state_nil_drop request_id=%s shell_id=%s exec_id=%s",
				strings.TrimSpace(stream.RequestID), shellID, strings.TrimSpace(pending.ExecID))
			return
		}
		exitCode := int32(event.Exit.GetCode())
		state.ExitCode = &exitCode
		state.WorkingDirectory = firstNonEmpty(strings.TrimSpace(event.Exit.GetCwd()), state.WorkingDirectory)
		state.Status = backgroundShellStatusCompleted
		state.LastActivityAt = now
		state.CompletedAt = now
		logger.Debugf("[shell-debug] Exit request_id=%s shell_id=%s exec_id=%s exit_code=%d stdout_total=%d stderr_total=%d stdout_tail=%q",
			strings.TrimSpace(stream.RequestID), shellID, strings.TrimSpace(pending.ExecID),
			exitCode, len(state.StdoutBuffer), len(state.StderrBuffer),
			truncateForLog(state.StdoutBuffer, 500))
	case *agentv1.ShellStream_Rejected:
		state := ensureBackgroundShellStateLocked(stream, shellID, pending, now)
		if state == nil {
			return
		}
		state.Status = backgroundShellStatusRejected
		state.StderrBuffer += strings.TrimSpace(event.Rejected.GetReason())
		state.LastActivityAt = now
		state.CompletedAt = now
		logger.Debugf("[shell-debug] Rejected request_id=%s shell_id=%s exec_id=%s reason=%q",
			strings.TrimSpace(stream.RequestID), shellID, strings.TrimSpace(pending.ExecID),
			strings.TrimSpace(event.Rejected.GetReason()))
	case *agentv1.ShellStream_PermissionDenied:
		state := ensureBackgroundShellStateLocked(stream, shellID, pending, now)
		if state == nil {
			return
		}
		state.Status = backgroundShellStatusPermissionDenied
		state.StderrBuffer += strings.TrimSpace(event.PermissionDenied.GetError())
		state.LastActivityAt = now
		state.CompletedAt = now
		logger.Debugf("[shell-debug] PermissionDenied request_id=%s shell_id=%s exec_id=%s error=%q",
			strings.TrimSpace(stream.RequestID), shellID, strings.TrimSpace(pending.ExecID),
			strings.TrimSpace(event.PermissionDenied.GetError()))
	default:
		logger.Debugf("[shell-debug] UnknownShellEvent request_id=%s shell_id=%s exec_id=%s event_type=%T",
			strings.TrimSpace(stream.RequestID), shellID, strings.TrimSpace(pending.ExecID), event)
	}
	stream.UpdatedAt = now
}

func observeBackgroundTaskCompletionAction(stream *ActiveStream, message *agentv1.AgentClientMessage) {
	if stream == nil || message == nil {
		return
	}
	action := message.GetConversationAction()
	if action == nil {
		return
	}
	item, ok := action.GetAction().(*agentv1.ConversationAction_BackgroundTaskCompletionAction)
	if !ok || item.BackgroundTaskCompletionAction == nil {
		return
	}
	now := time.Now().UTC()
	stream.mu.Lock()
	defer stream.mu.Unlock()
	for _, completion := range item.BackgroundTaskCompletionAction.GetCompletions() {
		observeBackgroundTaskCompletionLocked(stream, completion, now)
	}
}

func observeBackgroundTaskCompletionLocked(stream *ActiveStream, completion *agentv1.BackgroundTaskCompletion, now time.Time) bool {
	if stream == nil || completion == nil || completion.GetKind() != agentv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL {
		return false
	}
	shellID := strings.TrimSpace(completion.GetTaskId())
	if shellID == "" {
		return false
	}
	state := ensureBackgroundShellStateLocked(stream, shellID, runtimecore.PendingExec{}, now)
	if state == nil {
		return false
	}
	detail := strings.TrimSpace(completion.GetDetail())
	switch completion.GetStatus() {
	case agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCESS:
		state.Status = backgroundShellStatusCompleted
		if detail != "" {
			state.StdoutBuffer = appendBackgroundShellBuffer(state.StdoutBuffer, detail)
		}
	case agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_ERROR:
		state.Status = backgroundShellStatusTransportClosed
		if detail != "" {
			state.StderrBuffer = appendBackgroundShellBuffer(state.StderrBuffer, detail)
		}
	case agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_ABORTED:
		state.Status = backgroundShellStatusTransportClosed
		if detail != "" {
			state.StderrBuffer = appendBackgroundShellBuffer(state.StderrBuffer, detail)
		}
	default:
		if completion.GetReason() == agentv1.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_PROGRESS {
			state.Status = backgroundShellStatusRunning
		} else {
			return false
		}
	}
	state.LastActivityAt = now
	if completion.GetReason() == agentv1.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_FINISHED || isBackgroundShellTerminalStatus(state.Status) {
		state.CompletedAt = now
	}
	stream.UpdatedAt = now
	return true
}

func observeBackgroundShellAction(stream *ActiveStream, message *agentv1.AgentClientMessage) (string, bool) {
	if stream == nil || message == nil {
		return "", false
	}
	action := message.GetConversationAction()
	if action == nil {
		return "", false
	}
	item, ok := action.GetAction().(*agentv1.ConversationAction_BackgroundShellAction)
	if !ok || item.BackgroundShellAction == nil {
		return "", false
	}
	return recordBackgroundShellActionMemory(stream, item.BackgroundShellAction.GetToolCallId(), time.Now().UTC())
}

func recordBackgroundShellActionMemory(stream *ActiveStream, toolCallID string, now time.Time) (string, bool) {
	trimmedToolCallID := strings.TrimSpace(toolCallID)
	if stream == nil || trimmedToolCallID == "" {
		return "", false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return recordBackgroundShellActionLocked(stream, trimmedToolCallID, now)
}

func recordBackgroundShellActionLocked(stream *ActiveStream, toolCallID string, now time.Time) (string, bool) {
	trimmedToolCallID := strings.TrimSpace(toolCallID)
	if stream == nil || trimmedToolCallID == "" {
		return "", false
	}
	if stream.BackgroundShellActions == nil {
		stream.BackgroundShellActions = make(map[string]time.Time)
	}
	if _, exists := stream.BackgroundShellActions[trimmedToolCallID]; exists {
		return trimmedToolCallID, false
	}
	stream.BackgroundShellActions[trimmedToolCallID] = now
	stream.UpdatedAt = now
	return trimmedToolCallID, true
}

func newBackgroundShellActionMetadataEntry(turnSeq int64, requestID string, toolCallID string, source string) HistoryEntry {
	return newMetadataEntry(turnSeq, requestID, "background_shell_action", map[string]any{
		"tool_call_id": strings.TrimSpace(toolCallID),
		"source":       strings.TrimSpace(source),
	})
}

func backgroundTaskCompletionMetadataEntries(turnSeq int64, requestID string, message *agentv1.AgentClientMessage) []HistoryEntry {
	if message == nil || message.GetConversationAction() == nil {
		return nil
	}
	item, ok := message.GetConversationAction().GetAction().(*agentv1.ConversationAction_BackgroundTaskCompletionAction)
	if !ok || item.BackgroundTaskCompletionAction == nil {
		return nil
	}
	completions := item.BackgroundTaskCompletionAction.GetCompletions()
	if len(completions) == 0 {
		return nil
	}
	entries := make([]HistoryEntry, 0, len(completions))
	for _, completion := range completions {
		if completion == nil {
			continue
		}
		values := map[string]any{
			"task_id": strings.TrimSpace(completion.GetTaskId()),
			"kind":    completion.GetKind().String(),
			"status":  completion.GetStatus().String(),
			"reason":  completion.GetReason().String(),
		}
		if title := strings.TrimSpace(completion.GetTitle()); title != "" {
			values["title"] = title
		}
		if detail := strings.TrimSpace(completion.GetDetail()); detail != "" {
			values["detail"] = detail
		}
		if outputPath := strings.TrimSpace(completion.GetOutputPath()); outputPath != "" {
			values["output_path"] = outputPath
		}
		if threadID := strings.TrimSpace(completion.GetThreadId()); threadID != "" {
			values["thread_id"] = threadID
		}
		entries = append(entries, newMetadataEntry(turnSeq, requestID, "background_task_completion_action", values))
	}
	return entries
}

func shellToolCallIsBackgrounded(toolCall *agentv1.ToolCall) bool {
	if toolCall == nil {
		return false
	}
	shellToolCall := toolCall.GetShellToolCall()
	if shellToolCall == nil || shellToolCall.GetResult() == nil {
		return false
	}
	return shellToolCall.GetResult().GetIsBackground()
}

func backgroundShellIDFromToolCall(toolCall *agentv1.ToolCall) string {
	if toolCall == nil {
		return ""
	}
	shellToolCall := toolCall.GetShellToolCall()
	if shellToolCall == nil || shellToolCall.GetResult() == nil {
		return ""
	}
	if success := shellToolCall.GetResult().GetSuccess(); success != nil && success.ShellId != nil {
		return strconv.FormatUint(uint64(success.GetShellId()), 10)
	}
	return ""
}

func appendBackgroundShellBuffer(current string, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return current
	}
	if current == "" || strings.HasSuffix(current, "\n") {
		return current + trimmed
	}
	return current + "\n" + trimmed
}

func ensureBackgroundShellStateLocked(stream *ActiveStream, shellID string, pending runtimecore.PendingExec, now time.Time) *BackgroundShellState {
	trimmedShellID := strings.TrimSpace(shellID)
	if trimmedShellID == "" {
		return nil
	}
	if stream.BackgroundShells == nil {
		stream.BackgroundShells = make(map[string]*BackgroundShellState)
	}
	if stream.BackgroundShellsByMessageID == nil {
		stream.BackgroundShellsByMessageID = make(map[uint32]string)
	}
	if stream.BackgroundShellsByExecID == nil {
		stream.BackgroundShellsByExecID = make(map[string]string)
	}
	state := stream.BackgroundShells[trimmedShellID]
	if state == nil {
		state = &BackgroundShellState{ShellID: trimmedShellID, Status: backgroundShellStatusRunning, CreatedAt: now}
		stream.BackgroundShells[trimmedShellID] = state
	}
	if pending.MessageID != 0 {
		stream.BackgroundShellsByMessageID[pending.MessageID] = trimmedShellID
	}
	if strings.TrimSpace(pending.ExecID) != "" {
		stream.BackgroundShellsByExecID[strings.TrimSpace(pending.ExecID)] = trimmedShellID
	}
	if state.OriginalToolCallID == "" {
		state.OriginalToolCallID = strings.TrimSpace(pending.ToolCallID)
	}
	if state.OriginalExecID == "" {
		state.OriginalExecID = strings.TrimSpace(pending.ExecID)
	}
	if state.OriginalMessageID == 0 {
		state.OriginalMessageID = pending.MessageID
	}
	if len(state.ArgsJSON) == 0 && len(pending.ArgsJSON) > 0 {
		state.ArgsJSON = append([]byte(nil), pending.ArgsJSON...)
	}
	if state.ModelCallID == "" {
		state.ModelCallID = strings.TrimSpace(pending.ModelCallID)
	}
	return state
}

func backgroundShellIDForMessageLocked(stream *ActiveStream, messageID uint32, execID string) string {
	if stream == nil {
		return ""
	}
	if strings.TrimSpace(execID) != "" {
		if shellID := strings.TrimSpace(stream.BackgroundShellsByExecID[strings.TrimSpace(execID)]); shellID != "" {
			return shellID
		}
	}
	if messageID != 0 {
		return strings.TrimSpace(stream.BackgroundShellsByMessageID[messageID])
	}
	return ""
}

func truncateForLog(text string, maxLen int) string {
	if maxLen <= 0 || len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + fmt.Sprintf("...(+%d bytes)", len(text)-maxLen)
}
