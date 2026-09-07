import { useEffect, useState } from "react";
import { useLocation } from "react-router-dom";
import { type CursorAccount, type CursorAccountValidity } from "../../shared/api";
import { appStore, useAppStore } from "../../shared/store/appStore";
import { Card } from "../../shared/ui/Card";
import { ConfirmDialog } from "../../shared/ui/ConfirmDialog";
import { TruncatedButton } from "../../shared/ui/TruncatedButton";
import { useMessage } from "../../shared/ui/message";
import { PageContent } from "../../shell/layout/PageContent";
import styles from "./CursorAccountsPage.module.scss";

export function CursorAccountsPage() {
  const { cursorAccounts, busy } = useAppStore();
  const location = useLocation();
  const message = useMessage();
  const [switching, setSwitching] = useState<CursorAccount | null>(null);
  const [deleting, setDeleting] = useState<CursorAccount | null>(null);
  const [acting, setActing] = useState(false);

  useEffect(() => {
    if (location.pathname !== "/accounts" || busy) return;
    void appStore.refreshCursorAccounts(true);
  }, [busy, location.pathname]);

  const switchAccount = async () => {
    if (!switching || acting) return;
    setActing(true);
    try {
      await appStore.switchCursorAccount(switching.id);
      message(t("已切换到 {email}", { email: switching.email }));
      setSwitching(null);
      void appStore.refreshCursorAccounts(true);
    } catch (cause) {
      message(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setActing(false);
    }
  };

  const deleteAccount = async () => {
    if (!deleting || acting) return;
    setActing(true);
    try {
      await appStore.deleteCursorAccount(deleting.id);
      message(t("已删除 {email}", { email: deleting.email }));
      setDeleting(null);
    } catch (cause) {
      message(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setActing(false);
    }
  };

  const content = cursorAccounts.length === 0
    ? <div className={styles.empty}>
      <strong>{t("还没有保存的 Cursor 账号")}</strong>
      <span>{t("打开此页会读取当前登录。已登录时不会注入临时账号，但可以切换到它。")}</span>
    </div>
    : <div className={styles.grid}>
      {cursorAccounts.map((account) => <AccountCard
        key={account.id}
        account={account}
        busy={busy || acting}
        onSwitch={() => setSwitching(account)}
        onDelete={() => setDeleting(account)}
      />)}
    </div>;

  return <>
    <PageContent
      title={t("账号")}
      sections={[{ key: "cursor-accounts", estimatedHeight: Math.max(320, Math.ceil(Math.max(cursorAccounts.length, 1) / 3) * 180), content }]}
    />
    <ConfirmDialog
      id="switch-cursor-account"
      open={switching !== null}
      title={t("切换账号")}
      cancelLabel={t("取消")}
      confirmLabel={t("切换")}
      busy={acting}
      onCancel={() => setSwitching(null)}
      onConfirm={() => void switchAccount()}
    >
      <p>{t("切换到 {email}？若 Cursor 未立即生效，重新打开 Cursor。", { email: switching?.email ?? "" })}</p>
    </ConfirmDialog>
    <ConfirmDialog
      id="delete-cursor-account"
      open={deleting !== null}
      title={t("删除账号")}
      cancelLabel={t("取消")}
      confirmLabel={t("删除")}
      busy={acting}
      onCancel={() => setDeleting(null)}
      onConfirm={() => void deleteAccount()}
    >
      <p>{t("从账号库删除 {email}？不会退出 Cursor 当前登录。", { email: deleting?.email ?? "" })}</p>
    </ConfirmDialog>
  </>;
}

function AccountCard({ account, busy, onSwitch, onDelete }: {
  account: CursorAccount;
  busy: boolean;
  onSwitch: () => void;
  onDelete: () => void;
}) {
  return <Card className={styles.card}>
    <div className={styles.top}>
      <div className={styles.identity}>
        <span className={styles.email}>{account.email}</span>
        <span className={styles.meta}>{accountMeta(account)}</span>
      </div>
      <span className={`${styles.badge} ${validityClass(account.validity)}`}>
        {validityLabel(account.validity)}
      </span>
    </div>
    <div className={styles.flags}>
      {account.current && <span className={`${styles.badge} ${styles.current}`}>{t("当前")}</span>}
      {account.temporary && <span className={styles.badge}>{t("临时")}</span>}
    </div>
    <div className={styles.actions}>
      <TruncatedButton
        size="small"
        variant="primary"
        label={t("切换")}
        disabled={busy || account.current}
        onClick={onSwitch}
      />
      {!account.temporary && <TruncatedButton
        size="small"
        label={t("删除")}
        disabled={busy || account.current}
        onClick={onDelete}
      />}
    </div>
  </Card>;
}

function accountMeta(account: CursorAccount) {
  return [account.membership_type, account.subscription_status, account.sign_up_type]
    .filter((value): value is string => Boolean(value && value.trim()))
    .join(" · ");
}

function validityLabel(validity: CursorAccountValidity) {
  switch (validity) {
    case "valid": return t("有效");
    case "expired": return t("已过期");
    case "invalid": return t("无效");
    case "unknown": return t("未检测");
  }
}

function validityClass(validity: CursorAccountValidity) {
  switch (validity) {
    case "valid": return styles.valid;
    case "expired": return styles.expired;
    case "invalid": return styles.invalid;
    case "unknown": return styles.unknown;
  }
}
