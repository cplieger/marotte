import type { ForgeKind } from "./wire/types.gen.js";
import { connectPAT, detectForge, type ConnectionOptions } from "./actions/forge.js";
import { withAsyncFeedback } from "./async-button.js";
import type { ConnectionTarget } from "./forge-auth-connection.js";
import { kindTitle } from "./forge-types.js";
import { el } from "@cplieger/reactive";

type PATHelp = { kind: "link"; url: string; label: string } | { kind: "text"; text: string };

const PAT_HELP: Record<ForgeKind, PATHelp> = {
  github: {
    kind: "link",
    url: "https://github.com/settings/tokens/new?scopes=repo,read:org,workflow&description=Marotte",
    label: "Create a GitHub token",
  },
  gitlab: {
    kind: "link",
    url: "https://gitlab.com/-/user_settings/personal_access_tokens?name=Marotte&scopes=api",
    label: "Create a GitLab token",
  },
  codeberg: {
    kind: "link",
    url: "https://codeberg.org/user/settings/applications",
    label: "Create a Codeberg token",
  },
  gitea: {
    kind: "text",
    text: "Create a token at /user/settings/applications on your Gitea or Forgejo host.",
  },
};

/** The least a token needs for every operation Marotte calls, per family. */
const SCOPES = {
  github: "repo, read:org, workflow",
  gitlab: "api",
  gitea: "write:repository, write:issue, read:user",
} as const;

const GITEA_SCOPES = `Token scopes: ${SCOPES.gitea}.`;

const PAT_SCOPES: Record<ForgeKind, string> = {
  github: `Classic token scopes: ${SCOPES.github}. A fine-grained token needs read and write on Contents, Pull requests, Issues and Actions, and read on Commit statuses and Metadata.`,
  gitlab: `Token scope: ${SCOPES.gitlab}.`,
  codeberg: GITEA_SCOPES,
  gitea: GITEA_SCOPES,
};

const ANY_FORGE_SCOPES = `Token scopes: ${SCOPES.github} on GitHub (classic token), ${SCOPES.gitlab} on GitLab, ${SCOPES.gitea} on Gitea or Forgejo.`;

export interface PATFormDeps {
  closeSlot: (slot: HTMLElement) => void;
  /** The token connected the account `id` from the pane in `slot`. */
  connected: (slot: HTMLElement, id: string) => void;
}

/** Connects with the host and token read once per press. Resolves to the kind
 *  connected, or null when the request was cancelled; rejects after writing
 *  the refusal into `status`. */
type Connect = (host: string, token: string, status: HTMLElement) => Promise<ForgeKind | null>;

export function renderPATForm(
  hostEl: HTMLElement,
  kind: ForgeKind,
  slot: HTMLElement,
  target: ConnectionTarget,
  deps: PATFormDeps,
): void {
  hostEl.innerHTML = "";

  const help = PAT_HELP[kind];
  const helpEl = el("p", { className: "forge-help" });
  switch (help.kind) {
    case "link":
      helpEl.appendChild(
        el("a", { href: help.url, target: "_blank", rel: "noreferrer" }, help.label),
      );
      break;
    case "text":
      helpEl.textContent = help.text;
      break;
  }
  hostEl.appendChild(helpEl);
  hostEl.appendChild(el("p", { className: "forge-help forge-scopes" }, PAT_SCOPES[kind]));
  hostEl.appendChild(
    tokenForm(slot, target, deps, async (host, token, status) =>
      (await connectAs({ kind, host, token, options: target.options() }, status)) ? kind : null,
    ),
  );
}

/** The token form for a server whose forge kind is not known: the detect
 *  route names the kind, then the same body connects as that kind. */
export function renderDetectForm(
  hostEl: HTMLElement,
  slot: HTMLElement,
  target: ConnectionTarget,
  deps: PATFormDeps,
): void {
  hostEl.replaceChildren(
    el("p", { className: "forge-help forge-scopes" }, ANY_FORGE_SCOPES),
    tokenForm(slot, target, deps, (host, token, status) =>
      detectThenConnect(host, token, target.options(), status),
    ),
  );
}

function tokenForm(
  slot: HTMLElement,
  target: ConnectionTarget,
  deps: PATFormDeps,
  connect: Connect,
): HTMLFormElement {
  const form = el("form", { className: "forge-pat-form" }) as HTMLFormElement;

  const tokenInput = el("input", {
    type: "password",
    placeholder: "token",
    className: "tool-form-input",
    "aria-label": "Personal access token",
    autocomplete: "off",
    required: true,
  }) as HTMLInputElement;
  form.appendChild(tokenInput);

  const status = el("div", { className: "forge-card-status", "aria-live": "polite" });
  form.appendChild(status);

  const submit = el(
    "button",
    { type: "submit", className: "btn-small btn-primary" },
    "Connect",
  ) as HTMLButtonElement;
  form.appendChild(submit);

  const cancel = el("button", { type: "button", className: "btn-small" }, "Cancel");
  cancel.addEventListener("click", () => {
    deps.closeSlot(slot);
  });
  form.appendChild(cancel);

  form.addEventListener("submit", (e) => {
    e.preventDefault();
    const host = target.host();
    const token = tokenInput.value.trim();
    if (host === "" || token === "") {
      setLine(status, "Both host and token are required.", "err");
      return;
    }
    setLine(status, "");
    void withAsyncFeedback(
      submit,
      async () => {
        const kind = await connect(host, token, status);
        if (kind === null) {
          return;
        }
        tokenInput.value = "";
        deps.connected(slot, `${kind}:${host}`);
      },
      { keepLabel: true },
    );
  });

  return form;
}

/** Connect, landing the outcome in `status`: true when connected, false when
 *  cancelled. Rejects on a refusal so the button's feedback shows it. */
async function connectAs(
  args: Parameters<typeof connectPAT.dispatch>[0],
  status: HTMLElement,
): Promise<boolean> {
  // The framework's error toast is suppressed (error: false), so this status line is the only failure surface.
  const o = await connectPAT.dispatch(args).outcome;
  if (o.status === "cancelled") {
    setLine(status, "");
    return false;
  }
  const refusal = o.status === "error" ? o.error.message : o.value.error;
  if (refusal !== undefined) {
    setLine(status, refusal, "err");
    throw new Error(refusal);
  }
  setLine(status, "Connected.", "ok");
  return true;
}

async function detectThenConnect(
  host: string,
  token: string,
  options: ConnectionOptions,
  status: HTMLElement,
): Promise<ForgeKind | null> {
  const o = await detectForge.dispatch({ host, token, options }).outcome;
  if (o.status === "cancelled") {
    setLine(status, "");
    return null;
  }
  if (o.status === "error") {
    const refusal =
      o.error.code === "family_undetected"
        ? `${host} did not answer as a supported forge (GitHub, GitLab, Gitea or Forgejo). Check the address.`
        : o.error.message;
    setLine(status, refusal, "err");
    throw new Error(refusal);
  }
  const kind = o.value.kind;
  setLine(status, `${kindTitle(kind)} found. Connecting...`);
  return (await connectAs({ kind, host, token, options }, status)) ? kind : null;
}

function setLine(status: HTMLElement, text: string, kind: "ok" | "err" | "" = ""): void {
  status.textContent = text;
  status.className = kind === "" ? "forge-card-status" : `forge-card-status ${kind}`;
}
