package proto

import (
	"github.com/leookun/cursor-byok/tab-server/src/codec"
)

// DecodeStreamCppRequest decodes a StreamCpp request body.
func DecodeStreamCppRequest(payload []byte) (*StreamCppRequest, error) {
	request := &StreamCppRequest{}
	reader := codec.NewReader(payload)
	for {
		field, ok, err := reader.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return request, nil
		}
		switch field.Number {
		case 1:
			currentFile, err := decodeCurrentFileInfo(field.Bytes)
			if err != nil {
				return nil, err
			}
			request.CurrentFile = currentFile
		case 2:
			request.DiffHistory = append(request.DiffHistory, field.String())
		case 3:
			value := field.String()
			request.ModelName = &value
		case 5:
			request.DiffHistoryKeys = append(request.DiffHistoryKeys, field.String())
		case 7:
			request.FileDiffHistories = append(request.FileDiffHistories, decodeCppFileDiffHistory(field.Bytes))
		case 8:
			request.MergedDiffHistories = append(request.MergedDiffHistories, decodeCppFileDiffHistory(field.Bytes))
		case 13:
			request.ContextItems = append(request.ContextItems, decodeCppContextItem(field.Bytes))
		case 14:
			request.ParameterHints = append(request.ParameterHints, decodeCppParameterHint(field.Bytes))
		case 16:
			request.CppIntentInfo = &CppIntentInfo{Source: decodeCppIntentInfo(field.Bytes).Source}
		case 18:
			value := field.String()
			request.WorkspaceID = &value
		case 19:
			request.AdditionalFiles = append(request.AdditionalFiles, decodeAdditionalFile(field.Bytes))
		case 27:
			value := field.Bool()
			request.SupportsCpt = &value
		case 28:
			value := field.Bool()
			request.SupportsCrlfCpt = &value
		}
	}
}

// DecodeStreamNextCursorPredictionRequest decodes a next-edit request body.
func DecodeStreamNextCursorPredictionRequest(payload []byte) (*StreamNextCursorPredictionRequest, error) {
	request := &StreamNextCursorPredictionRequest{}
	reader := codec.NewReader(payload)
	for {
		field, ok, err := reader.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return request, nil
		}
		switch field.Number {
		case 1:
			currentFile, err := decodeCurrentFileInfo(field.Bytes)
			if err != nil {
				return nil, err
			}
			request.CurrentFile = currentFile
		case 2:
			request.DiffHistory = append(request.DiffHistory, field.String())
		case 3:
			value := field.String()
			request.ModelName = &value
		case 5:
			request.DiffHistoryKeys = append(request.DiffHistoryKeys, field.String())
		case 7:
			request.FileDiffHistories = append(request.FileDiffHistories, decodeCppFileDiffHistory(field.Bytes))
		case 8:
			request.MergedDiffHistories = append(request.MergedDiffHistories, decodeCppFileDiffHistory(field.Bytes))
		case 13:
			request.ContextItems = append(request.ContextItems, decodeCppContextItem(field.Bytes))
		case 14:
			request.ParameterHints = append(request.ParameterHints, decodeCppParameterHint(field.Bytes))
		case 16:
			request.CppIntentInfo = decodeCppIntentInfo(field.Bytes)
		case 18:
			value := field.String()
			request.WorkspaceID = &value
		case 19:
			request.FileSyncUpdates = field.Bytes
		case 20:
			request.VisibleRanges = append(request.VisibleRanges, decodeFileVisibleRange(field.Bytes))
		}
	}
}

func decodeCurrentFileInfo(payload []byte) (*CurrentFileInfo, error) {
	info := &CurrentFileInfo{}
	reader := codec.NewReader(payload)
	for {
		field, ok, err := reader.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return info, nil
		}
		switch field.Number {
		case 1:
			info.RelativeWorkspacePath = field.String()
		case 2:
			info.Contents = field.String()
		case 3:
			info.CursorPosition = decodeCursorPosition(field.Bytes)
		case 5:
			info.LanguageID = field.String()
		case 6:
			info.Selection = decodeCursorRange(field.Bytes)
		case 8:
			info.TotalNumberOfLines = field.Int32()
		case 9:
			info.ContentsStartAtLine = field.Int32()
		case 11:
			value := field.Int32()
			info.AlternativeVersionID = &value
		case 14:
			value := field.Int32()
			info.FileVersion = &value
		case 19:
			info.WorkspaceRootPath = field.String()
		case 20:
			value := field.String()
			info.LineEnding = &value
		}
	}
}

