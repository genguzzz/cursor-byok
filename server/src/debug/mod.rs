//! In-process Cursor traffic debugger: an HTTPS MITM proxy plus a web UI that
//! renders the three capture hops of a BYOK conversation.
//!
//! Data flow (mirrors the legacy Go `cursor-proxy-debugger`):
//!
//! ```text
//! Cursor ──(hop 1, client)──▶ MITM proxy ──▶ BYOK server
//! BYOK   ──(hop 2, upstream)──▶ official api2.cursor.sh
//! BYOK   ──(hop 3, provider)──▶ model provider gateway
//! ```
//!
//! Hops 2 and 3 are emitted from the existing forward/provider paths through
//! the global capture store; hop 1 is captured inside this crate's own MITM
//! proxy (`CursorRelay`). All three feed the same [`store::ExchangeStore`],
//! which the embedded web UI polls over `/api/exchanges` and `/api/events`.
pub mod capture;
pub mod decode;
pub mod model;
pub mod server;
pub mod store;

pub use capture::{ProviderHop, UpstreamHop};
pub use model::{CaptureSource, ServerKind, Side};
pub use server::{DebugController, DebugServer, DebugServerConfig, DebugStatus};
