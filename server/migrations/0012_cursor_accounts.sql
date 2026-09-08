CREATE TABLE cursor_accounts (
    account_id TEXT PRIMARY KEY,
    subject TEXT NOT NULL,
    email TEXT NOT NULL,
    membership_type TEXT,
    subscription_status TEXT,
    sign_up_type TEXT,
    auth_snapshot_json TEXT NOT NULL,
    last_seen_at_ms INTEGER NOT NULL,
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL
);

CREATE UNIQUE INDEX cursor_accounts_subject ON cursor_accounts(subject);
CREATE INDEX cursor_accounts_last_seen ON cursor_accounts(last_seen_at_ms DESC);
