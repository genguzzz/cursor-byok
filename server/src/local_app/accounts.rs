//! Captures, validates, switches, and deletes stored Cursor logins.
use std::time::Duration;

use serde::Serialize;

use crate::{
    store::{now_ms, CursorAccountRecord, CursorAccountUpsert, Store},
    Error, Result,
};

use super::account::{
    identity_from_snapshot, is_local_injected, jwt_expired, local_identity, local_snapshot,
    read_snapshot, read_snapshot_at, state_db_path, write_snapshot_at, AuthSnapshot,
};

const AUTH_PROBE_URL: &str = "https://api2.cursor.sh/auth/full_stripe_profile";
const AUTH_PROBE_TIMEOUT: Duration = Duration::from_secs(8);

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum AccountValidity {
    Valid,
    Expired,
    Invalid,
    Unknown,
}

#[derive(Clone, Debug, Serialize)]
pub struct CursorAccountView {
    pub id: String,
    pub email: String,
    pub membership_type: Option<String>,
    pub subscription_status: Option<String>,
    pub sign_up_type: Option<String>,
    pub current: bool,
    pub temporary: bool,
    pub valid: bool,
    pub validity: AccountValidity,
    pub last_seen_at_ms: i64,
}

pub async fn capture_current(store: &Store) -> Result<Option<CursorAccountRecord>> {
    capture_snapshot(store, read_snapshot().await?).await
}

async fn capture_snapshot(
    store: &Store,
    snapshot: Option<AuthSnapshot>,
) -> Result<Option<CursorAccountRecord>> {
    let Some(snapshot) = snapshot else {
        return Ok(None);
    };
    let Some(identity) = identity_from_snapshot(&snapshot) else {
        return Ok(None);
    };
    Ok(Some(
        store
            .upsert_cursor_account(CursorAccountUpsert {
                subject: identity.subject,
                email: identity.email,
                membership_type: snapshot.membership_type(),
                subscription_status: snapshot.subscription_status(),
                sign_up_type: snapshot.sign_up_type(),
                auth_snapshot_json: snapshot.to_json()?,
            })
            .await?,
    ))
}

pub async fn list_accounts(store: &Store, probe: bool) -> Result<Vec<CursorAccountView>> {
    if let Err(error) = capture_current(store).await {
        tracing::debug!(%error, "cursor account capture skipped");
    }
    if let Err(error) = ensure_local_account(store).await {
        tracing::debug!(%error, "cursor temporary account ensure skipped");
    }
    list_accounts_from(store, read_snapshot().await.ok().flatten(), probe).await
}

async fn ensure_local_account(store: &Store) -> Result<()> {
    let identity = local_identity();
    if store
        .cursor_account_by_subject(&identity.subject)
        .await?
        .is_some()
    {
        return Ok(());
    }
    let snapshot = local_snapshot()?;
    store
        .upsert_cursor_account(CursorAccountUpsert {
            subject: identity.subject,
            email: identity.email,
            membership_type: snapshot.membership_type(),
            subscription_status: snapshot.subscription_status(),
            sign_up_type: snapshot.sign_up_type(),
            auth_snapshot_json: snapshot.to_json()?,
        })
        .await?;
    Ok(())
}

async fn list_accounts_from(
    store: &Store,
    live: Option<AuthSnapshot>,
    probe: bool,
) -> Result<Vec<CursorAccountView>> {
    let current = live
        .as_ref()
        .and_then(identity_from_snapshot)
        .map(|identity| identity.subject);
    let records = store.cursor_accounts().await?;
    let now = now_ms();
    let mut views = Vec::with_capacity(records.len());
    for record in records {
        views.push(account_view(store, record, current.as_deref(), now, probe).await);
    }
    Ok(views)
}

pub async fn switch_account(store: &Store, account_id: &str) -> Result<Vec<CursorAccountView>> {
    switch_account_at(store, account_id, &state_db_path()?).await
}

