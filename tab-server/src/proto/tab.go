// Package proto defines the minimal aiserver.v1 wire subset used by the
// Cursor Tab (Cpp) surface. Only the fields this server reads or writes are
// declared; unknown fields encountered on the wire are skipped.
package proto

// CursorPosition is a zero-indexed position inside a file.
type CursorPosition struct {
	Line   int32 `protobuf:"varint,1"`
	Column int32 `protobuf:"varint,2"`
}

// CursorRange is a half-open selection range inside a file.
type CursorRange struct {
	StartPosition *CursorPosition `protobuf:"bytes,1"`
	EndPosition   *CursorPosition `protobuf:"bytes,2"`
}

// LineRange is a one-indexed inclusive line range.
type LineRange struct {
	StartLineNumber       int32 `protobuf:"varint,1"`
	EndLineNumberInclusive int32 `protobuf:"varint,2"`
}

// CurrentFileInfo describes the file the user is editing when Tab fires.
type CurrentFileInfo struct {
	RelativeWorkspacePath string          `protobuf:"bytes,1"`
	Contents              string          `protobuf:"bytes,2"`
	ContentsStartAtLine   int32           `protobuf:"varint,9"`
	CursorPosition        *CursorPosition `protobuf:"bytes,3"`
	TotalNumberOfLines    int32           `protobuf:"varint,8"`
	LanguageID            string          `protobuf:"bytes,5"`
	Selection             *CursorRange    `protobuf:"bytes,6"`
	WorkspaceRootPath     string          `protobuf:"bytes,19"`
	LineEnding            *string         `protobuf:"bytes,20"`
	AlternativeVersionID  *int32          `protobuf:"varint,11"`
	FileVersion           *int32          `protobuf:"varint,14"`
}

// CppContextItem is a related code snippet supplied as Tab context.
type CppContextItem struct {
	Contents             string  `protobuf:"bytes,1"`
	Symbol               *string `protobuf:"bytes,2"`
	RelativeWorkspacePath string  `protobuf:"bytes,3"`
	Score                float32 `protobuf:"fixed32,4"`
}

// CppFileDiffHistory is the recent edit history for one file.
type CppFileDiffHistory struct {
	FileName              string    `protobuf:"bytes,1"`
	DiffHistory           []string  `protobuf:"bytes,2"`
	DiffHistoryTimestamps []float64 `protobuf:"fixed64,3"`
}

// CppIntentInfo records what triggered this Tab request.
type CppIntentInfo struct {
	Source string `protobuf:"bytes,1"`
}

// CppParameterHint is a signature-help hint at the cursor.
type CppParameterHint struct {
	Label        string  `protobuf:"bytes,1"`
	Documentation *string `protobuf:"bytes,2"`
}

// AdditionalFile is an open or recently viewed file sent as Tab context.
type AdditionalFile struct {
	RelativeWorkspacePath     string      `protobuf:"bytes,1"`
	IsOpen                    bool        `protobuf:"varint,2"`
	VisibleRangeContent       []string    `protobuf:"bytes,3"`
	LastViewedAt              *float64    `protobuf:"fixed64,4"`
	StartLineNumberOneIndexed []int32     `protobuf:"varint,5"`
	VisibleRanges             []*LineRange `protobuf:"bytes,6"`
}

// CppEditHistoryStatusResponse reports whether edit-history tracking is on.
type CppEditHistoryStatusResponse struct {
	On             bool `protobuf:"varint,1"`
	OnlyIfExplicit bool `protobuf:"varint,2"`
}

// CppAppendResponse acknowledges an append of edit-history changes.
type CppAppendResponse struct {
	Success bool `protobuf:"varint,1"`
}

// EditHistoryAppendChangesResponse acknowledges batched edit-history changes.
type EditHistoryAppendChangesResponse struct {
	Success bool `protobuf:"varint,1"`
}

// CppConfigResponse controls Tab behaviour in the Cursor client.
type CppConfigResponse struct {
	AboveRadius                  *int32 `protobuf:"varint,1"`
	BelowRadius                  *int32 `protobuf:"varint,2"`
	IsOn                         *bool  `protobuf:"varint,5"`
	IsGhostText                  *bool  `protobuf:"varint,6"`
	EnableRvfTracking            bool   `protobuf:"varint,10"`
	GlobalDebounceDurationMillis int32  `protobuf:"varint,11"`
	ClientDebounceDurationMillis int32  `protobuf:"varint,12"`
	CppURL                       string `protobuf:"bytes,13"`
	UseWhitespaceDiffHistory     bool   `protobuf:"varint,14"`
	AllowsTabChunks              bool   `protobuf:"varint,25"`
}

// ImportPredictionConfig configures the auto-import prediction feature.
type ImportPredictionConfig struct {
	IsDisabledByBackend        bool `protobuf:"varint,1"`
	ShouldTurnOnAutomatically  bool `protobuf:"varint,2"`
	PythonEnabled              bool `protobuf:"varint,3"`
}

// AvailableCppModelsResponse lists the Tab models this server exposes.
type AvailableCppModelsResponse struct {
	Models       []string `protobuf:"bytes,1"`
	DefaultModel *string  `protobuf:"bytes,2"`
}

// RecordCppFateResponse acknowledges a suggestion accept/reject report.
type RecordCppFateResponse struct{}

// ReportAiCodeChangeMetricsResponse acknowledges code-change metrics.
type ReportAiCodeChangeMetricsResponse struct{}

// FSIsEnabledForUserResponse reports whether FileSync is enabled.
type FSIsEnabledForUserResponse struct {
	Enabled bool `protobuf:"varint,1"`
}

// FSConfigResponse configures FileSync. All values disable it: this server
// does not maintain a remote file mirror.
type FSConfigResponse struct {
	CheckFilesyncHashPercent       float32 `protobuf:"fixed32,1"`
	MaxRecentUpdatesStored         *int32  `protobuf:"varint,5"`
	MaxModelVersionCacheSize       *int32  `protobuf:"varint,6"`
	MaxFileSizeToSyncBytes         *int32  `protobuf:"varint,7"`
	SyncRetryMaxAttempts           *int32  `protobuf:"varint,8"`
	SyncRetryInitialDelayMs        *int32  `protobuf:"varint,9"`
	SyncRetryTimeMultiplier        *int32  `protobuf:"varint,10"`
	FileSyncStatusMaxCacheSize     *int32  `protobuf:"varint,11"`
	SuccessiveSyncsRequiredForReliance *int32 `protobuf:"varint,12"`
}

// FSSyncFileResponse acknowledges a FileSync update.
type FSSyncFileResponse struct {
	Error int32 `protobuf:"varint,1"`
}

// FSUploadFileResponse acknowledges a FileSync upload.
type FSUploadFileResponse struct {
	Error int32 `protobuf:"varint,1"`
}

// RefreshTabContextResponse returns refreshed Tab context.
type RefreshTabContextResponse struct{}

// GetCppEditClassificationResponse ranks candidate edits.
type GetCppEditClassificationResponse struct {
	ShouldNoop *bool `protobuf:"varint,3"`
}

// WriteGitCommitMessageResponse returns a generated commit message.
type WriteGitCommitMessageResponse struct {
	CommitMessage string `protobuf:"bytes,1"`
}

// WriteGitBranchNameResponse returns a generated branch name.
type WriteGitBranchNameResponse struct {
	BranchName string `protobuf:"bytes,1"`
}
