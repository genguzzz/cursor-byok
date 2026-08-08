package cursor

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"cursor/internal/appdata"
	"cursor/internal/logger"

	_ "modernc.org/sqlite"
)

const (
	cursorStateMembershipType      = "ultra"
	cursorStateSubscriptionStatus  = "active"
	cursorStateDefaultSignUpType   = "Google"
	cursorStateSQLiteBusyTimeoutMS = 2000
	cursorStateDBRelativePath      = "Cursor/User/globalStorage/state.vscdb"
	cursorStateDarwinRelativePath  = "Library/Application Support/Cursor/User/globalStorage/state.vscdb"
	cursorStateLinuxRelativePath   = ".config/Cursor/User/globalStorage/state.vscdb"
	cursorStateStatsigBootstrapKey = "workbench.experiments.statsigBootstrap"
)

var cursorStateDisabledStatsigGates = []string{
	"decompose_always_local_ext_host",
	"cursor_extensions_isolation_v2",
	"disable_terminal_output_ui_streaming",
}

// cursorAuthBackupKeys 是启动注入前需要备份的 cursorAuth/* 键列表。
var cursorAuthBackupKeys = []string{
	"cursorAuth/accessToken",
	"cursorAuth/cachedEmail",
	"cursorAuth/cachedSignUpType",
	"cursorAuth/refreshToken",
	"cursorAuth/stripeMembershipType",
	"cursorAuth/stripeSubscriptionStatus",
}

// cursorAuthBackupPath 返回备份文件路径。
func cursorAuthBackupPath() string {
	return filepath.Join(appdata.RootDir(), "cursor_auth_backup.json")
}

// cursorAuthBackupFile 对应备份文件的 JSON 结构。
type cursorAuthBackupFile struct {
	Keys             map[string]string `json:"keys"`
	StatsigBootstrap string            `json:"statsig_bootstrap,omitempty"`
}

// BackupCursorAuthState 读取当前 state.vscdb 中的 cursorAuth/* 和 statsigBootstrap 值，
// 保存到备份文件。在 InjectCursorUserInfo 之前调用，以便后续恢复。
// 如果备份文件已存在则跳过（避免覆盖已有备份）。
func BackupCursorAuthState() error {
	backupPath := cursorAuthBackupPath()
	if _, err := os.Stat(backupPath); err == nil {
		logger.Infof("cursor auth backup already exists, skip path=%s", backupPath)
		return nil
	}

	stateDBPath, err := resolveCursorStateDBPath()
	if err != nil {
		return err
	}

	db, err := sql.Open("sqlite", stateDBPath)
	if err != nil {
		return fmt.Errorf("打开 state.vscdb 失败: %w", err)
	}
	defer db.Close()

	backup := cursorAuthBackupFile{
		Keys: make(map[string]string),
	}

	for _, key := range cursorAuthBackupKeys {
		var raw []byte
		err := db.QueryRow("SELECT value FROM ItemTable WHERE key = ?", key).Scan(&raw)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return fmt.Errorf("读取 %s 失败: %w", key, err)
		}
		backup.Keys[key] = base64.StdEncoding.EncodeToString(raw)
	}

	var statsigRaw []byte
	err = db.QueryRow("SELECT value FROM ItemTable WHERE key = ?", cursorStateStatsigBootstrapKey).Scan(&statsigRaw)
	if err == nil {
		backup.StatsigBootstrap = base64.StdEncoding.EncodeToString(statsigRaw)
	} else if !errors.Is(err, sql.ErrNoRows) {
		logger.Errorf("读取 cursorAuth/statsigBootstrap 失败: %v，备份将不含此字段", err)
	}

	data, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化备份失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
		return fmt.Errorf("创建备份目录失败: %w", err)
	}
	if err := os.WriteFile(backupPath, data, 0o600); err != nil {
		return fmt.Errorf("写入备份文件失败: %w", err)
	}
	logger.Infof("cursor auth backed up path=%s keys=%d", backupPath, len(backup.Keys))
	return nil
}