async fn switch_account_at(
    store: &Store,
    account_id: &str,
    path: &std::path::Path,
) -> Result<Vec<CursorAccountView>> {
    if let Err(error) = capture_snapshot(store, read_snapshot_at(path).await?).await {
        tracing::debug!(%error, "cursor account capture before switch skipped");
    }
    let record = store
        .cursor_account(account_id)
        .await?
        .ok_or_else(|| Error::Config("cursor account not found".into()))?;
    let snapshot = AuthSnapshot::from_json(&record.auth_snapshot_json)?;
    if snapshot.access_token().is_none() {
        return Err(Error::Config(
            "cursor account is missing its access token".into(),
        ));
    }
    write_snapshot_at(path, &snapshot).await?;
    store
        .upsert_cursor_account(CursorAccountUpsert {
            subject: record.subject,
            email: record.email,
            membership_type: record.membership_type,
            subscription_status: record.subscription_status,
            sign_up_type: record.sign_up_type,
            auth_snapshot_json: record.auth_snapshot_json,
        })
        .await?;
    list_accounts_from(store, read_snapshot_at(path).await.ok().flatten(), false).await
}

pub async fn delete_account(store: &Store, account_id: &str) -> Result<Vec<CursorAccountView>> {
    delete_account_from(store, account_id, read_snapshot().await.ok().flatten()).await
}

async fn delete_account_from(
    store: &Store,
    account_id: &str,
    live: Option<AuthSnapshot>,
) -> Result<Vec<CursorAccountView>> {
    let current = live
        .as_ref()
        .and_then(identity_from_snapshot)
        .map(|identity| identity.subject);
    let record = store
        .cursor_account(account_id)
        .await?
        .ok_or_else(|| Error::Config("cursor account not found".into()))?;
    if record.subject == local_identity().subject {
        return Err(Error::Config(
            "the temporary Cursor account cannot be deleted".into(),
        ));
    }
    if current.as_deref() == Some(record.subject.as_str()) {
        return Err(Error::Config(
            "switch to another account before deleting the current one".into(),
        ));
    }
    if !store.delete_cursor_account(account_id).await? {
        return Err(Error::Config("cursor account not found".into()));
    }
    list_accounts_from(store, live, false).await
}

async fn account_view(
    store: &Store,
    record: CursorAccountRecord,
    current: Option<&str>,
    now_ms: i64,
    probe: bool,
) -> CursorAccountView {
    let snapshot = AuthSnapshot::from_json(&record.auth_snapshot_json).ok();
    let identity = snapshot.as_ref().and_then(identity_from_snapshot);
    let token = snapshot.as_ref().and_then(AuthSnapshot::access_token);
    let temporary = identity.as_ref().is_some_and(is_local_injected)
        || record.subject == local_identity().subject;
    let expired = jwt_expired(
        identity
            .as_ref()
            .and_then(|identity| identity.expires_at_ms),
        now_ms,
    );
    let validity = if token.is_none() {
        AccountValidity::Invalid
    } else if expired {
        AccountValidity::Expired
    } else if temporary {
        AccountValidity::Valid
    } else if probe {
        match probe_login(store, token.unwrap_or_default()).await {
            Ok(true) => AccountValidity::Valid,
            Ok(false) => AccountValidity::Invalid,
            Err(error) => {
                tracing::debug!(%error, "cursor account validity probe failed");
                AccountValidity::Unknown
            }
        }
    } else {
        AccountValidity::Unknown
    };
    CursorAccountView {
        id: record.account_id,
        email: record.email,
        membership_type: record.membership_type,
        subscription_status: record.subscription_status,
        sign_up_type: record.sign_up_type,
        current: current == Some(record.subject.as_str()),
        temporary,
        valid: validity == AccountValidity::Valid,
        validity,
        last_seen_at_ms: record.last_seen_at_ms,
    }
}

