use std::time::Duration;

use cursor_server::local_app::{CursorHarness, CursorHarnessStatus, IntegrationState, PROXYMAN_PROXY_ADDR};
use tauri::{
    menu::{CheckMenuItem, Menu, MenuItem, PredefinedMenuItem},
    tray::TrayIconBuilder,
    App, AppHandle, Manager,
};
use tauri_plugin_notification::NotificationExt;
use tauri_plugin_opener::OpenerExt;

#[cfg(target_os = "windows")]
use tauri::tray::{MouseButton, MouseButtonState, TrayIconEvent};

use crate::desktop::MAIN_WINDOW_LABEL;

const OPEN_MENU_ID: &str = "tray-open";
const INTEGRATION_MENU_ID: &str = "tray-integration";
const PROXYMAN_MENU_ID: &str = "tray-proxyman";
const DEBUG_MENU_ID: &str = "tray-debug";
const STATUS_MENU_ID: &str = "tray-status";
const RESTORE_MENU_ID: &str = "tray-restore";
const QUIT_MENU_ID: &str = "tray-quit";

/// How often the menu re-reads harness state.
///
/// The integration can also be toggled from the web console or fall into a
/// degraded state on its own, so the menu polls rather than assuming its own
/// clicks are the only source of change.
const REFRESH_INTERVAL: Duration = Duration::from_secs(2);

/// Menu items whose text or checked state is refreshed from harness status.
struct StatusItems {
    integration: CheckMenuItem<tauri::Wry>,
    proxyman: CheckMenuItem<tauri::Wry>,
    debug: CheckMenuItem<tauri::Wry>,
    status: MenuItem<tauri::Wry>,
}

pub fn create(app: &mut App, harness: CursorHarness) -> tauri::Result<()> {
    let open = MenuItem::with_id(app, OPEN_MENU_ID, "打开 Cursor BYOK", true, None::<&str>)?;
    let integration = CheckMenuItem::with_id(
        app,
        INTEGRATION_MENU_ID,
        "接管 Cursor",
        true,
        false,
        None::<&str>,
    )?;
    let proxyman = CheckMenuItem::with_id(
        app,
        PROXYMAN_MENU_ID,
        "Proxyman 代理 (9090)",
        true,
        false,
        None::<&str>,
    )?;
    let debug = CheckMenuItem::with_id(
        app,
        DEBUG_MENU_ID,
        "调试模式",
        true,
        false,
        None::<&str>,
    )?;
    // Display-only line; disabled so it never looks clickable.
    let status = MenuItem::with_id(app, STATUS_MENU_ID, "状态: 读取中…", false, None::<&str>)?;
    let restore = MenuItem::with_id(app, RESTORE_MENU_ID, "恢复 Cursor 设置", true, None::<&str>)?;
    let quit = MenuItem::with_id(app, QUIT_MENU_ID, "退出", true, None::<&str>)?;
    let menu = Menu::with_items(
        app,
        &[
            &open,
            &PredefinedMenuItem::separator(app)?,
            &integration,
            &proxyman,
            &debug,
            &status,
            &PredefinedMenuItem::separator(app)?,
            &restore,
            &PredefinedMenuItem::separator(app)?,
            &quit,
        ],
    )?;

    let items = StatusItems {
        integration: integration.clone(),
        proxyman: proxyman.clone(),
        debug: debug.clone(),
        status: status.clone(),
    };
    // Items are plain text on purpose: on macOS an icon on one item indents
    // every sibling, which misaligns the whole menu.
    TrayIconBuilder::with_id("main")
        .icon(tauri::include_image!("./icons/32x32.png"))
        .tooltip("Cursor BYOK")
        .menu(&menu)
        .show_menu_on_left_click(cfg!(target_os = "macos"))
        .on_menu_event({
            let harness = harness.clone();
            move |app, event| match event.id().as_ref() {
                OPEN_MENU_ID => show_main_window(app),
                INTEGRATION_MENU_ID => toggle_integration(app.clone(), harness.clone()),
                PROXYMAN_MENU_ID => toggle_proxyman_proxy(app.clone(), harness.clone()),
                DEBUG_MENU_ID => toggle_debug(app.clone(), harness.clone()),
                RESTORE_MENU_ID => restore_settings(app.clone(), harness.clone()),
                QUIT_MENU_ID => app.exit(0),
                _ => {}
            }
        })
        .on_tray_icon_event(|tray, event| {
            #[cfg(target_os = "windows")]
            if let TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Up,
                ..
            } = event
            {
                show_main_window(tray.app_handle());
            }

            #[cfg(not(target_os = "windows"))]
            let _ = (tray, event);
        })
        .build(app)?;

    app.manage(items);
    spawn_status_refresh(app.handle().clone(), harness);
    Ok(())
}

