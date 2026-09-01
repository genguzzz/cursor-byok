package proto

import (
	"github.com/leookun/cursor-byok/tab-server/src/codec"
)

// EncodeStreamCppResponse encodes one inline-completion chunk.
func EncodeStreamCppResponse(response *StreamCppResponse) []byte {
	writer := codec.NewWriter(64)
	writer.String(1, response.Text)
	if value := response.SuggestionStartLine; value != nil {
		writer.Int32(2, *value)
	}
	if value := response.SuggestionConfidence; value != nil {
		writer.Int32(3, *value)
	}
	if value := response.DoneStream; value != nil {
		writer.Bool(4, *value)
	}
	if value := response.RangeToReplace; value != nil {
		writer.Nested(11, encodeLineRange(value))
	}
	if value := response.CursorPredictionTarget; value != nil {
		writer.Nested(12, encodeCursorPredictionTarget(value))
	}
	if value := response.DoneEdit; value != nil {
		writer.Bool(13, *value)
	}
	if value := response.BeginEdit; value != nil {
		writer.Bool(15, *value)
	}
	if value := response.ShouldRemoveLeadingEol; value != nil {
		writer.Bool(16, *value)
	}
	if value := response.BindingID; value != nil {
		writer.String(17, *value)
	}
	return writer.Bytes()
}

// EncodeStreamNextCursorPredictionResponse encodes one next-edit chunk.
func EncodeStreamNextCursorPredictionResponse(response *StreamNextCursorPredictionResponse) []byte {
	writer := codec.NewWriter(64)
	if value := response.Text; value != nil {
		writer.String(1, *value)
	}
	if value := response.LineNumber; value != nil {
		writer.Int32(2, *value)
	}
	if value := response.IsNotInRange; value != nil {
		writer.Bool(3, *value)
	}
	if value := response.FileName; value != nil {
		writer.String(4, *value)
	}
	return writer.Bytes()
}

// EncodeCppConfigResponse encodes the Tab configuration.
func EncodeCppConfigResponse(response *CppConfigResponse) []byte {
	writer := codec.NewWriter(128)
	if value := response.AboveRadius; value != nil {
		writer.Int32(1, *value)
	}
	if value := response.BelowRadius; value != nil {
		writer.Int32(2, *value)
	}
	if value := response.IsOn; value != nil {
		writer.Bool(5, *value)
	}
	if value := response.IsGhostText; value != nil {
		writer.Bool(6, *value)
	}
	writer.Bool(10, response.EnableRvfTracking)
	writer.Int32(11, response.GlobalDebounceDurationMillis)
	writer.Int32(12, response.ClientDebounceDurationMillis)
	writer.String(13, response.CppURL)
	writer.Bool(14, response.UseWhitespaceDiffHistory)
	writer.Bool(25, response.AllowsTabChunks)
	return writer.Bytes()
}

// EncodeAvailableCppModelsResponse encodes the Tab model list.
func EncodeAvailableCppModelsResponse(response *AvailableCppModelsResponse) []byte {
	writer := codec.NewWriter(64)
	for _, model := range response.Models {
		writer.String(1, model)
	}
	if value := response.DefaultModel; value != nil {
		writer.String(2, *value)
	}
	return writer.Bytes()
}

// EncodeCppEditHistoryStatusResponse encodes edit-history status.
func EncodeCppEditHistoryStatusResponse(response *CppEditHistoryStatusResponse) []byte {
	writer := codec.NewWriter(8)
	writer.Bool(1, response.On)
	writer.Bool(2, response.OnlyIfExplicit)
	return writer.Bytes()
}

// EncodeCppAppendResponse encodes an edit-history append acknowledgement.
func EncodeCppAppendResponse(response *CppAppendResponse) []byte {
	writer := codec.NewWriter(8)
	writer.Bool(1, response.Success)
	return writer.Bytes()
}

// EncodeEditHistoryAppendChangesResponse encodes a batch append acknowledgement.
func EncodeEditHistoryAppendChangesResponse(response *EditHistoryAppendChangesResponse) []byte {
	writer := codec.NewWriter(8)
	writer.Bool(1, response.Success)
	return writer.Bytes()
}

