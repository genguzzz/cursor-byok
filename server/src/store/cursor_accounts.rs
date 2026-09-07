//! Persists official Cursor login accounts captured from the local auth desk.

use serde::{Deserialize, Serialize};
use sqlx::Row;

use crate::Result;

use super::{now_ms, Store};

const ACCOUNT_COLUMNS: &str = r#"
    account_id, subject, email, membership_type, subscription_status, sign_up_type,
    auth_snapshot_json, last_seen_at_ms, created_at_ms, updated_at_ms
"#;

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct CursorAccountRecord {
    pub account_id: String,
    pub subject: String,
    pub email: String,
    pub membership_type: Option<String>,
    pub subscription_status: Option<String>,
    pub sign_up_type: Option<String>,
    pub auth_snapshot_json: String,
    pub last_seen_at_ms: i64,
    pub created_at_ms: i64,
    pub updated_at_ms: i64,
}

#[derive(Clone, Debug)]
pub struct CursorAccountUpsert {
    pub subject: String,
    pub email: String,
    pub membership_type: Option<String>,
    pub subscription_status: Option<String>,
    pub sign_up_type: Option<String>,
    pub auth_snapshot_json: String,
}

impl Store {
    pub async fn cursor_accounts(&self) -> Result<Vec<CursorAccountRecord>> {
        let query = format!(
            "SELECT {ACCOUNT_COLUMNS} FROM cursor_accounts ORDER BY last_seen_at_ms DESC, email"
        );
        sqlx::query(&query)
            .fetch_all(&self.pool)
            .await?
            .into_iter()
            .map(account_from_row)
            .collect()
    }

    pub async fn cursor_account(&self, account_id: &str) -> Result<Option<CursorAccountRecord>> {
        let query = format!("SELECT {ACCOUNT_COLUMNS} FROM cursor_accounts WHERE account_id = ?");
        sqlx::query(&query)
            .bind(account_id)
            .fetch_optional(&self.pool)
            .await?
            .map(account_from_row)
            .transpose()
    }

    pub async fn cursor_account_by_subject(
        &self,
        subject: &str,
    ) -> Result<Option<CursorAccountRecord>> {
        let query = format!("SELECT {ACCOUNT_COLUMNS} FROM cursor_accounts WHERE subject = ?");
        sqlx::query(&query)
            .bind(subject)
            .fetch_optional(&self.pool)
            .await?
            .map(account_from_row)
            .transpose()
    }

    pub async fn upsert_cursor_account(
        &self,
        input: CursorAccountUpsert,
    ) -> Result<CursorAccountRecord> {
        let now = now_ms();
        let _write = self.writes.lock().await;
        if let Some(existing) = self.cursor_account_by_subject(&input.subject).await? {
            sqlx::query(
                "UPDATE cursor_accounts SET email = ?, membership_type = ?, subscription_status = ?, sign_up_type = ?, auth_snapshot_json = ?, last_seen_at_ms = ?, updated_at_ms = ? WHERE account_id = ?",
            )
            .bind(&input.email)
            .bind(&input.membership_type)
            .bind(&input.subscription_status)
            .bind(&input.sign_up_type)
            .bind(&input.auth_snapshot_json)
            .bind(now)
            .bind(now)
            .bind(&existing.account_id)
            .execute(&self.pool)
            .await?;
            return Ok(self
                .cursor_account(&existing.account_id)
                .await?
                .expect("updated cursor account must exist"));
        }

        let account_id = uuid::Uuid::new_v4().to_string();
        sqlx::query(
            "INSERT INTO cursor_accounts(account_id, subject, email, membership_type, subscription_status, sign_up_type, auth_snapshot_json, last_seen_at_ms, created_at_ms, updated_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
        )
        .bind(&account_id)
        .bind(&input.subject)
        .bind(&input.email)
        .bind(&input.membership_type)
        .bind(&input.subscription_status)
        .bind(&input.sign_up_type)
        .bind(&input.auth_snapshot_json)
        .bind(now)
        .bind(now)
        .bind(now)
        .execute(&self.pool)
        .await?;
        Ok(self
            .cursor_account(&account_id)
            .await?
            .expect("inserted cursor account must exist"))
    }

    pub async fn delete_cursor_account(&self, account_id: &str) -> Result<bool> {
        let _write = self.writes.lock().await;
        let result = sqlx::query("DELETE FROM cursor_accounts WHERE account_id = ?")
            .bind(account_id)
            .execute(&self.pool)
            .await?;
        Ok(result.rows_affected() > 0)
    }
}

fn account_from_row(row: sqlx::sqlite::SqliteRow) -> Result<CursorAccountRecord> {
    Ok(CursorAccountRecord {
        account_id: row.try_get("account_id")?,
        subject: row.try_get("subject")?,
        email: row.try_get("email")?,
        membership_type: row.try_get("membership_type")?,
        subscription_status: row.try_get("subscription_status")?,
        sign_up_type: row.try_get("sign_up_type")?,
        auth_snapshot_json: row.try_get("auth_snapshot_json")?,
        last_seen_at_ms: row.try_get("last_seen_at_ms")?,
        created_at_ms: row.try_get("created_at_ms")?,
        updated_at_ms: row.try_get("updated_at_ms")?,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn upsert(subject: &str, email: &str, token: &str) -> CursorAccountUpsert {
        CursorAccountUpsert {
            subject: subject.into(),
            email: email.into(),
            membership_type: Some("pro".into()),
            subscription_status: Some("active".into()),
            sign_up_type: Some("Google".into()),
            auth_snapshot_json: serde_json::json!({
                "cursorAuth/accessToken": token,
                "cursorAuth/cachedEmail": email
            })
            .to_string(),
        }
    }

    #[tokio::test]
    async fn upserts_by_subject_and_preserves_account_id() {
        let directory = tempfile::tempdir().unwrap();
        let store = Store::connect(&format!(
            "sqlite://{}",
            directory.path().join("test.db").display()
        ))
        .await
        .unwrap();

        let first = store
            .upsert_cursor_account(upsert("user-1", "one@example.com", "token-1"))
            .await
            .unwrap();
        let second = store
            .upsert_cursor_account(upsert("user-1", "one@example.com", "token-2"))
            .await
            .unwrap();

        assert_eq!(second.account_id, first.account_id);
        assert!(second.auth_snapshot_json.contains("token-2"));
        assert_eq!(store.cursor_accounts().await.unwrap().len(), 1);
    }

    #[tokio::test]
    async fn deletes_a_stored_account() {
        let directory = tempfile::tempdir().unwrap();
        let store = Store::connect(&format!(
            "sqlite://{}",
            directory.path().join("test.db").display()
        ))
        .await
        .unwrap();
        let saved = store
            .upsert_cursor_account(upsert("user-2", "two@example.com", "token"))
            .await
            .unwrap();

        assert!(store
            .delete_cursor_account(&saved.account_id)
            .await
            .unwrap());
        assert!(store
            .cursor_account(&saved.account_id)
            .await
            .unwrap()
            .is_none());
    }
}