fn spawn_status_refresh(app: AppHandle, harness: CursorHarness) {
    tauri::async_runtime::spawn(async move {
        loop {
            match harness.status().await {
                Ok(status) => apply_status(&app, &status, &harness).await,
                Err(error) => {
                    tracing::debug!(%error, "tray could not read harness status");
                    if let Some(items) = app.try_state::<StatusItems>() {
                        let _ = items.status.set_text("状态: 无法读取");
                    }
                }
            }
            tokio::time::sleep(REFRESH_INTERVAL).await;
        }
    });
}

async fn apply_status(app: &AppHandle, status: &CursorHarnessStatus, harness: &CursorHarness) {
    let Some(items) = app.try_state::<StatusItems>() else {
        return;
    };
    let enabled = matches!(status.integration, IntegrationState::Enabled);
    let _ = items.integration.set_checked(enabled);
    match harness.proxyman_proxy_enabled().await {
        Ok(proxyman_enabled) => {
            let _ = items.proxyman.set_checked(proxyman_enabled);
        }
        Err(error) => {
            tracing::debug!(%error, "tray could not read proxyman proxy state");
        }
    }
    match harness.debug_enabled().await {
        debug_enabled => {
            let _ = items.debug.set_checked(debug_enabled);
        }
    }
    let _ = items.status.set_text(status_text(status));
}

fn status_text(status: &CursorHarnessStatus) -> String {
    match status.integration {
        IntegrationState::Enabled => format!("状态: 已接管 · {} 个模型", status.enabled_models),
        // Degraded means the proxy and Cursor's settings disagree, which the
        // user cannot infer from a checkmark alone.
        IntegrationState::Degraded => "状态: 不一致，请重新接管".into(),
        IntegrationState::Disabled => "状态: 未接管".into(),
    }
}

fn toggle_integration(app: AppHandle, harness: CursorHarness) {
    tracing::info!("tray: 接管 Cursor 菜单项被点击");
    tauri::async_runtime::spawn(async move {
        // 用后端真实状态判断当前是否已接管，而非菜单项勾选状态：
        // macOS 的 CheckMenuItem 在点击时已自动翻转 is_checked，用它会取反错误。
        let current = harness.status().await;
        let enable = match current {
            Ok(status) => !matches!(status.integration, IntegrationState::Enabled),
            Err(_) => false,
        };
        tracing::info!(enable, "tray: 即将 set_enabled");
        match harness.set_enabled(enable).await {
            Ok(status) => {
                tracing::info!(integration = ?status.integration, "tray: set_enabled 成功");
                apply_status(&app, &status, &harness).await;
                notify_integration(&app, enable, None);
            }
            Err(error) => {
                tracing::warn!(%error, enable, "tray could not change Cursor integration");
                if let Some(items) = app.try_state::<StatusItems>() {
                    // Roll the checkmark back so it keeps reflecting reality.
                    let _ = items.integration.set_checked(!enable);
                    let _ = items.status.set_text(format!("状态: 操作失败 · {error}"));
                }
                notify_integration(&app, enable, Some(&error));
            }
        }
    });
}

fn toggle_proxyman_proxy(app: AppHandle, harness: CursorHarness) {
    tracing::info!("tray: Proxyman 代理菜单项被点击");
    tauri::async_runtime::spawn(async move {
        let current = harness.proxyman_proxy_enabled().await;
        let enable = match current {
            Ok(enabled) => !enabled,
            Err(_) => true,
        };
        match harness.set_proxyman_proxy(enable).await {
            Ok(enabled) => {
                if let Some(items) = app.try_state::<StatusItems>() {
                    let _ = items.proxyman.set_checked(enabled);
                }
                let notification = app.notification().builder();
                let _ = notification
                    .title(if enabled {
                        "已开启 Proxyman 代理"
                    } else {
                        "已关闭 Proxyman 代理"
                    })
                    .body(if enabled {
                        format!("出站流量将通过 {PROXYMAN_PROXY_ADDR} 转发")
                    } else {
                        "出站流量恢复为系统代理".into()
                    })
                    .show();
            }
            Err(error) => {
                tracing::warn!(%error, enable, "tray could not change proxyman proxy");
                if let Some(items) = app.try_state::<StatusItems>() {
                    let _ = items.proxyman.set_checked(!enable);
                }
                let notification = app.notification().builder();
                let _ = notification
                    .title("Proxyman 代理切换失败")
                    .body(error.to_string())
                    .show();
            }
        }
    });
}

