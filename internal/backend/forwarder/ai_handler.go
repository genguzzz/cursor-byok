package forwarder

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	"cursor/gen/aiserverv1"
)

type usageLookupRecord struct {
	InputTokens  int64
	OutputTokens int64
	CreatedAt    time.Time
}

// Procedure path constants only — do not import gen/aiserverv1/aiserverv1connect.
// That generated package is ~30k LoC of unused client/handler stubs and dominates cold compile time.
const (
	dashboardServiceGetTokenUsageProcedure                  = "/aiserver.v1.DashboardService/GetTokenUsage"
	dashboardServiceGetGlassEarlyPreviewEnrollmentProcedure = "/aiserver.v1.DashboardService/GetGlassEarlyPreviewEnrollment"
	aiServiceCountTokensProcedure                           = "/aiserver.v1.AiService/CountTokens"
	aiServiceGetThoughtAnnotationProcedure                  = "/aiserver.v1.AiService/GetThoughtAnnotation"
	aiServiceWriteGitCommitMessageProcedure                 = "/aiserver.v1.AiService/WriteGitCommitMessage"
	aiServiceCreateExperimentalIndexProcedure               = "/aiserver.v1.AiService/CreateExperimentalIndex"
	aiServiceListExperimentalIndexFilesProcedure            = "/aiserver.v1.AiService/ListExperimentalIndexFiles"
	aiServiceListenExperimentalIndexProcedure               = "/aiserver.v1.AiService/ListenExperimentalIndex"
	aiServiceRegisterFileToIndexProcedure                   = "/aiserver.v1.AiService/RegisterFileToIndex"
	aiServiceSetupIndexDependenciesProcedure                = "/aiserver.v1.AiService/SetupIndexDependencies"
	aiServiceComputeIndexTopoSortProcedure                  = "/aiserver.v1.AiService/ComputeIndexTopoSort"
	aiServiceDocumentationQueryProcedure                    = "/aiserver.v1.AiService/DocumentationQuery"
	aiServiceAvailableDocsProcedure                         = "/aiserver.v1.AiService/AvailableDocs"
	aiServiceKnowledgeBaseAddProcedure                      = "/aiserver.v1.AiService/KnowledgeBaseAdd"
	aiServiceKnowledgeBaseListProcedure                     = "/aiserver.v1.AiService/KnowledgeBaseList"
	aiServiceKnowledgeBaseRemoveProcedure                   = "/aiserver.v1.AiService/KnowledgeBaseRemove"
	aiServiceKnowledgeBaseUpdateProcedure                   = "/aiserver.v1.AiService/KnowledgeBaseUpdate"
	aiServiceFetchRelevantKnowledgeForConversationProcedure = "/aiserver.v1.AiService/FetchRelevantKnowledgeForConversation"
	aiServiceNameTabProcedure                               = "/aiserver.v1.AiService/NameTab"
	aiServiceNameAgentProcedure                             = "/aiserver.v1.AiService/NameAgent"
)

