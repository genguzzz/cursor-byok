package appdata

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func ensureAssistantHome() error {
	migrateLegacyAssistantHome()
	if err := os.MkdirAll(RootDir(), 0o755); err != nil {
		return fmt.Errorf("create assistant home: %w", err)
	}
	if err := os.MkdirAll(DataRootPath(), 0o755); err != nil {
		return fmt.Errorf("create data root: %w", err)
	}
	if err := os.MkdirAll(HistoryRootPath(), 0o755); err != nil {
		return fmt.Errorf("create history root: %w", err)
	}
	if err := os.MkdirAll(RulesRootPath(), 0o755); err != nil {
		return fmt.Errorf("create rules root: %w", err)
	}
	if err := os.MkdirAll(LogsRootPath(), 0o755); err != nil {
		return fmt.Errorf("create logs root: %w", err)
	}
	return nil
}

func EnsureAssistantHome() error {
	return ensureAssistantHome()
}

func migrateLegacyAssistantHome() {
	legacyRoot := legacyRootDir()
	copyLegacyFile(filepath.Join(legacyRoot, "config.yaml"), filepath.Join(RootDir(), "config.yaml"))
	copyLegacyRules(filepath.Join(legacyRoot, "rules"), RulesRootPath())
	_ = os.RemoveAll(legacyRoot)
}

func copyLegacyRules(sourceRoot string, targetRoot string) {
	_ = filepath.Walk(sourceRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return nil
		}
		targetPath := filepath.Join(targetRoot, rel)
		if info.IsDir() {
			_ = os.MkdirAll(targetPath, info.Mode().Perm())
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		copyLegacyFile(path, targetPath)
		return nil
	})
}

func copyLegacyFile(sourcePath string, targetPath string) {
	if _, err := os.Stat(targetPath); err == nil {
		return
	}
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return
	}
	defer sourceFile.Close()

	info, err := sourceFile.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	data, err := io.ReadAll(sourceFile)
	if err != nil {
		return
	}
	_ = WriteFileAtomic(targetPath, data, info.Mode().Perm())
}
