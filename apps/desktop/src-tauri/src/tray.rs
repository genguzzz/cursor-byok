use std::time::Duration;

use cursor_server::harness::{CursorHarness, CursorHarnessStatus, IntegrationState};
use tauri::{
    menu::{CheckMenuItem, Menu, MenuItem, PredefinedMenuItem},
    tray::TrayIconBuilder,
    App, AppHandle, Manager,
};

#[cfg(target_os = "windows")]
use tauri::tray::{MouseButton, MouseButtonState, TrayIconEvent};

use crate::desktop::MAIN_WINDOW_LABEL;

const OPEN_MENU_ID: &str = "tray-open";
const INTEGRATION_MENU_ID: &str = "tray-integration";
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
            &status,
            &PredefinedMenuItem::separator(app)?,
            &restore,
            &PredefinedMenuItem::separator(app)?,
            &quit,
        ],
    )?;

    let items = StatusItems {
        integration: integration.clone(),
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
                Ok(status) => apply_status(&app, &status),
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

fn apply_status(app: &AppHandle, status: &CursorHarnessStatus) {
    let Some(items) = app.try_state::<StatusItems>() else {
        return;
    };
    let enabled = matches!(status.integration, IntegrationState::Enabled);
    let _ = items.integration.set_checked(enabled);
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
    tauri::async_runtime::spawn(async move {
        let enable = !app
            .try_state::<StatusItems>()
            .and_then(|items| items.integration.is_checked().ok())
            .unwrap_or(false);
        match harness.set_enabled(enable).await {
            Ok(status) => apply_status(&app, &status),
            Err(error) => {
                tracing::warn!(%error, enable, "tray could not change Cursor integration");
                if let Some(items) = app.try_state::<StatusItems>() {
                    // Roll the checkmark back so it keeps reflecting reality.
                    let _ = items.integration.set_checked(!enable);
                    let _ = items.status.set_text(format!("状态: 操作失败 · {error}"));
                }
            }
        }
    });
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
        if let Err(error) = result {
            tracing::warn!(%error, "tray could not restore Cursor settings");
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
    use cursor_server::harness::CaState;

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