async fn probe_login(store: &Store, token: &str) -> Result<bool> {
    let client = crate::network::client_builder(store)
        .await?
        .timeout(AUTH_PROBE_TIMEOUT)
        .build()?;
    let response = client
        .get(AUTH_PROBE_URL)
        .header(reqwest::header::AUTHORIZATION, format!("Bearer {token}"))
        .header(
            reqwest::header::COOKIE,
            format!("WorkosCursorSessionToken={token}"),
        )
        .send()
        .await?;
    let status = response.status();
    if status.is_success() {
        return Ok(true);
    }
    if status.as_u16() == 401 || status.as_u16() == 403 {
        return Ok(false);
    }
    Err(Error::Provider(format!(
        "cursor auth probe returned HTTP {}",
        status.as_u16()
    )))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::local_app::account::{
        identity_from_snapshot, is_local_injected, jwt_expired, read_snapshot_at, test_token,
        write_snapshot_at, AccountIdentity, AuthSnapshot,
    };
    use std::collections::BTreeMap;

    fn snapshot(sub: &str, email: &str) -> AuthSnapshot {
        AuthSnapshot {
            values: BTreeMap::from([
                (
                    "cursorAuth/accessToken".into(),
                    test_token(sub, email, 4070908800),
                ),
                ("cursorAuth/cachedEmail".into(), email.into()),
                ("cursorAuth/stripeMembershipType".into(), "pro".into()),
            ]),
        }
    }

    async fn store() -> (tempfile::TempDir, Store) {
        let directory = tempfile::tempdir().unwrap();
        let store = Store::connect(&format!(
            "sqlite://{}",
            directory.path().join("byok.db").display()
        ))
        .await
        .unwrap();
        (directory, store)
    }

    #[test]
    fn jwt_expiry_and_local_inject_are_classified() {
        assert!(jwt_expired(Some(1), 2));
        assert!(!jwt_expired(Some(3), 2));
        assert!(!jwt_expired(None, 2));

        let identity = AccountIdentity {
            subject: "cursor-local-user".into(),
            email: "cursor@ai.com".into(),
            expires_at_ms: None,
        };
        assert!(is_local_injected(&identity));
    }

    #[tokio::test]
    async fn captures_official_and_temporary_logins() {
        let (_directory, store) = store().await;
        let local = capture_snapshot(&store, Some(snapshot("cursor-local-user", "cursor@ai.com")))
            .await
            .unwrap()
            .unwrap();
        assert_eq!(local.email, "cursor@ai.com");

        let saved = capture_snapshot(&store, Some(snapshot("user_one", "one@example.com")))
            .await
            .unwrap()
            .unwrap();
        assert_eq!(saved.email, "one@example.com");
        assert_eq!(store.cursor_accounts().await.unwrap().len(), 2);
    }

    #[tokio::test]
    async fn list_ensures_the_default_temporary_account() {
        let (_directory, store) = store().await;
        let views = list_accounts_from(&store, None, false).await.unwrap();
        assert!(views.is_empty());
        ensure_local_account(&store).await.unwrap();
        let views = list_accounts_from(&store, None, false).await.unwrap();
        assert_eq!(views.len(), 1);
        assert!(views[0].temporary);
        assert_eq!(views[0].email, "cursor@ai.com");
    }

    #[tokio::test]
    async fn switch_writes_stored_snapshot_and_keeps_the_previous_account() {
        let (directory, store) = store().await;
        let path = directory.path().join("state.vscdb");
        write_snapshot_at(&path, &snapshot("user_one", "one@example.com"))
            .await
            .unwrap();
        capture_snapshot(&store, read_snapshot_at(&path).await.unwrap())
            .await
            .unwrap();

        let two = capture_snapshot(&store, Some(snapshot("user_two", "two@example.com")))
            .await
            .unwrap()
            .unwrap();
        let views = switch_account_at(&store, &two.account_id, &path)
            .await
            .unwrap();
        let live = read_snapshot_at(&path).await.unwrap().unwrap();
        let identity = identity_from_snapshot(&live).unwrap();
        assert_eq!(identity.email, "two@example.com");
        assert_eq!(views.len(), 2);
        assert!(views
            .iter()
            .any(|account| account.current && account.email == "two@example.com"));
    }

    #[tokio::test]
    async fn delete_rejects_the_current_and_temporary_accounts() {
        let (_directory, store) = store().await;
        let live = snapshot("user_one", "one@example.com");
        let saved = capture_snapshot(&store, Some(live.clone()))
            .await
            .unwrap()
            .unwrap();
        let error = delete_account_from(&store, &saved.account_id, Some(live))
            .await
            .unwrap_err();
        assert!(error.to_string().contains("switch to another account"));

        ensure_local_account(&store).await.unwrap();
        let temporary = store
            .cursor_account_by_subject("cursor-local-user")
            .await
            .unwrap()
            .unwrap();
        let error = delete_account_from(&store, &temporary.account_id, None)
            .await
            .unwrap_err();
        assert!(error.to_string().contains("temporary"));
    }
}