fn toggle_debug(app: AppHandle, harness: CursorHarness) {
    tracing::info!("tray: 调试模式菜单项被点击");
    tauri::async_runtime::spawn(async move {
        let enable = !harness.debug_enabled().await;
        match harness.set_debug_enabled(enable).await {
            Ok(()) => {
                if let Some(items) = app.try_state::<StatusItems>() {
                    let _ = items.debug.set_checked(enable);
                }
                let notification = app.notification().builder();
                let _ = notification
                    .title(if enable {
                        "已开启调试模式"
                    } else {
                        "已关闭调试模式"
                    })
                    .body(if enable {
                        "已开启抓包，并同步开启调用观测详细日志".to_owned()
                    } else {
                        "已关闭抓包与调用观测详细日志".to_owned()
                    })
                    .show();
                // 开启后自动跳转抓包 WebUI。
                if enable {
                    if let Some(ui_addr) = harness.debug_ui_addr().await {
                        tracing::info!(%ui_addr, "tray: 打开抓包 WebUI");
                        let _ = app.opener().open_url(&ui_addr, None::<&str>);
                    }
                }
            }
            Err(error) => {
                tracing::warn!(%error, enable, "tray could not change debug mode");
                if let Some(items) = app.try_state::<StatusItems>() {
                    let _ = items.debug.set_checked(!enable);
                }
                let notification = app.notification().builder();
                let _ = notification
                    .title("调试模式切换失败")
                    .body(error.to_string())
                    .show();
            }
        }
    });
}

/// 用系统通知告知用户接管开关的结果，避免菜单栏静默失败无从排查。
fn notify_integration(app: &AppHandle, enable: bool, error: Option<&cursor_server::Error>) {
    let (title, body) = match error {
        Some(error) => (
            format!("{} Cursor 失败", if enable { "接管" } else { "关闭接管" }),
            error.to_string(),
        ),
        None => (
            format!("已{} Cursor", if enable { "接管" } else { "关闭接管" }),
            if enable {
                "代理已启动，重启 Cursor 后生效".into()
            } else {
                "已恢复 Cursor 原始设置".into()
            },
        ),
    };
    let notification = app.notification().builder();
    let _ = notification.title(title).body(body).show();
}

fn restore_settings(app: AppHandle, harness: CursorHarness) {
    tauri::async_runtime::spawn(async move {
        let result = harness
            .set_enabled(false)
            .await
            .and(harness.cleanup_stale_settings().await.map(|_| ()));
        if let Some(items) = app.try_state::<StatusItems>() {
            let _ = items.status.set_text(match &result {
                Ok(()) => "状态: 已恢复 Cursor 设置".into(),
                Err(error) => format!("状态: 恢复失败 · {error}"),
            });
        }
        match &result {
            Ok(()) => {
                let notification = app.notification().builder();
                let _ = notification
                    .title("已恢复 Cursor 设置")
                    .body("已清除代理注入，Cursor 恢复原始配置")
                    .show();
            }
            Err(error) => {
                tracing::warn!(%error, "tray could not restore Cursor settings");
                let notification = app.notification().builder();
                let _ = notification
                    .title("恢复 Cursor 设置失败")
                    .body(error.to_string())
                    .show();
            }
        }
    });
}

pub fn show_main_window(app: &AppHandle) {
    // The dock icon is hidden while the window is, so restore it first or the
    // window can come back without an app icon to switch to.
    #[cfg(target_os = "macos")]
    let _ = app.set_dock_visibility(true);
    if let Some(window) = app.get_webview_window(MAIN_WINDOW_LABEL) {
        let _ = window.unminimize();
        let _ = window.show();
        let _ = window.set_focus();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use cursor_server::local_app::CaState;

    fn status(integration: IntegrationState, enabled_models: usize) -> CursorHarnessStatus {
        CursorHarnessStatus {
            platform: "macos",
            ca: CaState::Ready,
            configured_models: enabled_models,
            enabled_models,
            integration,
            proxy_url: Some("http://127.0.0.1:8080".into()),
            ca_install_command: None,
        }
    }

    #[test]
    fn status_text_distinguishes_degraded_from_disabled() {
        assert_eq!(
            status_text(&status(IntegrationState::Enabled, 3)),
            "状态: 已接管 · 3 个模型"
        );
        assert_eq!(
            status_text(&status(IntegrationState::Degraded, 3)),
            "状态: 不一致，请重新接管"
        );
        assert_eq!(
            status_text(&status(IntegrationState::Disabled, 0)),
            "状态: 未接管"
        );
    }
}