func newAIHandler(service *Service) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(
		dashboardServiceGetTokenUsageProcedure,
		connect.NewUnaryHandler(dashboardServiceGetTokenUsageProcedure, service.GetTokenUsage),
	)
	mux.Handle(
		dashboardServiceGetGlassEarlyPreviewEnrollmentProcedure,
		connect.NewUnaryHandler(dashboardServiceGetGlassEarlyPreviewEnrollmentProcedure, service.GetGlassEarlyPreviewEnrollment),
	)
	mux.Handle(
		aiServiceCountTokensProcedure,
		connect.NewUnaryHandler(aiServiceCountTokensProcedure, service.CountTokens),
	)
	mux.Handle(
		aiServiceGetThoughtAnnotationProcedure,
		connect.NewUnaryHandler(aiServiceGetThoughtAnnotationProcedure, service.GetThoughtAnnotation),
	)
	mux.Handle(
		aiServiceWriteGitCommitMessageProcedure,
		connect.NewUnaryHandler(aiServiceWriteGitCommitMessageProcedure, service.WriteGitCommitMessage),
	)
	mux.Handle(
		aiServiceCreateExperimentalIndexProcedure,
		connect.NewUnaryHandler(aiServiceCreateExperimentalIndexProcedure, service.CreateExperimentalIndex),
	)
	mux.Handle(
		aiServiceListExperimentalIndexFilesProcedure,
		connect.NewUnaryHandler(aiServiceListExperimentalIndexFilesProcedure, service.ListExperimentalIndexFiles),
	)
	mux.Handle(
		aiServiceListenExperimentalIndexProcedure,
		connect.NewServerStreamHandler(aiServiceListenExperimentalIndexProcedure, service.ListenExperimentalIndex),
	)
	mux.Handle(
		aiServiceRegisterFileToIndexProcedure,
		connect.NewUnaryHandler(aiServiceRegisterFileToIndexProcedure, service.RegisterFileToIndex),
	)
	mux.Handle(
		aiServiceSetupIndexDependenciesProcedure,
		connect.NewUnaryHandler(aiServiceSetupIndexDependenciesProcedure, service.SetupIndexDependencies),
	)
	mux.Handle(
		aiServiceComputeIndexTopoSortProcedure,
		connect.NewUnaryHandler(aiServiceComputeIndexTopoSortProcedure, service.ComputeIndexTopoSort),
	)
	mux.Handle(
		aiServiceDocumentationQueryProcedure,
		connect.NewUnaryHandler(aiServiceDocumentationQueryProcedure, service.DocumentationQuery),
	)
	mux.Handle(
		aiServiceAvailableDocsProcedure,
		connect.NewUnaryHandler(aiServiceAvailableDocsProcedure, service.AvailableDocs),
	)
	mux.Handle(
		aiServiceKnowledgeBaseAddProcedure,
		connect.NewUnaryHandler(aiServiceKnowledgeBaseAddProcedure, service.KnowledgeBaseAdd),
	)
	mux.Handle(
		aiServiceKnowledgeBaseListProcedure,
		connect.NewUnaryHandler(aiServiceKnowledgeBaseListProcedure, service.KnowledgeBaseList),
	)
	mux.Handle(
		aiServiceKnowledgeBaseRemoveProcedure,
		connect.NewUnaryHandler(aiServiceKnowledgeBaseRemoveProcedure, service.KnowledgeBaseRemove),
	)
	mux.Handle(
		aiServiceKnowledgeBaseUpdateProcedure,
		connect.NewUnaryHandler(aiServiceKnowledgeBaseUpdateProcedure, service.KnowledgeBaseUpdate),
	)
	mux.Handle(
		aiServiceFetchRelevantKnowledgeForConversationProcedure,
		connect.NewUnaryHandler(aiServiceFetchRelevantKnowledgeForConversationProcedure, service.FetchRelevantKnowledgeForConversation),
	)
	mux.Handle(
		aiServiceNameTabProcedure,
		connect.NewUnaryHandler(aiServiceNameTabProcedure, service.NameTab),
	)
	mux.Handle(
		aiServiceNameAgentProcedure,
		connect.NewUnaryHandler(aiServiceNameAgentProcedure, service.NameAgent),
	)
	mux.Handle("/", http.NotFoundHandler())
	return mux
}

