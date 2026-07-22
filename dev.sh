#!/bin/bash
set -e

# ============================================================================
# cursor-byok 开发脚本
# 用法:
#   ./dev.sh          — 调试运行菜单栏程序（前台，完整日志）
#   ./dev.sh cli      — 调试运行 CLI 版本（前台，完整日志）
#   ./dev.sh install  — 编译并安装 .app 到 /Applications
#   ./dev.sh off      — 恢复 Cursor 原始账号（等同 cursor-local-assistant off）
#   ./dev.sh kill     — 杀掉所有相关进程
# ============================================================================

PROJECT_ROOT="$(cd "$(dirname "$0")" && pwd)"
APP_NAME="cursor-menubar"
CLI_NAME="cursor-local-assistant"
LOG_FILE="$HOME/.cursor-local-assistant-v2/logs/app.log"
CLI_LOG_FILE="$HOME/.cursor-local-assistant-v2/logs/cli-subprocess.log"
INSTALL_PATH="/Applications/CursorLocalAssistant.app"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

info()  { echo -e "${CYAN}==> $1${NC}"; }
ok()    { echo -e "${GREEN}    ✓ $1${NC}"; }
warn()  { echo -e "${YELLOW}    ! $1${NC}"; }
fail()  { echo -e "${RED}    ✗ $1${NC}"; }

# ── 杀掉已有进程 ──────────────────────────────────────────────────────────
kill_existing() {
    info "清理已有进程..."
    local killed=0
    if pgrep -f "$APP_NAME" >/dev/null 2>&1; then
        pkill -f "$APP_NAME" 2>/dev/null || true
        killed=1
    fi
    if pgrep -f "$CLI_NAME" | grep -v $$ >/dev/null 2>&1; then
        pkill -f "$CLI_NAME" 2>/dev/null || true
        killed=1
    fi
    if [ "$killed" = "1" ]; then
        sleep 1
        ok "已清理旧进程"
    else
        ok "无运行中的进程"
    fi
}

# ── 编译全部 ────────────────────────────────────────────────────────────────
build_all() {
    info "编译菜单栏程序 + CLI..."
    cd "$PROJECT_ROOT"
    go build -tags cli -o "$APP_NAME" ./cmd/menubar 2>&1
    go build -tags cli -o "$CLI_NAME" ./cmd/cli 2>&1
    ok "编译完成: $APP_NAME + $CLI_NAME"
}

# ── 编译 CLI ───────────────────────────────────────────────────────────────
build_cli() {
    info "编译 CLI..."
    cd "$PROJECT_ROOT"
    go build -tags cli -o "$CLI_NAME" ./cmd/cli 2>&1
    ok "编译完成: $CLI_NAME"
}

# ── 调试运行菜单栏程序 ─────────────────────────────────────────────────────
cmd_debug() {
    kill_existing
    build_all
    info "启动菜单栏程序（前台模式，Ctrl+C 退出）..."
    echo ""
    if [ -f "$LOG_FILE" ]; then
        ( tail -f "$LOG_FILE" "$CLI_LOG_FILE" 2>/dev/null ) &
        TAIL_PID=$!
        trap "kill $TAIL_PID 2>/dev/null; true" EXIT INT TERM
        warn "日志文件 tail PID=$TAIL_PID"
    fi
    "$PROJECT_ROOT/$APP_NAME" 2>&1
    echo ""
    info "菜单栏程序已退出"
}

# ── 调试运行 CLI ───────────────────────────────────────────────────────────
cmd_debug_cli() {
    kill_existing
    build_cli
    info "启动 CLI（前台模式，Ctrl+C 退出）..."
    echo ""
    if [ -f "$LOG_FILE" ]; then
        ( tail -f "$LOG_FILE" "$CLI_LOG_FILE" 2>/dev/null ) &
        TAIL_PID=$!
        trap "kill $TAIL_PID 2>/dev/null; true" EXIT INT TERM
    fi
    "$PROJECT_ROOT/$CLI_NAME" 2>&1
    echo ""
    info "CLI 已退出"
}

# ── 安装到 /Applications ───────────────────────────────────────────────────
cmd_install() {
    kill_existing
    build_all
    info "安装到 $INSTALL_PATH ..."
    rm -rf "$INSTALL_PATH"
    mkdir -p "$INSTALL_PATH/Contents/MacOS"
    mkdir -p "$INSTALL_PATH/Contents/Resources"
    # 拷贝两个二进制：菜单栏程序 + CLI
    cp "$PROJECT_ROOT/$APP_NAME" "$INSTALL_PATH/Contents/MacOS/"
    cp "$PROJECT_ROOT/$CLI_NAME" "$INSTALL_PATH/Contents/MacOS/"
    chmod +x "$INSTALL_PATH/Contents/MacOS/$APP_NAME"
    chmod +x "$INSTALL_PATH/Contents/MacOS/$CLI_NAME"
    # 生成 Info.plist
    cat > "$INSTALL_PATH/Contents/Info.plist" << 'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key>
    <string>Cursor Local Assistant</string>
    <key>CFBundleDisplayName</key>
    <string>Cursor Local Assistant</string>
    <key>CFBundleIdentifier</key>
    <string>com.cursor.local-assistant</string>
    <key>CFBundleVersion</key>
    <string>1.0.0</string>
    <key>CFBundleShortVersionString</key>
    <string>1.0.0</string>
    <key>CFBundleExecutable</key>
    <string>cursor-menubar</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>LSMinimumSystemVersion</key>
    <string>11.0</string>
    <key>LSUIElement</key>
    <true/>
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>NSAppTransportSecurity</key>
    <dict>
        <key>NSAllowsLocalNetworking</key>
        <true/>
    </dict>
</dict>
</plist>
PLIST
    ok "已安装到 $INSTALL_PATH"
    echo ""
    echo "  启动:  open '$INSTALL_PATH'"
    echo "  卸载:  rm -rf '$INSTALL_PATH'"
    echo ""
    info "提示: 首次运行可能需要在系统设置中允许运行"
}

# ── 恢复账号 ──────────────────────────────────────────────────────────────
cmd_off() {
    kill_existing
    build_cli
    info "恢复 Cursor 原始账号..."
    "$PROJECT_ROOT/$CLI_NAME" off
}

# ── 入口 ──────────────────────────────────────────────────────────────────
case "${1:-debug}" in
    debug)
        cmd_debug
        ;;
    cli)
        cmd_debug_cli
        ;;
    install)
        cmd_install
        ;;
    off)
        cmd_off
        ;;
    kill)
        kill_existing
        ;;
    *)
        echo "cursor-byok 开发脚本"
        echo ""
        echo "用法: $0 [命令]"
        echo ""
        echo "命令:"
        echo "  debug     调试运行菜单栏程序（默认，前台完整日志）"
        echo "  cli       调试运行 CLI 版本（前台完整日志）"
        echo "  install   编译并安装 .app 到 /Applications"
        echo "  off       恢复 Cursor 原始账号鉴权"
        echo "  kill      杀掉所有相关进程"
        echo ""
        exit 1
        ;;
esac
