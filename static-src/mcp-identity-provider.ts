// The Organization sign-in card on the MCP page: marotte's provider record and kiro-cli's one organization sign-in per
// engine. Its states and actions are those of @cplieger/ui's coming connection element, so moving onto it is a swap.

import { el, effect, signal } from "@cplieger/reactive";

import { setControlBusy } from "./dom.js";
import { confirm as confirmDialog } from "./confirm.js";
import {
  identityProviderConfig,
  identityProviderName,
  identityProviderStatus,
  mcpState,
  type IdentityProviderConfig,
} from "./mcp-state.js";
import { clearFieldMarks, showSubmitError } from "./mcp-panels.js";
import {
  removeIdentityProvider,
  saveIdentityProvider,
  signInOrganization,
  signOutOrganization,
} from "./actions/mcp.js";
import type { IdentityProvider, MCPIdentityProviderPayload } from "./wire/types.gen.js";

type CardState =
  | "none"
  | "signed-out"
  | "signing-in"
  | "signed-in"
  | "session-expired"
  | "invalid-config";

/** The card's state: none while marotte holds no provider, else kiro-cli's report. A Sign in the user started is
 *  signing in before kiro-cli says so, and an unreported provider is not signed in. */
function cardStateOf(
  cfg: IdentityProviderConfig,
  p: MCPIdentityProviderPayload | undefined,
): CardState {
  if (cfg.config === null && !cfg.unreadable) {
    return "none";
  }
  if (p?.signing_in === true) {
    return "signing-in";
  }
  switch (p?.status?.state) {
    case "signing-in":
      return "signing-in";
    case "signed-in":
      return "signed-in";
    case "session-expired":
      return "session-expired";
    case "invalid-config":
      return "invalid-config";
    default:
      return "signed-out";
  }
}

/** The sign-in method's controls: the consent page's link and the paste box for a callback the browser cannot
 *  reach. */
export interface SignInSlot {
  link(url: string): HTMLElement;
  paste(): HTMLElement;
}

const HINT =
  "Some MCP servers sign in through your organization's identity provider instead of their own. Set it up with the issuer and client ID your administrator gave you.";

const UNREADABLE =
  "The saved provider could not be read from marotte's mcp.json, and kiro-cli keeps using it. Edit replaces it, and Remove deletes it.";

// KAS waits 300 s for the consent page's callback.
const TIME_LIMIT = "Finish signing in within 5 minutes.";

function stateText(s: CardState, p: MCPIdentityProviderPayload | undefined): string {
  switch (s) {
    case "none":
      return "Not set up";
    case "signed-out":
      return "Not signed in";
    case "signing-in":
      return "Signing in";
    case "signed-in": {
      const account = p?.status?.account ?? "";
      return account === "" ? "Signed in" : `Signed in as ${account}`;
    }
    case "session-expired":
      return "Sign-in expired";
    case "invalid-config":
      return "Configuration error";
  }
}

/** kiro-cli's own words for what went wrong, or what the reader waits on; "" for none. */
function detailText(s: CardState, p: MCPIdentityProviderPayload | undefined): string {
  if (s === "invalid-config") {
    return p?.status?.error ?? "";
  }
  if (s === "signing-in" && p?.signing_in === true && (p.oauth_url ?? "") === "") {
    return "Waiting for kiro-cli to open the sign-in page.";
  }
  const err = p?.sign_in_error ?? "";
  if ((s === "signed-out" || s === "session-expired") && err !== "") {
    return `The last sign-in did not complete: ${err}`;
  }
  return "";
}

function identityText(cfg: IdentityProviderConfig, name: string, s: CardState): string {
  if (s === "none") {
    return "";
  }
  if (cfg.config === null) {
    return UNREADABLE;
  }
  return name === "" || name === cfg.config.issuer ? cfg.config.issuer : `${name} · ${cfg.config.issuer}`;
}

function actionBtn(label: string, cls: string, run: (btn: HTMLButtonElement) => Promise<unknown>): HTMLButtonElement {
  const btn = el("button", { type: "button", className: `btn-small ${cls}` }, label) as HTMLButtonElement;
  btn.addEventListener("click", () => {
    if (btn.disabled) {
      return;
    }
    setControlBusy(btn, true);
    void run(btn).finally(() => {
      setControlBusy(btn, false);
    });
  });
  return btn;
}