func (service *Service) GetThoughtAnnotation(_ context.Context, req *connect.Request[aiserverv1.GetThoughtAnnotationRequest]) (*connect.Response[aiserverv1.GetThoughtAnnotationResponse], error) {
	requestID := strings.TrimSpace(req.Msg.GetRequestId())
	thought, ok, err := service.lookupThoughtAnnotation(requestID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !ok {
		return connect.NewResponse(&aiserverv1.GetThoughtAnnotationResponse{}), nil
	}
	return connect.NewResponse(&aiserverv1.GetThoughtAnnotationResponse{
		ThoughtAnnotation: &aiserverv1.AiThoughtAnnotation{
			RequestId: requestID,
			Thought:   thought,
		},
	}), nil
}

func (service *Service) GetTokenUsage(_ context.Context, req *connect.Request[aiserverv1.GetTokenUsageRequest]) (*connect.Response[aiserverv1.GetTokenUsageResponse], error) {
	record, ok, err := service.lookupUsageRecord(strings.TrimSpace(req.Msg.GetUsageUuid()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !ok {
		return connect.NewResponse(&aiserverv1.GetTokenUsageResponse{}), nil
	}
	return connect.NewResponse(&aiserverv1.GetTokenUsageResponse{
		InputTokens:  clampInt64ToInt32(record.InputTokens),
		OutputTokens: clampInt64ToInt32(record.OutputTokens),
	}), nil
}

func (service *Service) GetGlassEarlyPreviewEnrollment(context.Context, *connect.Request[aiserverv1.GetGlassEarlyPreviewEnrollmentRequest]) (*connect.Response[aiserverv1.GetGlassEarlyPreviewEnrollmentResponse], error) {
	granted := true
	return connect.NewResponse(&aiserverv1.GetGlassEarlyPreviewEnrollmentResponse{
		Enabled:                           true,
		EnterpriseGlassSelfEnrollEligible: &granted,
		GlassAccessGranted:                &granted,
	}), nil
}

func (service *Service) CountTokens(_ context.Context, req *connect.Request[aiserverv1.CountTokensRequest]) (*connect.Response[aiserverv1.CountTokensResponse], error) {
	total := int64(0)
	details := make([]*aiserverv1.ContextItemTokenDetail, 0, len(req.Msg.GetContextItems()))
	for _, item := range req.Msg.GetContextItems() {
		count := estimateContextItemTokens(item)
		total += count
		details = append(details, &aiserverv1.ContextItemTokenDetail{
			RelativeWorkspacePath: contextItemRelativeWorkspacePath(item),
			Count:                 clampInt64ToInt32(count),
			LineCount:             lineCountForContextItem(item),
		})
	}
	return connect.NewResponse(&aiserverv1.CountTokensResponse{
		Count:        clampInt64ToInt32(total),
		TokenDetails: details,
	}), nil
}

func (service *Service) lookupUsageRecord(usageUUID string) (usageLookupRecord, bool, error) {
	if service == nil {
		return usageLookupRecord{}, false, nil
	}
	if service.usageStore == nil {
		return usageLookupRecord{}, false, nil
	}
	item, ok, err := service.usageStore.LookupEvent(strings.TrimSpace(usageUUID))
	if err != nil || !ok {
		return usageLookupRecord{}, ok, err
	}
	return usageLookupRecord{
		InputTokens:  item.InputTokens + item.CacheReadTokens + item.CacheWriteTokens,
		OutputTokens: item.OutputTokens,
		CreatedAt:    item.At,
	}, true, nil
}

func (service *Service) lookupThoughtAnnotation(requestID string) (string, bool, error) {
	if service == nil || service.store == nil {
		return "", false, nil
	}
	needle := strings.TrimSpace(requestID)
	if needle == "" {
		return "", false, nil
	}
	foundRequest := false
	foundThought := false
	latestThought := ""
	latestThoughtAt := time.Time{}
	conversationIDs, err := service.store.ListConversationIDs()
	if err != nil {
		return "", false, err
	}
	for _, conversationID := range conversationIDs {
		conversation, err := service.store.LoadConversation(conversationID)
		if err != nil {
			return "", false, err
		}
		if conversation == nil {
			continue
		}
		for _, entry := range conversation.Entries {
			if strings.TrimSpace(entry.RequestID) != needle {
				continue
			}
			foundRequest = true
			if strings.TrimSpace(entry.Kind) != "metadata" {
				continue
			}
			var payload metadataPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				continue
			}
			if strings.TrimSpace(payload.Type) != "thought_annotation" {
				continue
			}
			if strings.TrimSpace(readStringValue(payload.Value["kind"])) != "summary_completed" {
				continue
			}
			thought := strings.TrimSpace(readStringValue(payload.Value["thought"]))
			if thought == "" {
				continue
			}
			if !foundThought || entry.CreatedAt.After(latestThoughtAt) {
				foundThought = true
				latestThought = thought
				latestThoughtAt = entry.CreatedAt
			}
		}
	}
	if foundThought {
		return latestThought, true, nil
	}
	if foundRequest {
		return defaultSummaryCompletedThought, true, nil
	}
	return "", false, nil
}

func lineCountForContextItem(item *aiserverv1.ContextItem) int32 {
	if item == nil {
		return 0
	}
	if chunk := item.GetFileChunk(); chunk != nil {
		return countTextLines(chunk.GetChunkContents())
	}
	if outline := item.GetOutlineChunk(); outline != nil {
		return lineCountForRange(outline.GetFullRange())
	}
	if selection := item.GetCmdKSelection(); selection != nil {
		return int32(len(selection.GetLines()))
	}
	if sparse := item.GetSparseFileChunk(); sparse != nil {
		return int32(len(sparse.GetLines()))
	}
	return 0
}

func contextItemRelativeWorkspacePath(item *aiserverv1.ContextItem) string {
	if item == nil {
		return ""
	}
	if chunk := item.GetFileChunk(); chunk != nil {
		return strings.TrimSpace(chunk.GetRelativeWorkspacePath())
	}
	if outline := item.GetOutlineChunk(); outline != nil {
		return strings.TrimSpace(outline.GetRelativeWorkspacePath())
	}
	if sparse := item.GetSparseFileChunk(); sparse != nil {
		return strings.TrimSpace(sparse.GetRelativeWorkspacePath())
	}
	return ""
}

func lineCountForRange(lineRange *aiserverv1.LineRange) int32 {
	if lineRange == nil {
		return 0
	}
	start := lineRange.GetStartLineNumber()
	end := lineRange.GetEndLineNumberInclusive()
	if start <= 0 || end < start {
		return 0
	}
	return end - start + 1
}

func countTextLines(text string) int32 {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	return int32(strings.Count(text, "\n") + 1)
}

func readInt64Value(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case float32:
		return int64(typed)
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case uint32:
		return int64(typed)
	case uint64:
		if typed > uint64(^uint32(0)>>1) {
			return int64(^uint32(0) >> 1)
		}
		return int64(typed)
	case json.Number:
		value, err := typed.Int64()
		if err == nil {
			return value
		}
	}
	return 0
}

func readStringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return ""
	}
}

func readBoolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func readTimeValue(value any) time.Time {
	return parseRFC3339Time(readStringValue(value))
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value.UTC()
		}
	}
	return time.Time{}
}
