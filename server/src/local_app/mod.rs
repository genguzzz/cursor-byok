//! Exposes the local desktop application integration.
mod account;
pub mod ca;
mod proxy;
mod settings;

use std::{net::SocketAddr, sync::Arc};

use parking_lot::RwLock;
use serde::{Deserialize, Serialize};
use tokio::sync::Mutex;

use crate::{
    debug::{DebugController, DebugServer, DebugServerConfig, DebugStatus},
    store::{ProxyMode, ProxySettingsInput, Store, TabMode, TabSettings},
    Error, Result,
};

pub const PROXYMAN_PROXY_ADDR: &str = "http://127.0.0.1:9090";

use self::{ca::CaManager, proxy::ProxyRuntime};

pub(crate) fn proxy_host_allowed(host: &str) -> bool {
    proxy::is_cursor_host(host)
}

fn integration_prerequisites_ready(ca: &CaState, backend_ready: bool) -> bool {
    matches!(ca, CaState::Ready) && backend_ready
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum CaState {
    Missing,
    Untrusted,
    Ready,
    Invalid,
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum IntegrationState {
    Disabled,
    Enabled,
    Degraded,
}

#[derive(Clone, Debug, Serialize)]
pub struct CursorHarnessStatus {
    pub platform: &'static str,
    pub ca: CaState,
    pub configured_models: usize,
    pub enabled_models: usize,
    pub integration: IntegrationState,
    pub proxy_url: Option<String>,
    pub ca_install_command: Option<String>,
}

#[derive(Clone, Copy, Debug, Deserialize)]
pub struct SetEnabled {
    pub enabled: bool,
}

#[derive(Clone)]
pub struct CursorHarness {
    inner: Arc<Inner>,
}

struct Inner {
    store: Store,
    ca: CaManager,
    ca_initialization: Mutex<()>,
    backend_addr: RwLock<Option<SocketAddr>>,
    tab_mode: Arc<RwLock<TabMode>>,
    proxy: Mutex<ProxyRuntime>,
    debug: tokio::sync::Mutex<Option<DebugController>>,
}

impl CursorHarness {
    pub fn new(store: Store) -> Result<Self> {
        Ok(Self {
            inner: Arc::new(Inner {
                store,
                ca: CaManager::managed()?,
                ca_initialization: Mutex::new(()),
                backend_addr: RwLock::new(None),
                tab_mode: Arc::new(RwLock::new(TabMode::default())),
                proxy: Mutex::new(ProxyRuntime::default()),
                debug: tokio::sync::Mutex::new(None),
            }),
        })
    }

    pub fn set_backend_addr(&self, addr: SocketAddr) {
        *self.inner.backend_addr.write() = Some(addr);
    }

    pub async fn cleanup_stale_settings(&self) -> Result<()> {
        settings::clear_stale_managed_settings()
    }

    pub async fn status(&self) -> Result<CursorHarnessStatus> {
        let models = self.inner.store.models().await?;
        let configured_models = models.len();
        let enabled_models = configured_models;
        let ca = self.inner.ca.state()?;
        if integration_prerequisites_ready(&ca, self.inner.backend_addr.read().is_some()) {
            self.enable().await?;
        }
        let proxy = self.inner.proxy.lock().await;
        let proxy_url = proxy.url();
        let settings_applied = proxy_url
            .as_deref()
            .map(settings::settings_match)
            .transpose()?
            .unwrap_or(false);
        let integration = match (proxy.running(), settings_applied) {
            (false, false) => IntegrationState::Disabled,
            (true, true) => IntegrationState::Enabled,
            _ => IntegrationState::Degraded,
        };
        Ok(CursorHarnessStatus {
            platform: std::env::consts::OS,
            ca,
            configured_models,
            enabled_models,
            integration,
            proxy_url,
            ca_install_command: self.inner.ca.install_command(),
        })
    }

    pub async fn initialize_ca(&self) -> Result<CursorHarnessStatus> {
        let _initialization = self.inner.ca_initialization.lock().await;
        let manager = self.inner.ca.clone();
        tokio::task::spawn_blocking(move || manager.initialize_local())
            .await
            .map_err(|error| Error::Store(format!("CA initialization task failed: {error}")))??;
        self.status().await
    }

    pub async fn set_enabled(&self, enabled: bool) -> Result<CursorHarnessStatus> {
        if enabled {
            self.enable().await?;
        } else {
            self.disable().await?;
        }
        self.status().await
    }

    pub async fn set_tab_settings(&self, settings: TabSettings) -> Result<TabSettings> {
        let saved = self.inner.store.set_tab_settings(settings).await?;
        *self.inner.tab_mode.write() = saved.mode;
        Ok(saved)
    }

    pub async fn proxyman_proxy_enabled(&self) -> Result<bool> {
        let settings = self.inner.store.proxy_settings_secret().await?;
        Ok(settings.mode.is_custom() && settings.address == PROXYMAN_PROXY_ADDR)
    }

    pub async fn set_proxyman_proxy(&self, enabled: bool) -> Result<bool> {
        let input = if enabled {
            ProxySettingsInput {
                mode: ProxyMode::Custom,
                address: PROXYMAN_PROXY_ADDR.into(),
                auth_enabled: false,
                username: String::new(),
                password: None,
            }
        } else {
            ProxySettingsInput {
                mode: ProxyMode::System,
                address: String::new(),
                auth_enabled: false,
                username: String::new(),
                password: None,
            }
        };
        self.inner.store.set_proxy_settings(input).await?;
        Ok(enabled)
    }

    /// 返回调试模式（MITM 抓包）是否已开启。
    pub async fn debug_enabled(&self) -> bool {
        let debug = self.inner.debug.lock().await;
        debug
            .as_ref()
            .is_some_and(|controller| matches!(controller.status(), DebugStatus::Running))
    }

    /// 返回抓包 WebUI 地址（调试开启后才有值）。
    pub async fn debug_ui_addr(&self) -> Option<String> {
        let debug = self.inner.debug.lock().await;
        debug
            .as_ref()
            .filter(|controller| matches!(controller.status(), DebugStatus::Running))
            .map(|controller| format!("http://{}", controller.ui_addr()))
    }

    /// 开启/关闭「调试模式」：装载抓包 store + 起 WebUI，并同步「调用观测 · 详细模式」。
    ///
    /// 开启时安装进程级 capture store（hop1 由主代理抓取，hop2/3 由转发路径抓取）、
    /// 启动 WebUI，并把 `llm_detailed_logging` 置为 true；关闭时反之。
    pub async fn set_debug_enabled(&self, enabled: bool) -> Result<()> {
        if enabled {
            self.enable_debug().await
        } else {
            self.disable_debug().await
        }
    }

    /// 打开调试模式：装载 store + 起 WebUI + 同步详细日志。
    async fn enable_debug(&self) -> Result<()> {
        // 主代理可能尚未启动（未接管 Cursor），此时抓不到 hop1，但 hop2/3 仍可抓。
        let main_proxy_url = {
            let proxy = self.inner.proxy.lock().await;
            proxy.url().unwrap_or_default()
        };
        let mut debug = self.inner.debug.lock().await;
        if debug.is_none() {
            let mut config = DebugServerConfig::default();
            config.proxy_addr = main_proxy_url;
            let controller = DebugServer::new(config)
                .build()
                .map_err(Error::Config)?;
            controller.install_store();
            *debug = Some(controller);
        }
        let controller = debug.as_ref().expect("debug controller present");
        if matches!(controller.status(), DebugStatus::Stopped) {
            controller.start().await.map_err(Error::Config)?;
        }
        drop(debug);
        // 同步「调用观测 · 详细模式」。
        self.inner.store.set_detailed_logging(true).await?;
        Ok(())
    }

    async fn disable_debug(&self) -> Result<()> {
        // 同步关闭「调用观测 · 详细模式」。
        self.inner.store.set_detailed_logging(false).await?;
        let debug = self.inner.debug.lock().await;
        if let Some(controller) = debug.as_ref() {
            controller.stop().await;
        }
        Ok(())
    }

    async fn enable(&self) -> Result<()> {
        if !matches!(self.inner.ca.state()?, CaState::Ready) {
            return Err(Error::Config(
                "initialize and trust the CA before enabling Cursor".into(),
            ));
        }
        let backend_addr = self
            .inner
            .backend_addr
            .read()
            .ok_or_else(|| Error::Config("desktop management server is not ready".into()))?;
        let mut proxy = self.inner.proxy.lock().await;
        if proxy.running() {
            if let Some(url) = proxy.url() {
                apply_cursor_configuration(&url).await?;
            }
            return Ok(());
        }
        let ca = self.inner.ca.load()?;
        let requested_port = self.inner.store.port_settings().await?.proxy_port;
        *self.inner.tab_mode.write() = self.inner.store.tab_settings().await?.mode;
        let (url, actual_port) = proxy
            .start(
                backend_addr,
                ca,
                requested_port,
                self.inner.tab_mode.clone(),
            )
            .await?;
        if let Err(error) = self.inner.store.set_proxy_port(actual_port).await {
            proxy.stop().await;
            return Err(error);
        }
        if let Err(error) = apply_cursor_configuration(&url).await {
            proxy.stop().await;
            return Err(error);
        }
        Ok(())
    }

    pub async fn disable(&self) -> Result<()> {
        settings::clear_proxy_settings()?;
        self.inner.proxy.lock().await.stop().await;
        Ok(())
    }
}

async fn apply_cursor_configuration(proxy_url: &str) -> Result<()> {
    account::inject_if_missing().await?;
    settings::write_proxy_settings(proxy_url)
}
