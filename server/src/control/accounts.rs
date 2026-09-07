//! Exposes Cursor account list, switch, and delete endpoints.
use axum::{
    extract::{Path, Query, State},
    http::StatusCode,
    Json,
};
use serde::Deserialize;

use crate::{local_app::CursorAccountView, Result};

use super::ControlService;

#[derive(Debug, Default, Deserialize)]
pub struct ListAccountsQuery {
    #[serde(default)]
    probe: Option<String>,
}

impl ListAccountsQuery {
    fn probe(&self) -> bool {
        matches!(
            self.probe.as_deref().map(str::trim),
            Some("1" | "true" | "yes")
        )
    }
}

pub async fn list(
    State(service): State<ControlService>,
    Query(query): Query<ListAccountsQuery>,
) -> Result<Json<Vec<CursorAccountView>>> {
    Ok(Json(service.cursor_accounts(query.probe()).await?))
}

pub async fn switch_account(
    State(service): State<ControlService>,
    Path(account_id): Path<String>,
) -> Result<Json<Vec<CursorAccountView>>> {
    Ok(Json(service.switch_cursor_account(&account_id).await?))
}

pub async fn remove(
    State(service): State<ControlService>,
    Path(account_id): Path<String>,
) -> Result<StatusCode> {
    service.delete_cursor_account(&account_id).await?;
    Ok(StatusCode::NO_CONTENT)
}