// EncodeGetCppEditClassificationResponse encodes the edit ranking result.
func EncodeGetCppEditClassificationResponse(response *GetCppEditClassificationResponse) []byte {
	writer := codec.NewWriter(16)
	if value := response.ShouldNoop; value != nil {
		writer.Bool(3, *value)
	}
	return writer.Bytes()
}

// EncodeFSIsEnabledForUserResponse encodes FileSync availability.
func EncodeFSIsEnabledForUserResponse(response *FSIsEnabledForUserResponse) []byte {
	writer := codec.NewWriter(8)
	writer.Bool(1, response.Enabled)
	return writer.Bytes()
}

// EncodeFSSyncFileResponse encodes a FileSync update acknowledgement.
func EncodeFSSyncFileResponse(response *FSSyncFileResponse) []byte {
	writer := codec.NewWriter(8)
	writer.Int32(1, response.Error)
	return writer.Bytes()
}

// EncodeFSUploadFileResponse encodes a FileSync upload acknowledgement.
func EncodeFSUploadFileResponse(response *FSUploadFileResponse) []byte {
	writer := codec.NewWriter(8)
	writer.Int32(1, response.Error)
	return writer.Bytes()
}

// EncodeFSConfigResponse encodes the FileSync configuration.
func EncodeFSConfigResponse(response *FSConfigResponse) []byte {
	writer := codec.NewWriter(64)
	writer.Float32(1, response.CheckFilesyncHashPercent)
	if value := response.MaxRecentUpdatesStored; value != nil {
		writer.Int32(5, *value)
	}
	if value := response.MaxModelVersionCacheSize; value != nil {
		writer.Int32(6, *value)
	}
	if value := response.MaxFileSizeToSyncBytes; value != nil {
		writer.Int32(7, *value)
	}
	if value := response.SyncRetryMaxAttempts; value != nil {
		writer.Int32(8, *value)
	}
	if value := response.SyncRetryInitialDelayMs; value != nil {
		writer.Int32(9, *value)
	}
	if value := response.SyncRetryTimeMultiplier; value != nil {
		writer.Int32(10, *value)
	}
	if value := response.FileSyncStatusMaxCacheSize; value != nil {
		writer.Int32(11, *value)
	}
	if value := response.SuccessiveSyncsRequiredForReliance; value != nil {
		writer.Int32(12, *value)
	}
	return writer.Bytes()
}

// EncodeWriteGitCommitMessageResponse encodes a generated commit message.
func EncodeWriteGitCommitMessageResponse(response *WriteGitCommitMessageResponse) []byte {
	writer := codec.NewWriter(64)
	writer.String(1, response.CommitMessage)
	return writer.Bytes()
}

// EncodeWriteGitBranchNameResponse encodes a generated branch name.
func EncodeWriteGitBranchNameResponse(response *WriteGitBranchNameResponse) []byte {
	writer := codec.NewWriter(64)
	writer.String(1, response.BranchName)
	return writer.Bytes()
}

func encodeLineRange(value *LineRange) []byte {
	writer := codec.NewWriter(16)
	writer.Int32(1, value.StartLineNumber)
	writer.Int32(2, value.EndLineNumberInclusive)
	return writer.Bytes()
}

func encodeCursorPredictionTarget(value *CursorPredictionTarget) []byte {
	writer := codec.NewWriter(64)
	writer.String(1, value.RelativePath)
	writer.Int32(2, value.LineNumberOneIndexed)
	writer.String(3, value.ExpectedContent)
	writer.Bool(4, value.ShouldRetriggerCpp)
	return writer.Bytes()
}

// EncodeRefreshTabContextResponse encodes an empty refresh acknowledgement.
func EncodeRefreshTabContextResponse(response *RefreshTabContextResponse) []byte {
	return []byte{}
}

// EncodeRecordCppFateResponse encodes an empty fate acknowledgement.
func EncodeRecordCppFateResponse(response *RecordCppFateResponse) []byte {
	return []byte{}
}

// EncodeReportAiCodeChangeMetricsResponse encodes an empty metrics acknowledgement.
func EncodeReportAiCodeChangeMetricsResponse(response *ReportAiCodeChangeMetricsResponse) []byte {
	return []byte{}
}
