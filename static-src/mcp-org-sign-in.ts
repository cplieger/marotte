// The organization sign-in card at the top of the MCP tab: the identity provider every server set to an
// organization Sign-in method signs in through. kiro-cli runs the sign-in and keeps the tokens; the card saves the
// provider (internal/mcp), starts and cancels the sign-in (internal/agent/mcp_org_sign_in.go) and shows its state.

import { el, effect } from "@cplieger/reactive";

import { setControlBusy } from "./dom.js";
import { ICON_EDIT_UI } from "./icons.js";
import { iconEl } from "./icon-el.js";
import {
  type IdentityProviderConfig,
  type IdentityProviderRead,
  identityProvider,
  mcpState,
  orgProviderName,
  orgSignInStatus,
} from "./mcp-state.js";
import {
  orgSignIn,
  orgSignOut,
  removeIdentityProvider,
  saveIdentityProvider,
  validationFieldsOf,
} from "./actions/mcp.js";
import type { MCPIdentityProviderStatus } from "./wire/types.gen.js";

/** The card's element id, which the server form's Sign-in method hint links to. */
export const ORG_CARD_ID = "mcp-org-sign-in";

/** What the card shows, one per state. Exported for its test. */
export type OrgView =
  | { kind: "loading" }
  | { kind: "not_set_up" }
  | { kind: "signed_out" }
  | { kind: "signing_in"; deadline: Date | undefined; url: string; relayed: boolean }
  | { kind: "signed_in"; account: string }
  | { kind: "expired" }
  | { kind: "invalid"; message: string }
  | { kind: "failed"; message: string };

/** The card's state from the saved provider and the runtime status: a sign-in marotte started outranks a refusal,
 *  which outranks kiro-cli's last report. Exported for its test. */
export function orgView(cfg: IdentityProviderRead, st: MCPIdentityProviderStatus | undefined): OrgView {
  if (!cfg.loaded) {
    return { kind: "loading" };
  }
  if (cfg.provider === null) {
    return { kind: "not_set_up" };
  }
  const reported = st?.reported;
  if (st?.sign_in_deadline !== undefined || reported?.state === "signing-in") {
    const at = st?.sign_in_deadline === undefined ? Number.NaN : Date.parse(st.sign_in_deadline);
    return {
      kind: "signing_in",
      deadline: Number.isNaN(at) ? undefined : new Date(at),
      url: st?.sign_in_url ?? "",
      relayed: st?.relayed ?? false,
    };
  }
  if (st?.error !== undefined && st.error !== "") {
    return { kind: "failed", message: st.error };
  }
  switch (reported?.state) {
    case "signed-in":
      return { kind: "signed_in", account: reported.account ?? "" };
    case "session-expired":
      return { kind: "expired" };
    case "invalid-config":
      return { kind: "invalid", message: reported.error_message ?? "" };
    default:
      return { kind: "signed_out" };
  }
}

/** The state line, which is also the dot's accessible name. Exported for its test. */
export function orgStateText(v: OrgView, provider: string): string {
  switch (v.kind) {
    case "loading":
      return "Reading the organization sign-in";
    case "not_set_up":
      return "Not set up. Enter your organization's identity provider to sign in once for every server that uses it.";
    case "signed_out":
      return `Signed out of ${provider}`;
    case "signing_in":
      return `Signing in to ${provider}`;
    case "signed_in":
      return v.account === "" ? `Signed in to ${provider}` : `Signed in to ${provider} as ${v.account}`;
    case "expired":
      return `Your ${provider} sign-in expired`;
    case "invalid":
      return v.message === "" ? "The setting is invalid" : `The setting is invalid. ${v.message}`;
    case "failed":
      return `Sign-in failed. ${v.message}`;
  }
}

/** "Finish by 11:42": the time kiro-cli stops waiting, shown once rather than counted down. Exported for its test. */
export function finishByText(deadline: Date): string {
  return `Finish by ${deadline.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`;
}

const DOT_CSS: Readonly<Record<OrgView["kind"], string>> = {
  loading: "idle",
  not_set_up: "idle",
  signed_out: "idle",
  signing_in: "oauth",
  signed_in: "connected",
  expired: "oauth",
  invalid: "failed",
  failed: "failed",
};

/** What the card borrows from the server rows, injected so the row module can mount the card without a cycle. */
export interface OrgCardDeps {
  /** The consent-page link a signing-in row shows. */
  readonly pill: (url: string) => HTMLElement;
  /** The paste box for a callback the browser could not reach. */
  readonly relay: (label: string) => HTMLElement;
  /** Whether the organization has MCP turned off, which hides the card. Reactive. */
  readonly locked: () => boolean;
}