interface FormField {
  readonly key: keyof IdentityProvider;
  readonly id: string;
  readonly label: string;
  readonly hint: string;
  readonly type: "url" | "text";
}

/** kiro-cli's own `mcp idp setup` flags and their help. */
const FIELDS: readonly FormField[] = [
  {
    key: "issuer",
    id: "mcp-idp-issuer",
    label: "Issuer",
    hint: "The identity provider's issuer URL, such as https://acme.okta.com.",
    type: "url",
  },
  {
    key: "client_id",
    id: "mcp-idp-client-id",
    label: "Client ID",
    hint: "Kiro's client ID at the identity provider, issued by your administrator.",
    type: "text",
  },
  {
    key: "name",
    id: "mcp-idp-name",
    label: "Name",
    hint: "The name to show for the identity provider. Leave empty for the issuer's host.",
    type: "text",
  },
  {
    key: "authorization_endpoint",
    id: "mcp-idp-authorization-endpoint",
    label: "Authorization endpoint",
    hint: "Only for an identity provider that publishes no discovery metadata.",
    type: "url",
  },
  {
    key: "token_endpoint",
    id: "mcp-idp-token-endpoint",
    label: "Token endpoint",
    hint: "Only for an identity provider that publishes no discovery metadata.",
    type: "url",
  },
];

const OPTIONAL: ReadonlySet<keyof IdentityProvider> = new Set([
  "name",
  "authorization_endpoint",
  "token_endpoint",
]);

interface ProviderForm {
  readonly form: HTMLFormElement;
  open(cfg: IdentityProvider | null): void;
}

function buildForm(onClose: () => void): ProviderForm {
  const inputs = new Map<keyof IdentityProvider, HTMLInputElement>();
  const labels = FIELDS.map((f) => {
    const input = el("input", {
      id: f.id,
      className: "tool-form-input",
      type: f.type,
      autocomplete: "off",
      spellcheck: "false",
    }) as HTMLInputElement;
    inputs.set(f.key, input);
    const aux = OPTIONAL.has(f.key) ? [" ", el("span", { className: "tool-form-label-aux" }, "(optional)")] : [];
    return el(
      "label",
      { className: "tool-form-label" },
      f.label,
      ...aux,
      input,
      el("span", { className: "tool-form-hint" }, f.hint),
    );
  });
  const err = el("p", { className: "section-hint mcp-form-error hidden" }) as HTMLParagraphElement;
  const save = el("button", { type: "submit", className: "btn-small btn-primary mcp-idp-save" }, "Save") as HTMLButtonElement;
  const discard = el("button", { type: "button", className: "btn-small mcp-idp-discard" }, "Cancel") as HTMLButtonElement;
  const form = el(
    "form",
    { className: "mcp-idp-form", autocomplete: "off", novalidate: true, hidden: true },
    ...labels,
    err,
    el("div", { className: "mcp-idp-actions" }, save, discard),
  ) as HTMLFormElement;

  const close = (): void => {
    form.hidden = true;
    onClose();
  };
  discard.addEventListener("click", close);
  form.addEventListener("submit", (ev) => {
    ev.preventDefault();
    err.classList.add("hidden");
    err.replaceChildren();
    clearFieldMarks();
    const body: IdentityProvider = { issuer: "", client_id: "" };
    for (const f of FIELDS) {
      const v = inputs.get(f.key)?.value.trim() ?? "";
      if (v !== "" || !OPTIONAL.has(f.key)) {
        body[f.key] = v;
      }
    }
    setControlBusy(save, true);
    void saveIdentityProvider
      .dispatch(body)
      .outcome.then((o) => {
        if (o.status !== "success") {
          showSubmitError(err, o.status === "error" ? o.error : undefined, "Save failed.");
          return;
        }
        close();
        void mcpState.refetchIdentityProvider();
      })
      .finally(() => {
        setControlBusy(save, false);
      });
  });

  return {
    form,
    open(cfg) {
      for (const f of FIELDS) {
        const input = inputs.get(f.key);
        if (input !== undefined) {
          input.value = cfg?.[f.key] ?? "";
        }
      }
      clearFieldMarks();
      err.classList.add("hidden");
      err.replaceChildren();
      form.hidden = false;
      inputs.get("issuer")?.focus();
    },
  };
}

