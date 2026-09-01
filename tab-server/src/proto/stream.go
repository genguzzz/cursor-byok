// Package proto defines the minimal aiserver.v1 wire subset used by the
// Cursor Tab (Cpp) surface. Only the fields this server reads or writes are
// declared; unknown fields encountered on the wire are skipped.
package proto

// StreamCppRequest is the Tab inline-completion request.
type StreamCppRequest struct {
	CurrentFile           *CurrentFileInfo     `protobuf:"bytes,1"`
	DiffHistory           []string             `protobuf:"bytes,2"`
	ModelName            *string              `protobuf:"bytes,3"`
	ContextItems         []*CppContextItem    `protobuf:"bytes,13"`
	DiffHistoryKeys      []string             `protobuf:"bytes,5"`
	FileDiffHistories    []*CppFileDiffHistory `protobuf:"bytes,7"`
	MergedDiffHistories  []*CppFileDiffHistory `protobuf:"bytes,8"`
	ParameterHints       []*CppParameterHint  `protobuf:"bytes,14"`
	CppIntentInfo        *CppIntentInfo       `protobuf:"bytes,16"`
	WorkspaceID          *string              `protobuf:"bytes,18"`
	AdditionalFiles      []*AdditionalFile    `protobuf:"bytes,19"`
	SupportsCpt          *bool                `protobuf:"varint,27"`
	SupportsCrlfCpt      *bool                `protobuf:"varint,28"`
}

// StreamNextCursorPredictionRequest is the jump-to-next-edit request.
type StreamNextCursorPredictionRequest struct {
	CurrentFile        *CurrentFileInfo      `protobuf:"bytes,1"`
	DiffHistory        []string              `protobuf:"bytes,2"`
	ModelName          *string               `protobuf:"bytes,3"`
	ContextItems       []*CppContextItem     `protobuf:"bytes,13"`
	DiffHistoryKeys    []string              `protobuf:"bytes,5"`
	FileDiffHistories  []*CppFileDiffHistory `protobuf:"bytes,7"`
	MergedDiffHistories []*CppFileDiffHistory `protobuf:"bytes,8"`
	ParameterHints     []*CppParameterHint   `protobuf:"bytes,14"`
	CppIntentInfo      *CppIntentInfo        `protobuf:"bytes,16"`
	WorkspaceID        *string               `protobuf:"bytes,18"`
	FileSyncUpdates    []byte                `protobuf:"bytes,19"`
	VisibleRanges      []*FileVisibleRange   `protobuf:"bytes,20"`
}

// FileVisibleRange is the visible viewport of one file.
type FileVisibleRange struct {
	Filename      string         `protobuf:"bytes,1"`
	VisibleRanges []*VisibleRange `protobuf:"bytes,2"`
}

// VisibleRange is a one-indexed half-open line range.
type VisibleRange struct {
	StartLineNumberInclusive int32 `protobuf:"varint,1"`
	EndLineNumberExclusive   int32 `protobuf:"varint,2"`
}

// StreamCppResponse is one chunk of an inline-completion stream.
type StreamCppResponse struct {
	Text                    string  `protobuf:"bytes,1"`
	SuggestionStartLine     *int32  `protobuf:"varint,2"`
	SuggestionConfidence    *int32  `protobuf:"varint,3"`
	DoneStream              *bool   `protobuf:"varint,4"`
	DebugModelOutput        *string `protobuf:"bytes,5"`
	DebugModelInput         *string `protobuf:"bytes,6"`
	DebugStreamTime         *string `protobuf:"bytes,7"`
	DebugTotalTime          *string `protobuf:"bytes,8"`
	DebugTtftTime           *string `protobuf:"bytes,9"`
	DebugServerTiming       *string `protobuf:"bytes,10"`
	RangeToReplace          *LineRange `protobuf:"bytes,11"`
	CursorPredictionTarget  *CursorPredictionTarget `protobuf:"bytes,12"`
	DoneEdit                *bool   `protobuf:"varint,13"`
	BeginEdit               *bool   `protobuf:"varint,15"`
	ShouldRemoveLeadingEol  *bool   `protobuf:"varint,16"`
	BindingID               *string `protobuf:"bytes,17"`
}

// CursorPredictionTarget is the target of a jump-to-next-edit prediction.
type CursorPredictionTarget struct {
	RelativePath          string `protobuf:"bytes,1"`
	LineNumberOneIndexed  int32  `protobuf:"varint,2"`
	ExpectedContent       string `protobuf:"bytes,3"`
	ShouldRetriggerCpp    bool   `protobuf:"varint,4"`
}

// StreamNextCursorPredictionResponse is one chunk of a next-edit stream.
type StreamNextCursorPredictionResponse struct {
	Text          *string `protobuf:"bytes,1"`
	LineNumber    *int32  `protobuf:"varint,2"`
	IsNotInRange  *bool   `protobuf:"varint,3"`
	FileName      *string `protobuf:"bytes,4"`
}