interface FormFields {
  readonly issuer: HTMLInputElement;
  readonly client_id: HTMLInputElement;
  readonly name: HTMLInputElement;
  readonly authorization_endpoint: HTMLInputElement;
  readonly token_endpoint: HTMLInputElement;
}

function field(label: string, input: HTMLInputElement, hint?: string): HTMLElement {
  const lab = el("label", { className: "tool-form-label" }, label, input);
  if (hint !== undefined) {
    lab.appendChild(el("span", { className: "tool-form-hint" }, hint));
  }
  return lab;
}

function textInput(type: "url" | "text", placeholder: string): HTMLInputElement {
  return el("input", { type, className: "tool-form-input", autocomplete: "off", placeholder }) as HTMLInputElement;
}

/** Build the card; its one effect repaints it from the provider read and the sign-in status. Returns the card and
 *  the effect's stop. */
export function mountOrgCard(deps: OrgCardDeps): { readonly card: HTMLElement; readonly stop: () => void } {
  const dot = el("span", { className: "mcp-dot", role: "img" });
  const title = el("span", { className: "mcp-row-name" }, "Organization sign-in");
  const editBtn = el("button", {
    type: "button",
    className: "icon-btn",
    "data-tooltip": "Edit",
    "aria-label": "Edit organization sign-in",
  }) as HTMLButtonElement;
  editBtn.replaceChildren(iconEl(ICON_EDIT_UI));
  const stateText = el("p", { className: "mcp-mode-hint" }) as HTMLParagraphElement;
  const finishBy = el("p", { className: "mcp-sign-in-hint", hidden: true }) as HTMLParagraphElement;
  const controls = el("div", { className: "mcp-row-actions" });
  const linkSlot = el("div");
  const relaySlot = el("div");
  const unreadable = el(
    "p",
    { className: "mcp-mode-hint", hidden: true },
    "The saved organization sign-in could not be read. Saving one here replaces it.",
  );

  const f: FormFields = {
    issuer: textInput("url", "https://login.example.com"),
    client_id: textInput("text", "e.g. kiro-mcp"),
    name: textInput("text", "e.g. Acme"),
    authorization_endpoint: textInput("url", "Found from the issuer when empty"),
    token_endpoint: textInput("url", "Found from the issuer when empty"),
  };
  const formErr = el("p", { className: "section-hint mcp-form-error hidden" }) as HTMLParagraphElement;
  const saveBtn = el("button", { type: "submit", className: "btn-small btn-primary" }, "Save") as HTMLButtonElement;
  const cancelEdit = el("button", { type: "button", className: "btn-small" }, "Cancel") as HTMLButtonElement;
  const removeBtn = el("button", { type: "button", className: "btn-small" }, "Remove") as HTMLButtonElement;
  const form = el(
    "form",
    { className: "mcp-form-section", autocomplete: "off", novalidate: true },
    field("Issuer URL", f.issuer, "The identity provider's https address."),
    field("Client ID", f.client_id, "The client your organization registered for Kiro."),
    field("Name (optional)", f.name, "How sign-in prompts name it."),
    field("Authorization endpoint (optional)", f.authorization_endpoint),
    field("Token endpoint (optional)", f.token_endpoint),
    formErr,
    el("div", { className: "mcp-relay-row" }, saveBtn, cancelEdit, removeBtn),
  ) as HTMLFormElement;

  const card = el(
    "section",
    { className: "mcp-row mcp-row-readonly", id: ORG_CARD_ID, "aria-labelledby": `${ORG_CARD_ID}-title` },
    el(
      "div",
      { className: "mcp-row-body" },
      el("div", { className: "mcp-row-name-line" }, dot, title),
      stateText,
      finishBy,
      linkSlot,
      relaySlot,
      unreadable,
      form,
    ),
    el("div", { className: "mcp-row-actions" }, controls, editBtn),
  );
  title.id = `${ORG_CARD_ID}-title`;

  let editing = false;
  const fill = (p: IdentityProviderConfig | null): void => {
    f.issuer.value = p?.issuer ?? "";
    f.client_id.value = p?.client_id ?? "";
    f.name.value = p?.name ?? "";
    f.authorization_endpoint.value = p?.authorization_endpoint ?? "";
    f.token_endpoint.value = p?.token_endpoint ?? "";
  };
  const showForm = (open: boolean): void => {
    editing = open;
    formErr.classList.add("hidden");
    formErr.textContent = "";
    paint();
  };
  editBtn.addEventListener("click", () => {
    fill(identityProvider.peek().provider);
    showForm(true);
  });
  cancelEdit.addEventListener("click", () => {
    showForm(false);
  });
  form.addEventListener("submit", (ev) => {
    ev.preventDefault();
    const body: IdentityProviderConfig = { issuer: f.issuer.value.trim(), client_id: f.client_id.value.trim() };
    for (const key of ["name", "authorization_endpoint", "token_endpoint"] as const) {
      const v = f[key].value.trim();
      if (v !== "") {
        body[key] = v;
      }
    }
    setControlBusy(saveBtn, true);
    void saveIdentityProvider
      .dispatch(body, {
        onSuccess: () => {
          void mcpState.refetchIdentityProvider().then(() => {
            showForm(false);
          });
        },
        onError: (err) => {
          const fields = validationFieldsOf(err);
          formErr.textContent = fields.length > 0 ? fields.map((x) => x.message).join(". ") : err.message;
          formErr.classList.remove("hidden");
        },
      })
      .finally(() => {
        setControlBusy(saveBtn, false);
      });
  });
  removeBtn.addEventListener("click", () => {
    setControlBusy(removeBtn, true);
    void removeIdentityProvider
      .dispatch(undefined, {
        onSuccess: () => {
          void mcpState.refetchIdentityProvider().then(() => {
            showForm(false);
          });
        },
      })
      .finally(() => {
        setControlBusy(removeBtn, false);
      });
  });

  const actionBtn = (label: string, run: () => Promise<unknown>): HTMLButtonElement => {
    const b = el("button", { type: "button", className: "btn-small mcp-sign-in-btn" }, label) as HTMLButtonElement;
    b.addEventListener("click", () => {
      setControlBusy(b, true);
      void run().finally(() => {
        setControlBusy(b, false);
      });
    });
    return b;
  };
  const signInBtn = actionBtn("Sign in", () => orgSignIn.dispatch(undefined));
  const cancelBtn = actionBtn("Cancel", () => orgSignOut.dispatch(undefined));
  const signOutBtn = actionBtn("Sign out", () => orgSignOut.dispatch(undefined));

  // The link and paste box are rebuilt only when the sign-in page changes, so an open paste box keeps its text.
  let shownURL: string | null = null;
  let relayShown = false;

  function paint(): void {
    const cfg = identityProvider.value;
    const view = orgView(cfg, orgSignInStatus.value);
    const provider = orgProviderName();
    card.hidden = deps.locked();
    const text = orgStateText(view, provider);
    stateText.textContent = text;
    dot.className = `mcp-dot ${DOT_CSS[view.kind]}`;
    dot.setAttribute("aria-label", `Organization sign-in: ${text}`);
    unreadable.hidden = !cfg.unreadable;

    const formOpen = view.kind === "not_set_up" || editing;
    form.hidden = !formOpen;
    editBtn.hidden = formOpen || view.kind === "loading";
    cancelEdit.hidden = cfg.provider === null;
    removeBtn.hidden = cfg.provider === null;
    if (view.kind === "not_set_up" && !editing) {
      editing = true;
      fill(null);
    }

    const buttons: HTMLButtonElement[] = [];
    if (view.kind === "signed_out" || view.kind === "expired" || view.kind === "failed") {
      buttons.push(signInBtn);
    } else if (view.kind === "signing_in") {
      buttons.push(cancelBtn);
    } else if (view.kind === "signed_in") {
      buttons.push(signOutBtn);
    }
    controls.replaceChildren(...buttons);

    const signing = view.kind === "signing_in" ? view : null;
    finishBy.hidden = signing?.deadline === undefined;
    finishBy.textContent = signing?.deadline === undefined ? "" : finishByText(signing.deadline);
    const url = signing === null || signing.url === "" ? null : signing.url;
    if (url !== shownURL) {
      shownURL = url;
      linkSlot.replaceChildren(...(url === null ? [] : [deps.pill(url)]));
    }
    const relay = url !== null && signing?.relayed === false;
    if (relay !== relayShown) {
      relayShown = relay;
      relaySlot.replaceChildren(...(relay ? [deps.relay(provider)] : []));
    }
  }

  const stop = effect(() => {
    paint();
  });
  return { card, stop };
}
