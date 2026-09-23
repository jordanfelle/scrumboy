import { apiFetch } from '../api.js';
import { attachDialogClose, bindDialogLocale, escapeHTML, showConfirmDialog, showToast } from '../utils.js';
import { apiErrorMessageOrRaw, formatDate, t } from '../i18n/index.js';
import { ApiToken, ApiTokenCreateResponse } from '../types.js';

type BindApiTokensInteractionsOptions = {
  signal: AbortSignal;
  rerender: () => Promise<void>;
};

let cachedApiTokens: ApiToken[] | null = null;
let apiTokensLoadErrorMessage: string | null = null;

/** Drop the cached API token list so the Profile tab refetches on next render. */
export function invalidateApiTokensCache(): void {
  cachedApiTokens = null;
  apiTokensLoadErrorMessage = null;
}

const API_TOKEN_DATE_OPTS: Intl.DateTimeFormatOptions = {
  month: "short",
  day: "numeric",
  year: "numeric",
};

function formatApiTokenDate(value: string | undefined): string {
  if (!value) return t("settings.profile.apiTokens.neverUsed");
  return formatDate(value, API_TOKEN_DATE_OPTS);
}

function renderApiTokenStatusBadges(token: ApiToken): string {
  const badges: string[] = [];
  if (token.revokedAt) {
    badges.push(`<span class="status-pill status-pill--revoked" data-i18n-text="settings.profile.apiTokens.badge.revoked">Revoked</span>`);
  } else {
    badges.push(`<span class="status-pill status-pill--active" data-i18n-text="settings.profile.apiTokens.badge.active">Active</span>`);
  }
  if (token.isService) {
    badges.push(`<span class="status-pill status-pill--service" data-i18n-text="settings.profile.apiTokens.badge.service">Service</span>`);
  }
  return badges.join(" ");
}

async function loadApiTokens(): Promise<ApiToken[]> {
  if (cachedApiTokens) return cachedApiTokens;
  const res = await apiFetch<{ items: ApiToken[] } | null>("/api/me/tokens");
  cachedApiTokens = res?.items ?? [];
  return cachedApiTokens;
}

/** Renders the "API Tokens" section of the Profile tab: existing tokens + a create-token form. */
export async function renderApiTokensSectionHTML(): Promise<string> {
  let tokens: ApiToken[] = [];
  try {
    tokens = await loadApiTokens();
    apiTokensLoadErrorMessage = null;
  } catch (err: any) {
    apiTokensLoadErrorMessage = apiErrorMessageOrRaw(err, { fallbackKey: "settings.profile.apiTokens.loadFailed" });
    tokens = [];
  }

  const rowsHTML = tokens
    .map((tok) => {
      const displayName = tok.name
        ? escapeHTML(tok.name)
        : `<span class="muted" data-i18n-text="settings.profile.apiTokens.unnamed">(unnamed)</span>`;
      const actionsHTML = tok.revokedAt
        ? "-"
        : `<button class="btn btn--danger btn--small" data-action="revoke-api-token" data-token-id="${escapeHTML(String(tok.id))}" data-token-name="${escapeHTML(tok.name || "")}" data-i18n-text="settings.profile.apiTokens.actions.revoke">Revoke</button>`;
      return `
        <tr>
          <td>${displayName}</td>
          <td>${escapeHTML(formatApiTokenDate(tok.createdAt))}</td>
          <td>${escapeHTML(formatApiTokenDate(tok.lastUsedAt))}</td>
          <td>${renderApiTokenStatusBadges(tok)}</td>
          <td>${actionsHTML}</td>
        </tr>
      `;
    })
    .join("");

  const listHTML = apiTokensLoadErrorMessage
    ? `<div class="muted" role="alert">${escapeHTML(apiTokensLoadErrorMessage)}</div>`
    : tokens.length === 0
      ? `<div class="muted" data-i18n-text="settings.profile.apiTokens.empty">No API tokens yet.</div>`
      : `
        <table class="api-tokens-table">
          <thead>
            <tr>
              <th data-i18n-text="settings.profile.apiTokens.table.name">Name</th>
              <th data-i18n-text="settings.profile.apiTokens.table.created">Created</th>
              <th data-i18n-text="settings.profile.apiTokens.table.lastUsed">Last used</th>
              <th data-i18n-text="settings.profile.apiTokens.table.status">Status</th>
              <th data-i18n-text="settings.profile.apiTokens.table.actions">Actions</th>
            </tr>
          </thead>
          <tbody>${rowsHTML}</tbody>
        </table>
      `;

  return `
    <div class="settings-section" style="margin-top: 24px;">
      <div class="settings-section__title" data-i18n-text="settings.profile.apiTokens.title">API Tokens</div>
      <div class="settings-section__description muted" data-i18n-text="settings.profile.apiTokens.description">Tokens for authenticating with Scrumboy's API and MCP endpoint outside the browser.</div>
      ${listHTML}
      <form id="createApiTokenForm" class="api-tokens-create">
        <label class="field api-tokens-create__name">
          <div class="field__label" data-i18n-text="settings.profile.apiTokens.create.nameLabel">Name (optional)</div>
          <input type="text" id="createApiTokenName" class="input" data-i18n-placeholder="settings.profile.apiTokens.create.namePlaceholder" placeholder="e.g. CI pipeline" maxlength="200" />
        </label>
        <label class="api-tokens-create__service">
          <input type="checkbox" id="createApiTokenService" />
          <span data-i18n-text="settings.profile.apiTokens.create.serviceLabel">Service token (its record survives if this account is later deleted)</span>
        </label>
        <button type="submit" class="btn" id="createApiTokenSubmit" data-i18n-text="settings.profile.apiTokens.create.submit">Create token</button>
      </form>
    </div>
  `;
}

