package appdata

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WriteFileAtomic 将 data 原子写入 path：先写同目录唯一临时文件再 rename。
// 临时名带 pid 与纳秒，避免多进程共用 path+".tmp" 互相覆盖。
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if path == "" {
		return fmt.Errorf("atomic write path is empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.%d.%d.tmp", filepath.Base(path), os.Getpid(), time.Now().UnixNano()))
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}