func decodeCursorPosition(payload []byte) *CursorPosition {
	position := &CursorPosition{}
	reader := codec.NewReader(payload)
	for {
		field, ok, err := reader.Next()
		if err != nil || !ok {
			return position
		}
		switch field.Number {
		case 1:
			position.Line = field.Int32()
		case 2:
			position.Column = field.Int32()
		}
	}
}

func decodeCursorRange(payload []byte) *CursorRange {
	selection := &CursorRange{}
	reader := codec.NewReader(payload)
	for {
		field, ok, err := reader.Next()
		if err != nil || !ok {
			return selection
		}
		switch field.Number {
		case 1:
			selection.StartPosition = decodeCursorPosition(field.Bytes)
		case 2:
			selection.EndPosition = decodeCursorPosition(field.Bytes)
		}
	}
}

func decodeCppContextItem(payload []byte) *CppContextItem {
	item := &CppContextItem{}
	reader := codec.NewReader(payload)
	for {
		field, ok, err := reader.Next()
		if err != nil || !ok {
			return item
		}
		switch field.Number {
		case 1:
			item.Contents = field.String()
		case 2:
			value := field.String()
			item.Symbol = &value
		case 3:
			item.RelativeWorkspacePath = field.String()
		case 4:
			item.Score = field.Float32()
		}
	}
}

func decodeCppFileDiffHistory(payload []byte) *CppFileDiffHistory {
	history := &CppFileDiffHistory{}
	reader := codec.NewReader(payload)
	for {
		field, ok, err := reader.Next()
		if err != nil || !ok {
			return history
		}
		switch field.Number {
		case 1:
			history.FileName = field.String()
		case 2:
			history.DiffHistory = append(history.DiffHistory, field.String())
		case 3:
			history.DiffHistoryTimestamps = append(history.DiffHistoryTimestamps, field.Float64())
		}
	}
}

func decodeCppIntentInfo(payload []byte) *CppIntentInfo {
	intent := &CppIntentInfo{}
	reader := codec.NewReader(payload)
	for {
		field, ok, err := reader.Next()
		if err != nil || !ok {
			return intent
		}
		if field.Number == 1 {
			intent.Source = field.String()
		}
	}
}

func decodeCppParameterHint(payload []byte) *CppParameterHint {
	hint := &CppParameterHint{}
	reader := codec.NewReader(payload)
	for {
		field, ok, err := reader.Next()
		if err != nil || !ok {
			return hint
		}
		switch field.Number {
		case 1:
			hint.Label = field.String()
		case 2:
			value := field.String()
			hint.Documentation = &value
		}
	}
}

func decodeAdditionalFile(payload []byte) *AdditionalFile {
	file := &AdditionalFile{}
	reader := codec.NewReader(payload)
	for {
		field, ok, err := reader.Next()
		if err != nil || !ok {
			return file
		}
		switch field.Number {
		case 1:
			file.RelativeWorkspacePath = field.String()
		case 2:
			file.IsOpen = field.Bool()
		case 3:
			file.VisibleRangeContent = append(file.VisibleRangeContent, field.String())
		case 4:
			value := field.Float64()
			file.LastViewedAt = &value
		case 5:
			file.StartLineNumberOneIndexed = append(file.StartLineNumberOneIndexed, field.Int32())
		case 6:
			file.VisibleRanges = append(file.VisibleRanges, decodeLineRange(field.Bytes))
		}
	}
}

func decodeLineRange(payload []byte) *LineRange {
	lineRange := &LineRange{}
	reader := codec.NewReader(payload)
	for {
		field, ok, err := reader.Next()
		if err != nil || !ok {
			return lineRange
		}
		switch field.Number {
		case 1:
			lineRange.StartLineNumber = field.Int32()
		case 2:
			lineRange.EndLineNumberInclusive = field.Int32()
		}
	}
}

func decodeFileVisibleRange(payload []byte) *FileVisibleRange {
	visible := &FileVisibleRange{}
	reader := codec.NewReader(payload)
	for {
		field, ok, err := reader.Next()
		if err != nil || !ok {
			return visible
		}
		switch field.Number {
		case 1:
			visible.Filename = field.String()
		case 2:
			visible.VisibleRanges = append(visible.VisibleRanges, decodeVisibleRange(field.Bytes))
		}
	}
}

func decodeVisibleRange(payload []byte) *VisibleRange {
	visibleRange := &VisibleRange{}
	reader := codec.NewReader(payload)
	for {
		field, ok, err := reader.Next()
		if err != nil || !ok {
			return visibleRange
		}
		switch field.Number {
		case 1:
			visibleRange.StartLineNumberInclusive = field.Int32()
		case 2:
			visibleRange.EndLineNumberExclusive = field.Int32()
		}
	}
}
