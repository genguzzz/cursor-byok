package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"cursor/internal/appdata"

	"gopkg.in/yaml.v3"
)

const lastGoodBackupSuffix = ".bak-last-good"

var (
	// ErrDestructiveConfigWrite 表示即将用空 modelAdapters 覆盖已有非空配置。
	ErrDestructiveConfigWrite = errors.New("拒绝用空 modelAdapters 覆盖已有配置")
	// ErrEmptyConfigFile 表示磁盘上的 config.yaml 为空或只有空白。
	ErrEmptyConfigFile = errors.New("config.yaml 为空")
)

type Store struct {
	path     string
	logsRoot string
	mu       sync.Mutex
}

type fileSnapshot struct {
	exists  bool
	modTime int64
	size    int64
}

func NewStore(path string, logsRoot string) *Store {
	return &Store{
		path:     strings.TrimSpace(path),
		logsRoot: strings.TrimSpace(logsRoot),
	}
}

func (store *Store) Path() string {
	if store == nil {
		return ""
	}
	return store.path
}

func (store *Store) LogsRoot() string {
	if store == nil {
		return ""
	}
	return store.logsRoot
}

func (store *Store) snapshot() fileSnapshot {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return fileSnapshot{}
	}
	info, err := os.Stat(store.path)
	if err != nil {
		return fileSnapshot{}
	}
	return fileSnapshot{
		exists:  true,
		modTime: info.ModTime().UnixNano(),
		size:    info.Size(),
	}
}

func (store *Store) lastGoodBackupPath() string {
	return store.path + lastGoodBackupSuffix
}

func (store *Store) Load(_ context.Context) (Config, error) {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return DefaultConfig(), nil
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	data, err := os.ReadFile(store.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			defaultConfig := DefaultConfig()
			if err := store.saveLocked(defaultConfig); err != nil {
				return DefaultConfig(), err
			}
			return defaultConfig, nil
		}
		return DefaultConfig(), fmt.Errorf("读取用户配置失败: %w", err)
	}

	cfg, err := store.parseAndMaybePersistLocked(data)
	if err == nil {
		return cfg, nil
	}
	if store.restoreLastGoodLocked() {
		restored, readErr := os.ReadFile(store.path)
		if readErr != nil {
			return DefaultConfig(), fmt.Errorf("恢复 last-good 后读取失败: %w", readErr)
		}
		return store.parseAndMaybePersistLocked(restored)
	}
	return DefaultConfig(), err
}

func (store *Store) parseAndMaybePersistLocked(data []byte) (Config, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return DefaultConfig(), ErrEmptyConfigFile
	}

	var current Config
	if err := yaml.Unmarshal(data, &current); err != nil {
		return DefaultConfig(), fmt.Errorf("解析用户配置失败: %w", err)
	}
	normalized, err := NormalizeConfig(current)
	if err != nil {
		return DefaultConfig(), err
	}
	probed, probeOK := probeModelAdapterCount(data)
	if probeOK && probed > 0 && len(normalized.ModelAdapters) == 0 {
		return DefaultConfig(), fmt.Errorf("config.yaml 含 %d 个 modelAdapters 但规范化后为空，拒绝使用", probed)
	}
	if !probeOK && looksLikeModelAdapterYAML(data) && len(normalized.ModelAdapters) == 0 {
		return DefaultConfig(), errors.New("config.yaml 含模型配置痕迹但无法解析 modelAdapters，拒绝使用")
	}
	if shouldPersistNormalizedConfig(data, current, normalized) {
		if err := store.saveLocked(normalized); err != nil {
			return DefaultConfig(), err
		}
	}
	return normalized, nil
}

func (store *Store) Save(_ context.Context, cfg Config) (Config, error) {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return Config{}, errors.New("配置存储未初始化")
	}

	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		return Config{}, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	if err := store.saveLocked(normalized); err != nil {
		return Config{}, err
	}
	return normalized, nil
}

func (store *Store) saveLocked(normalized Config) error {
	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		return fmt.Errorf("创建用户配置目录失败: %w", err)
	}

	existing, err := os.ReadFile(store.path)
	if err == nil {
		if err := rejectDestructiveAdapterOverwrite(existing, len(normalized.ModelAdapters)); err != nil {
			log.Printf("config write refused path=%s incoming_adapters=%d error=%v", store.path, len(normalized.ModelAdapters), err)
			return err
		}
		if count, ok := probeModelAdapterCount(existing); ok && count > 0 {
			if bakErr := appdata.WriteFileAtomic(store.lastGoodBackupPath(), existing, 0o644); bakErr != nil {
				log.Printf("config last-good backup failed path=%s error=%v", store.lastGoodBackupPath(), bakErr)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("读取现有配置失败: %w", err)
	}

	data, err := yaml.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("序列化用户配置失败: %w", err)
	}
	if err := appdata.WriteFileAtomic(store.path, data, 0o644); err != nil {
		return fmt.Errorf("保存用户配置失败: %w", err)
	}
	return nil
}

func (store *Store) restoreLastGoodLocked() bool {
	backupPath := store.lastGoodBackupPath()
	data, err := os.ReadFile(backupPath)
	if err != nil {
		return false
	}
	count, ok := probeModelAdapterCount(data)
	if !ok || count <= 0 {
		return false
	}
	if err := appdata.WriteFileAtomic(store.path, data, 0o644); err != nil {
		log.Printf("config restore last-good failed path=%s error=%v", backupPath, err)
		return false
	}
	log.Printf("config restored from last-good backup path=%s adapters=%d", backupPath, count)
	return true
}

func rejectDestructiveAdapterOverwrite(existing []byte, incomingCount int) error {
	if incomingCount > 0 {
		return nil
	}
	existingCount, ok := probeModelAdapterCount(existing)
	if ok && existingCount > 0 {
		return fmt.Errorf("%w: 磁盘已有 %d 个模型渠道", ErrDestructiveConfigWrite, existingCount)
	}
	if !ok && looksLikeModelAdapterYAML(existing) {
		return fmt.Errorf("%w: 磁盘配置含模型字段但无法解析数量", ErrDestructiveConfigWrite)
	}
	return nil
}

func probeModelAdapterCount(raw []byte) (int, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, false
	}
	var root map[string]any
	if err := yaml.Unmarshal(raw, &root); err != nil || root == nil {
		return 0, false
	}
	value, exists := root["modelAdapters"]
	if !exists || value == nil {
		return 0, true
	}
	list, ok := value.([]any)
	if !ok {
		return 0, false
	}
	return len(list), true
}

func looksLikeModelAdapterYAML(raw []byte) bool {
	return bytes.Contains(raw, []byte("modelID:")) &&
		bytes.Contains(raw, []byte("displayName:")) &&
		(bytes.Contains(raw, []byte("baseURL:")) || bytes.Contains(raw, []byte("apiKey:")))
}

func shouldPersistNormalizedConfig(raw []byte, current Config, normalized Config) bool {
	if yamlHasKey(raw, "routing") {
		return true
	}
	if !yamlHasKey(raw, "backendListenAddr") || !yamlHasKey(raw, "proxyListenAddr") {
		return true
	}
	if current.BackendListenAddr != normalized.BackendListenAddr || current.ProxyListenAddr != normalized.ProxyListenAddr {
		return true
	}
	if current.ProviderStreamIdleTimeout == normalized.ProviderStreamIdleTimeout {
		return false
	}
	return yamlHasKey(raw, "providerStreamIdleTimeout")
}

func yamlHasKey(raw []byte, key string) bool {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return false
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return false
	}
	mapping := root.Content[0]
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return true
		}
	}
	return false
}
