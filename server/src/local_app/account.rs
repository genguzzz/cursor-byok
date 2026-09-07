//! Reads and writes the local Cursor auth desk in `state.vscdb`.
use std::{
    collections::BTreeMap,
    path::{Path, PathBuf},
    time::Duration,
};

use base64::{engine::general_purpose::URL_SAFE_NO_PAD, Engine};
use serde_json::{json, Value};
use sqlx::{Connection, Row, SqliteConnection};

use crate::{Error, Result};

const EMAIL: &str = "cursor@ai.com";
const SIGN_UP_TYPE: &str = "Google";
const SUBJECT: &str = "cursor-local-user";
const MEMBERSHIP_TYPE: &str = "ultra";
const SUBSCRIPTION_STATUS: &str = "active";
const AUTH_KEY_PREFIX: &str = "cursorAuth/";
const ACCESS_TOKEN_KEY: &str = "cursorAuth/accessToken";
const REFRESH_TOKEN_KEY: &str = "cursorAuth/refreshToken";
const EMAIL_KEY: &str = "cursorAuth/cachedEmail";
const SIGN_UP_TYPE_KEY: &str = "cursorAuth/cachedSignUpType";
const MEMBERSHIP_KEY: &str = "cursorAuth/stripeMembershipType";
const SUBSCRIPTION_KEY: &str = "cursorAuth/stripeSubscriptionStatus";

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct AuthSnapshot {
    pub values: BTreeMap<String, String>,
}

impl AuthSnapshot {
    pub fn access_token(&self) -> Option<&str> {
        non_empty(self.values.get(ACCESS_TOKEN_KEY).map(String::as_str))
    }

    pub fn email(&self) -> Option<&str> {
        non_empty(self.values.get(EMAIL_KEY).map(String::as_str))
    }

    pub fn membership_type(&self) -> Option<String> {
        optional_text(self.values.get(MEMBERSHIP_KEY))
    }

    pub fn subscription_status(&self) -> Option<String> {
        optional_text(self.values.get(SUBSCRIPTION_KEY))
    }

    pub fn sign_up_type(&self) -> Option<String> {
        optional_text(self.values.get(SIGN_UP_TYPE_KEY))
    }

    pub fn to_json(&self) -> Result<String> {
        Ok(serde_json::to_string(&self.values)?)
    }