// RestoreCursorAuthState 从备份文件恢复 cursorAuth/* 和 statsigBootstrap 值到 state.vscdb。
// 恢复后删除备份文件。如果备份文件不存在则返回 nil（无需恢复）。
func RestoreCursorAuthState() error {
	backupPath := cursorAuthBackupPath()
	data, err := os.ReadFile(backupPath)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Infof("cursor auth backup not found, nothing to restore")
			return nil
		}
		return fmt.Errorf("读取备份文件失败: %w", err)
	}

	var backup cursorAuthBackupFile
	if err := json.Unmarshal(data, &backup); err != nil {
		return fmt.Errorf("解析备份文件失败: %w", err)
	}

	stateDBPath, err := resolveCursorStateDBPath()
	if err != nil {
		return err
	}

	db, err := sql.Open("sqlite", stateDBPath)
	if err != nil {
		return fmt.Errorf("打开 state.vscdb 失败: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", cursorStateSQLiteBusyTimeoutMS)); err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// 恢复 cursorAuth/* 键：备份中有的写入，备份中没有的删除
	for _, key := range cursorAuthBackupKeys {
		if encoded, ok := backup.Keys[key]; ok {
			raw, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return fmt.Errorf("解码 %s 失败: %w", key, err)
			}
			if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO ItemTable(key, value) VALUES(?, ?)", key, raw); err != nil {
				return fmt.Errorf("恢复 %s 失败: %w", key, err)
			}
		} else {
			if _, err := tx.ExecContext(ctx, "DELETE FROM ItemTable WHERE key = ?", key); err != nil {
				return fmt.Errorf("删除 %s 失败: %w", key, err)
			}
		}
	}

	// 恢复 statsigBootstrap
	if backup.StatsigBootstrap != "" {
		raw, err := base64.StdEncoding.DecodeString(backup.StatsigBootstrap)
		if err != nil {
			return fmt.Errorf("解码 statsigBootstrap 失败: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO ItemTable(key, value) VALUES(?, ?)", cursorStateStatsigBootstrapKey, raw); err != nil {
			return fmt.Errorf("恢复 statsigBootstrap 失败: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, "DELETE FROM ItemTable WHERE key = ?", cursorStateStatsigBootstrapKey); err != nil {
			return fmt.Errorf("删除 statsigBootstrap 失败: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交恢复事务失败: %w", err)
	}
	committed = true

	// 删除备份文件
	_ = os.Remove(backupPath)
	logger.Infof("cursor auth restored from backup and backup file removed")
	return nil
}

// PatchCursorStatsigGates disables a small always-local stability gate set in
// state.vscdb without rewriting the user's access/refresh tokens.
func PatchCursorStatsigGates() error {
	stateDBPath, err := resolveCursorStateDBPath()
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", stateDBPath)
	if err != nil {
		return fmt.Errorf("打开 state.vscdb 失败: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", cursorStateSQLiteBusyTimeoutMS)); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)"); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := disableCursorStatsigGates(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	logger.Infof("patched Cursor statsig gates without rewriting auth path=%s gates=%s", stateDBPath, strings.Join(cursorStateDisabledStatsigGates, ","))
	return nil
}

// InjectCursorUserInfo synchronizes the Cursor user-level auth cache used by the
// Settings page. It does not modify the installed Cursor app bundle.
func InjectCursorUserInfo(email, token string) error {
	stateDBPath, err := resolveCursorStateDBPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(stateDBPath), 0o755); err != nil {
		return fmt.Errorf("创建 Cursor 状态目录失败: %w", err)
	}

	values := buildCursorAuthStateValues(email, token)
	if err := syncCursorAuthStateDB(stateDBPath, values); err != nil {
		return fmt.Errorf("同步 Cursor 状态库失败 path=%s: %w", stateDBPath, err)
	}

	logger.Infof(
		"injectCursorUserInfo synced path=%s email=%s membership=%s subscription=%s disabled_statsig_gates=%s",
		stateDBPath,
		values["cursorAuth/cachedEmail"],
		values["cursorAuth/stripeMembershipType"],
		values["cursorAuth/stripeSubscriptionStatus"],
		strings.Join(cursorStateDisabledStatsigGates, ","),
	)
	return nil
}