/** Build the card into `host` and keep it on the provider's record and status; returns its stop function. */
export function mountIdentityProviderCard(host: HTMLElement, slot: SignInSlot): () => void {
  const editing = signal(false);
  const identity = el("p", { className: "mcp-idp-identity" });
  const state = el("p", { className: "mcp-idp-state", role: "status" });
  const detail = el("p", { className: "mcp-idp-detail" });
  const slotBox = el("div", { className: "mcp-idp-slot" });
  const form = buildForm(() => {
    editing.value = false;
  });

  const signIn = actionBtn("Sign in", "mcp-idp-sign-in", () => signInOrganization.dispatch(undefined));
  const cancel = actionBtn("Cancel", "mcp-idp-cancel", () => signOutOrganization.dispatch(undefined));
  const signOut = actionBtn("Sign out", "mcp-idp-sign-out", () => signOutOrganization.dispatch(undefined));
  const edit = el("button", { type: "button", className: "btn-small mcp-idp-edit" }, "Edit") as HTMLButtonElement;
  edit.addEventListener("click", () => {
    editing.value = true;
    form.open(identityProviderConfig.peek().config);
  });
  const remove = actionBtn("Remove", "mcp-idp-remove btn-danger", async () => {
    const ok = await confirmDialog({
      title: "Remove the organization sign-in?",
      body: "Servers that sign in through your organization stop connecting, and kiro-cli forgets the organization session.",
      confirmLabel: "Remove",
      variant: "destructive",
    });
    if (!ok) {
      return;
    }
    await removeIdentityProvider.dispatch(undefined, {
      onSuccess: () => {
        void mcpState.refetchIdentityProvider();
      },
    });
  });
  const actions = el("div", { className: "mcp-idp-actions" }, signIn, cancel, signOut, edit, remove);

  host.replaceChildren(
    el("h3", { className: "mcp-idp-title" }, "Organization sign-in"),
    el("p", { className: "section-hint" }, HINT),
    identity,
    state,
    detail,
    slotBox,
    actions,
    form.form,
  );

  let shownURL: string | null = null;
  let paste: HTMLElement | null = null;
  const paintSlot = (p: MCPIdentityProviderPayload | undefined, s: CardState): void => {
    const url = s === "signing-in" && p?.signing_in === true ? (p.oauth_url ?? "") : "";
    if (url === "") {
      slotBox.replaceChildren();
      slotBox.hidden = true;
      shownURL = null;
      paste = null;
      return;
    }
    slotBox.hidden = false;
    if (shownURL !== url) {
      shownURL = url;
      paste = null;
      slotBox.replaceChildren(slot.link(url), el("p", { className: "mcp-sign-in-hint" }, TIME_LIMIT));
    }
    // A relayed code is spent, so a second paste can only fail.
    if (p?.relayed === true) {
      paste?.remove();
      paste = null;
    } else if (paste === null) {
      paste = slotBox.appendChild(slot.paste());
    }
  };

  return effect(() => {
    const cfg = identityProviderConfig.value;
    const p = identityProviderStatus.value;
    const s = cardStateOf(cfg, p);
    const busy = editing.value;
    host.dataset["state"] = s;
    identity.textContent = identityText(cfg, identityProviderName.value, s);
    identity.hidden = identity.textContent === "";
    state.textContent = stateText(s, p);
    detail.textContent = detailText(s, p);
    detail.hidden = detail.textContent === "";
    paintSlot(p, s);
    signIn.hidden = busy || (s !== "signed-out" && s !== "session-expired");
    cancel.hidden = s !== "signing-in";
    signOut.hidden = busy || s !== "signed-in";
    edit.hidden = busy || s === "signing-in";
    edit.textContent = s === "none" ? "Set up" : "Edit";
    remove.hidden = busy || s === "none" || s === "signing-in";
  });
}