    pub fn from_json(value: &str) -> Result<Self> {
        Ok(Self {
            values: serde_json::from_str(value)?,
        })
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AccountIdentity {
    pub subject: String,
    pub email: String,
    pub expires_at_ms: Option<i64>,
}

pub async fn inject_if_missing() -> Result<()> {
    inject_if_missing_at(&state_db_path()?).await
}

pub fn state_db_path() -> Result<PathBuf> {
    let home = dirs::home_dir()
        .ok_or_else(|| Error::Config("cannot resolve user home directory".into()))?;
    match std::env::consts::OS {
        "macos" => {
            Ok(home.join("Library/Application Support/Cursor/User/globalStorage/state.vscdb"))
        }
        "windows" => Ok(std::env::var_os("APPDATA")
            .map(PathBuf::from)
            .unwrap_or_else(|| home.join("AppData/Roaming"))
            .join("Cursor/User/globalStorage/state.vscdb")),
        "linux" => Ok(std::env::var_os("XDG_CONFIG_HOME")
            .map(PathBuf::from)
            .unwrap_or_else(|| home.join(".config"))
            .join("Cursor/User/globalStorage/state.vscdb")),
        platform => Err(Error::Config(format!(
            "Cursor account injection is unsupported on {platform}"
        ))),
    }
}

pub async fn read_snapshot() -> Result<Option<AuthSnapshot>> {
    read_snapshot_at(&state_db_path()?).await
}

pub async fn read_snapshot_at(path: &Path) -> Result<Option<AuthSnapshot>> {
    if !path.exists() {
        return Ok(None);
    }
    let mut connection = connect(path, false).await?;
    let rows =
        sqlx::query("SELECT key, CAST(value AS TEXT) AS value FROM ItemTable WHERE key LIKE ?")
            .bind(format!("{AUTH_KEY_PREFIX}%"))
            .fetch_all(&mut connection)
            .await?;
    let mut values = BTreeMap::new();
    for row in rows {
        let key = row.try_get::<String, _>("key")?;
        let value = row
            .try_get::<Option<String>, _>("value")?
            .unwrap_or_default();
        if value.trim().is_empty() {
            continue;
        }
        values.insert(key, value);
    }
    if values.is_empty() {
        return Ok(None);
    }
    Ok(Some(AuthSnapshot { values }))
}

pub async fn write_snapshot_at(path: &Path, snapshot: &AuthSnapshot) -> Result<()> {
    if let Some(parent) = path.parent() {
        tokio::fs::create_dir_all(parent).await?;
    }
    let mut connection = connect(path, true).await?;
    ensure_item_table(&mut connection).await?;
    let mut transaction = connection.begin().await?;
    sqlx::query("DELETE FROM ItemTable WHERE key LIKE ?")
        .bind(format!("{AUTH_KEY_PREFIX}%"))
        .execute(&mut *transaction)
        .await?;
    for (key, value) in &snapshot.values {
        if !key.starts_with(AUTH_KEY_PREFIX) || value.trim().is_empty() {
            continue;
        }
        sqlx::query("INSERT OR REPLACE INTO ItemTable(key, value) VALUES(?, ?)")
            .bind(key)
            .bind(value)
            .execute(&mut *transaction)
            .await?;
    }
    transaction.commit().await?;
    Ok(())
}

pub fn identity_from_snapshot(snapshot: &AuthSnapshot) -> Option<AccountIdentity> {
    let token = snapshot.access_token()?;
    let payload = decode_jwt_payload(token);
    let email = payload
        .as_ref()
        .and_then(|value| text_field(value, "email"))
        .or_else(|| snapshot.email().map(str::to_owned))
        .filter(|email| !email.trim().is_empty())?;
    let subject = payload
        .as_ref()
        .and_then(|value| text_field(value, "sub"))
        .filter(|subject| !subject.trim().is_empty())
        .unwrap_or_else(|| format!("email:{email}"));
    Some(AccountIdentity {
        subject,
        email,
        expires_at_ms: payload.as_ref().and_then(jwt_expiry_ms),
    })
}

pub fn is_local_injected(identity: &AccountIdentity) -> bool {
    identity.subject == SUBJECT || identity.email.eq_ignore_ascii_case(EMAIL)
}

pub fn local_identity() -> AccountIdentity {
    AccountIdentity {
        subject: SUBJECT.into(),
        email: EMAIL.into(),
        expires_at_ms: Some(4_070_908_800_000),
    }
}

pub fn local_snapshot() -> Result<AuthSnapshot> {
    let token = local_token()?;
    Ok(AuthSnapshot {
        values: BTreeMap::from([
            (ACCESS_TOKEN_KEY.into(), token.clone()),
            (REFRESH_TOKEN_KEY.into(), token),
            (EMAIL_KEY.into(), EMAIL.into()),
            (SIGN_UP_TYPE_KEY.into(), SIGN_UP_TYPE.into()),
            (MEMBERSHIP_KEY.into(), MEMBERSHIP_TYPE.into()),
            (SUBSCRIPTION_KEY.into(), SUBSCRIPTION_STATUS.into()),
        ]),
    })
}

pub fn jwt_expired(expires_at_ms: Option<i64>, now_ms: i64) -> bool {
    expires_at_ms.is_some_and(|expires| expires <= now_ms)
}

async fn inject_if_missing_at(path: &Path) -> Result<()> {
    if read_snapshot_at(path)
        .await?
        .is_some_and(|snapshot| snapshot.access_token().is_some())
    {
        return Ok(());
    }
    write_snapshot_at(path, &local_snapshot()?).await?;
    tracing::info!(
        email = EMAIL,
        subject = SUBJECT,
        "injected local Cursor account"
    );
    Ok(())
}

async fn connect(path: &Path, create: bool) -> Result<SqliteConnection> {
    let options = sqlx::sqlite::SqliteConnectOptions::new()
        .filename(path)
        .create_if_missing(create)
        .busy_timeout(Duration::from_secs(5));
    Ok(SqliteConnection::connect_with(&options).await?)
}

async fn ensure_item_table(connection: &mut SqliteConnection) -> Result<()> {
    sqlx::query(
        "CREATE TABLE IF NOT EXISTS ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)",
    )
    .execute(&mut *connection)
    .await?;
    Ok(())
}

fn local_token() -> Result<String> {
    encode_jwt(
        json!({
            "sub": SUBJECT,
            "email": EMAIL,
            "type": "session",
            "iss": "cursor-client",
            "scope": "openid profile email",
            "exp": 4070908800_u64
        }),
        SUBJECT,
    )
}

fn encode_jwt(payload: Value, signature: &str) -> Result<String> {
    let header = URL_SAFE_NO_PAD.encode(br#"{"alg":"HS256","typ":"JWT"}"#);
    let payload = URL_SAFE_NO_PAD.encode(serde_json::to_vec(&payload)?);
    Ok(format!("{header}.{payload}.{signature}"))
}

fn decode_jwt_payload(token: &str) -> Option<Value> {
    let jwt = token.rsplit("::").next().filter(|part| !part.is_empty())?;
    let payload = jwt.split('.').nth(1)?;
    let decoded = URL_SAFE_NO_PAD.decode(payload).ok()?;
    serde_json::from_slice(&decoded).ok()
}

fn jwt_expiry_ms(payload: &Value) -> Option<i64> {
    payload
        .get("exp")
        .and_then(Value::as_i64)
        .or_else(|| {
            payload
                .get("exp")
                .and_then(Value::as_u64)
                .and_then(|value| i64::try_from(value).ok())
        })
        .map(|seconds| seconds.saturating_mul(1000))
}

fn text_field(value: &Value, key: &str) -> Option<String> {
    value
        .get(key)
        .and_then(Value::as_str)
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(str::to_owned)
}

fn optional_text(value: Option<&String>) -> Option<String> {
    value
        .map(|value| value.trim())
        .filter(|value| !value.is_empty())
        .map(str::to_owned)
}

fn non_empty(value: Option<&str>) -> Option<&str> {
    value.map(str::trim).filter(|value| !value.is_empty())
}

#[cfg(test)]
pub(crate) fn test_token(sub: &str, email: &str, exp: u64) -> String {
    encode_jwt(json!({ "sub": sub, "email": email, "exp": exp }), "sig").unwrap()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn token(sub: &str, email: &str, exp: u64) -> String {
        test_token(sub, email, exp)
    }

    #[test]
    fn identity_prefers_jwt_subject_and_skips_local_inject() {
        let official = AuthSnapshot {
            values: BTreeMap::from([
                (
                    ACCESS_TOKEN_KEY.into(),
                    token("user_abc", "dev@example.com", 4070908800),
                ),
                (EMAIL_KEY.into(), "cached@example.com".into()),
            ]),
        };
        let identity = identity_from_snapshot(&official).unwrap();
        assert_eq!(identity.subject, "user_abc");
        assert_eq!(identity.email, "dev@example.com");
        assert!(!is_local_injected(&identity));

        let local = AuthSnapshot {
            values: BTreeMap::from([(ACCESS_TOKEN_KEY.into(), token(SUBJECT, EMAIL, 4070908800))]),
        };
        assert!(is_local_injected(&identity_from_snapshot(&local).unwrap()));
    }

    #[tokio::test]
    async fn snapshot_round_trips_cursor_auth_keys() {
        let directory = tempfile::tempdir().unwrap();
        let path = directory.path().join("state.vscdb");
        let snapshot = AuthSnapshot {
            values: BTreeMap::from([
                (ACCESS_TOKEN_KEY.into(), "access-secret".into()),
                (EMAIL_KEY.into(), "dev@example.com".into()),
                (MEMBERSHIP_KEY.into(), "pro".into()),
            ]),
        };

        write_snapshot_at(&path, &snapshot).await.unwrap();
        let read = read_snapshot_at(&path).await.unwrap().unwrap();
        assert_eq!(read, snapshot);
    }

    #[tokio::test]
    async fn inject_skips_when_already_signed_in() {
        let directory = tempfile::tempdir().unwrap();
        let path = directory.path().join("state.vscdb");
        let official = AuthSnapshot {
            values: BTreeMap::from([
                (
                    ACCESS_TOKEN_KEY.into(),
                    token("user_abc", "dev@example.com", 4070908800),
                ),
                (EMAIL_KEY.into(), "dev@example.com".into()),
            ]),
        };
        write_snapshot_at(&path, &official).await.unwrap();
        inject_if_missing_at(&path).await.unwrap();
        let read = read_snapshot_at(&path).await.unwrap().unwrap();
        assert_eq!(
            identity_from_snapshot(&read).unwrap().email,
            "dev@example.com"
        );
    }
}