func buildCursorAuthStateValues(email, token string) map[string]string {
	email = strings.TrimSpace(email)
	token = strings.TrimSpace(token)

	return map[string]string{
		"cursorAuth/accessToken":              token,
		"cursorAuth/cachedEmail":              email,
		"cursorAuth/cachedSignUpType":         cursorStateDefaultSignUpType,
		"cursorAuth/refreshToken":             token,
		"cursorAuth/stripeMembershipType":     cursorStateMembershipType,
		"cursorAuth/stripeSubscriptionStatus": cursorStateSubscriptionStatus,
	}
}

func syncCursorAuthStateDB(path string, values map[string]string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", cursorStateSQLiteBusyTimeoutMS)); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)"); err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	stmt, err := tx.PrepareContext(ctx, "INSERT OR REPLACE INTO ItemTable(key, value) VALUES(?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, key := range keys {
		if _, err := stmt.ExecContext(ctx, key, values[key]); err != nil {
			return err
		}
	}

	if err := disableCursorStatsigGates(ctx, tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func disableCursorStatsigGates(ctx context.Context, tx *sql.Tx) error {
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT value FROM ItemTable WHERE key = ?", cursorStateStatsigBootstrapKey).Scan(&raw)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("解析 Cursor Statsig bootstrap 失败: %w", err)
	}

	featureGates, _ := payload["feature_gates"].(map[string]any)
	if featureGates == nil {
		featureGates = map[string]any{}
		payload["feature_gates"] = featureGates
	}

	hashUsed, _ := payload["hash_used"].(string)
	for _, gate := range cursorStateDisabledStatsigGates {
		disableCursorStatsigGate(featureGates, gate)
		if strings.EqualFold(hashUsed, "djb2") {
			disableCursorStatsigGate(featureGates, cursorStateDJB2Hash(gate))
		}
	}

	updated, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("编码 Cursor Statsig bootstrap 失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE ItemTable SET value = ? WHERE key = ?", updated, cursorStateStatsigBootstrapKey); err != nil {
		return err
	}
	return nil
}

func disableCursorStatsigGate(featureGates map[string]any, key string) {
	gate, _ := featureGates[key].(map[string]any)
	if gate == nil {
		gate = map[string]any{
			"name":       key,
			"rule_id":    "local_disabled",
			"ruleID":     "local_disabled",
			"group_name": "local_disabled",
			"groupName":  "local_disabled",
			"id_type":    "userID",
			"idType":     "userID",
		}
		featureGates[key] = gate
	}
	gate["value"] = false
}

func cursorStateDJB2Hash(value string) string {
	var hash uint32
	for _, b := range []byte(value) {
		hash = hash*31 + uint32(b)
	}
	return fmt.Sprintf("%d", hash)
}

func resolveCursorStateDBPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户目录失败: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(homeDir, filepath.FromSlash(cursorStateDarwinRelativePath)), nil
	case "windows":
		appData := strings.TrimSpace(os.Getenv("APPDATA"))
		if appData == "" {
			appData = filepath.Join(homeDir, "AppData", "Roaming")
		}
		return filepath.Join(appData, "Cursor", "User", "globalStorage", "state.vscdb"), nil
	case "linux":
		configDir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
		if configDir == "" {
			return filepath.Join(homeDir, filepath.FromSlash(cursorStateLinuxRelativePath)), nil
		}
		return filepath.Join(configDir, filepath.FromSlash(cursorStateDBRelativePath)), nil
	default:
		return "", fmt.Errorf("不支持的系统: %s", runtime.GOOS)
	}
}