/** Shows the newly-created API token's secret once, with a copy-to-clipboard action. */
function showApiTokenCreatedDialog(token: string): void {
  const dialog = document.createElement("dialog");
  dialog.className = "dialog";
  dialog.innerHTML = `
    <div class="dialog__form">
      <div class="dialog__header">
        <div class="dialog__title" data-i18n-text="settings.profile.apiTokens.created.title">Token created</div>
        <button class="btn btn--ghost" type="button" id="apiTokenCreatedClose" aria-label="Close" data-i18n-aria-label="common.close">✕</button>
      </div>
      <p class="muted" role="alert" data-i18n-text="settings.profile.apiTokens.created.warning">Copy this token now — it won't be shown again.</p>
      <div class="field" style="margin: 12px 0;">
        <input type="text" id="apiTokenCreatedDisplay" class="input" readonly value="${escapeHTML(token)}" style="font-size: 12px;" />
      </div>
      <div class="dialog__footer">
        <div class="spacer"></div>
        <button type="button" class="btn" id="apiTokenCreatedCopy" data-i18n-text="settings.profile.apiTokens.created.copy">Copy</button>
        <button type="button" class="btn" id="apiTokenCreatedDone" data-i18n-text="settings.profile.apiTokens.created.done">Done</button>
      </div>
    </div>
  `;
  document.body.appendChild(dialog);
  (dialog as HTMLDialogElement).showModal();
  const releaseLocale = bindDialogLocale(dialog);

  const closeBtn = dialog.querySelector<HTMLElement>("#apiTokenCreatedClose");
  const doneBtn = dialog.querySelector<HTMLElement>("#apiTokenCreatedDone");
  const copyBtn = dialog.querySelector<HTMLElement>("#apiTokenCreatedCopy");
  const tokenInput = dialog.querySelector<HTMLInputElement>("#apiTokenCreatedDisplay");

  const close = attachDialogClose(dialog, releaseLocale);

  if (closeBtn) closeBtn.addEventListener("click", close);
  if (doneBtn) doneBtn.addEventListener("click", close);
  dialog.addEventListener("click", (e) => {
    if (e.target === dialog) close();
  });

  if (copyBtn && tokenInput) {
    copyBtn.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(tokenInput.value);
        showToast(t("settings.profile.apiTokens.created.copied"));
      } catch {
        tokenInput.select();
        showToast(t("settings.profile.apiTokens.created.copyManual"));
      }
    });
  }
}

/** Wires up the create-token form and revoke buttons rendered by {@link renderApiTokensSectionHTML}. */
export function bindApiTokensInteractions({ signal, rerender }: BindApiTokensInteractionsOptions): void {
  const createApiTokenForm = document.getElementById("createApiTokenForm") as HTMLFormElement | null;
  if (createApiTokenForm) {
    createApiTokenForm.addEventListener("submit", async (e) => {
      e.preventDefault();
      const nameInput = document.getElementById("createApiTokenName") as HTMLInputElement | null;
      const serviceInput = document.getElementById("createApiTokenService") as HTMLInputElement | null;
      const submitBtn = document.getElementById("createApiTokenSubmit") as HTMLButtonElement | null;
      const name = nameInput?.value.trim() || undefined;
      const isService = !!serviceInput?.checked;

      if (submitBtn) {
        submitBtn.disabled = true;
        submitBtn.textContent = t("settings.profile.apiTokens.create.creating");
      }
      try {
        const created = await apiFetch<ApiTokenCreateResponse>("/api/me/tokens", {
          method: "POST",
          body: JSON.stringify({ name, isService }),
        });
        invalidateApiTokensCache();
        showToast(t("settings.profile.apiTokens.toast.created"));
        await rerender();
        if (created?.token) {
          showApiTokenCreatedDialog(created.token);
        }
      } catch (err: any) {
        showToast(apiErrorMessageOrRaw(err, { fallbackKey: "settings.profile.apiTokens.toast.createFailed" }));
        if (submitBtn) {
          submitBtn.disabled = false;
          submitBtn.textContent = t("settings.profile.apiTokens.create.submit");
        }
      }
    }, { signal });
  }

  document.querySelectorAll('[data-action="revoke-api-token"]').forEach((btn) => {
    btn.addEventListener("click", async (e) => {
      const el = e.currentTarget as HTMLElement;
      const tokenId = el.getAttribute("data-token-id");
      if (!tokenId) return;
      const tokenName = el.getAttribute("data-token-name") || t("settings.profile.apiTokens.unnamed");

      const confirmed = await showConfirmDialog(
        t("settings.profile.apiTokens.revoke.confirmMessage", { name: tokenName }),
        t("settings.profile.apiTokens.revoke.confirmTitle"),
        t("settings.profile.apiTokens.revoke.confirmAction"),
      );
      if (!confirmed) return;

      try {
        await apiFetch(`/api/me/tokens/${encodeURIComponent(tokenId)}`, { method: "DELETE" });
        invalidateApiTokensCache();
        showToast(t("settings.profile.apiTokens.toast.revoked"));
        await rerender();
      } catch (err: any) {
        showToast(apiErrorMessageOrRaw(err, { fallbackKey: "settings.profile.apiTokens.toast.revokeFailed" }));
      }
    }, { signal });
  });
}
