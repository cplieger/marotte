import type { ForgeKind } from "./wire/types.gen.js";
import { connectPAT, detectForge, type ConnectionOptions } from "./actions/forge.js";
import { buildAddForm, wireAddForm } from "./add-form.js";
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
  /** The token connected the account `id` from the pane in `slot`. */
  connected: (slot: HTMLElement, id: string) => void;
}

/** The server's refusal codes whose remedy is a corrected address, so the Server field is the one marked. */
const HOST_REFUSALS: ReadonlySet<string> = new Set([
  "family_undetected",
  "plaintext_refused",
  "private_address_refused",
]);

/** The pane's two lines: progress under the row, and the row's own error line for a refusal. */
interface Lines {
  note: (text: string) => void;
  /** `code` is the server's error code, when the refusal came from it. */
  refuse: (why: string, code?: string) => void;
}

/** Connects with the host and token read once per press. Resolves to the id of the connection the server saved, or
 *  null when the request was cancelled or refused, after `lines` says which. */
type Connect = (host: string, token: string, lines: Lines) => Promise<string | null>;

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
    tokenForm(slot, target, deps, (host, token, lines) =>
      connectAs({ kind, host, token, options: target.options() }, lines),
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
    tokenForm(slot, target, deps, (host, token, lines) =>
      detectThenConnect(host, token, target.options(), lines),
    ),
  );
}

let tokenForms = 0;

/** The token row, plus a progress line under it. The pane's `+` opener closes it, so the row carries no Cancel. */
function tokenForm(
  slot: HTMLElement,
  target: ConnectionTarget,
  deps: PATFormDeps,
  connect: Connect,
): DocumentFragment {
  const tokenInput = el("input", {
    type: "password",
    placeholder: "personal access token",
    autocomplete: "off",
  }) as HTMLInputElement;
  tokenForms += 1;
  const form = buildAddForm({
    fields: [{ label: "Token", control: tokenInput }],
    label: "Connect",
    ariaLabel: "Connect with a token",
    errorId: `forge-token-error-${String(tokenForms)}`,
  });
  form.dataset["forgeToken"] = "";
  const status = el("div", { className: "forge-card-status", "aria-live": "polite" });

  const row = wireAddForm(form, {
    refuse: () => (target.host() === "" ? "Enter the server's host." : null),
    refuses: target.hostInput,
    onSubmit: async () => {
      const lines: Lines = {
        note: (text) => {
          status.textContent = text;
        },
        refuse: (why, code) => {
          status.textContent = "";
          row.setError(
            why,
            code !== undefined && HOST_REFUSALS.has(code) ? target.hostInput : tokenInput,
          );
        },
      };
      lines.note("");
      row.setError(null);
      const id = await connect(target.host(), tokenInput.value.trim(), lines);
      if (id !== null) {
        tokenInput.value = "";
        deps.connected(slot, id);
      }
    },
  });

  const frag = document.createDocumentFragment();
  frag.append(form, status);
  return frag;
}

async function connectAs(
  args: Parameters<typeof connectPAT.dispatch>[0],
  lines: Lines,
): Promise<string | null> {
  // The framework's error toast is suppressed (error: false), so the row's error line is the only failure surface.
  const o = await connectPAT.dispatch(args).outcome;
  if (o.status === "cancelled") {
    lines.note("");
    return null;
  }
  if (o.status === "error") {
    lines.refuse(o.error.message, o.error.code);
    return null;
  }
  if (o.value.forge_id === "") {
    lines.refuse("the server answered no connection id");
    return null;
  }
  return o.value.forge_id;
}

async function detectThenConnect(
  host: string,
  token: string,
  options: ConnectionOptions,
  lines: Lines,
): Promise<string | null> {
  const o = await detectForge.dispatch({ host, token, options }).outcome;
  if (o.status === "cancelled") {
    lines.note("");
    return null;
  }
  if (o.status === "error") {
    lines.refuse(
      o.error.code === "family_undetected"
        ? `${host} did not answer as a supported forge (GitHub, GitLab, Gitea or Forgejo). Check the address.`
        : o.error.message,
      o.error.code,
    );
    return null;
  }
  const kind = o.value.kind;
  lines.note(`${kindTitle(kind)} found. Connecting…`);
  return connectAs({ kind, host, token, options }, lines);
}
