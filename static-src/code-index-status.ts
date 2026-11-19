// The Kiro Code Analysis sub-row in Settings: the code index's status line, kiro-cli's message under it, and one
// button that reads Cancel while a build runs and Build otherwise. Shown only while the switch is on.

import { effect } from "@cplieger/reactive";

import { apiAction } from "./actions/index.js";
import { apiGetTyped } from "./api-client.js";
import { BUS_RECONCILE, onBus, onSSE } from "./bus.js";
import { byId } from "./dom.js";
import { shownSettingsSignal } from "./persist.js";
import { decodeCodeIndexView } from "./wire/decoders.gen.js";
import type { CodeIndexBuild, CodeIndexStatusPayload } from "./wire/types.gen.js";

/** kiro-cli's four build phases, in order: the step a running build shows. */
const PHASES: readonly (readonly [phase: string, label: string])[] = [
  ["scan", "Scanning"],
  ["parse", "Parsing"],
  ["graph", "Graphing"],
  ["save", "Saving"],
];

/** What the row shows for one status: the line, kiro-cli's text under it, and the button. */
interface CodeIndexLine {
  text: string;
  detail: string;
  action: "build" | "cancel";
}

function buildRunning(b: CodeIndexBuild | undefined): boolean {
  return b?.state === "running" || b?.state === "queued";
}

function count(n: number): string {
  return n.toLocaleString("en-US");
}

/** The row for `st`, or for no status reported yet. */
export function codeIndexLine(st: CodeIndexStatusPayload | undefined): CodeIndexLine {
  const b = st?.build;
  if (b !== undefined && buildRunning(b)) {
    if (b.state === "queued") {
      return { text: "Build queued", detail: b.message ?? "", action: "cancel" };
    }
    const step = PHASES.findIndex(([phase]) => phase === b.phase);
    const label = PHASES[step]?.[1];
    const text =
      label === undefined
        ? "Building"
        : `Building: ${label} (${String(step + 1)} of ${String(PHASES.length)})`;
    return { text, detail: b.message ?? "", action: "cancel" };
  }
  const why = [b?.reason, b?.detail].filter((s) => s !== undefined && s !== "").join(": ");
  if (b?.state === "failed") {
    return { text: "Build failed", detail: why, action: "build" };
  }
  if (b?.state === "canceled") {
    return { text: "Build cancelled", detail: why, action: "build" };
  }
  const state = st?.store.state;
  switch (state) {
    case "ready": {
      const s = st?.store.summary;
      const text =
        s === undefined
          ? "Ready"
          : `Ready: ${count(s.files)} files, ${count(s.functions)} functions`;
      return { text, detail: "", action: "build" };
    }
    case "unreadable":
      return { text: "The index cannot be read", detail: "", action: "build" };
    case undefined:
    case "none":
      return { text: "No index yet", detail: "", action: "build" };
    default:
      return { text: `Index ${state}`, detail: "", action: "build" };
  }
}

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for an action with no result
const startBuild = apiAction<undefined, void>({
  name: "codeIndex.build",
  request: () => ({ method: "POST", path: "/api/code-index/build" }),
  error: false,
});

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for an action with no result
const cancelBuild = apiAction<{ buildID: string }, void>({
  name: "codeIndex.cancel",
  request: ({ buildID }) => ({
    method: "POST",
    path: "/api/code-index/cancel",
    body: { build_id: buildID },
  }),
  error: false,
});

/** Mounts the row on the markup's ids; call once at boot. */
export function mountCodeIndexStatus(): void {
  const row = byId<HTMLElement>("code-index-status");
  const text = byId<HTMLElement>("code-index-status-text");
  const detail = byId<HTMLElement>("code-index-status-detail");
  const button = byId<HTMLButtonElement>("code-index-action");
  const statuses = new Map<string, CodeIndexStatusPayload>();
  let root = "";
  /** A refused Build or Cancel's message, shown until the next status. */
  let refusal = "";

  const shown = (): CodeIndexStatusPayload | undefined => statuses.get(root);
  const render = (): void => {
    const line = codeIndexLine(shown());
    text.textContent = line.text;
    const d = refusal !== "" ? refusal : line.detail;
    detail.textContent = d;
    detail.hidden = d === "";
    button.textContent = line.action === "cancel" ? "Cancel" : "Build";
  };
  const read = (signal?: AbortSignal): void => {
    void apiGetTyped("/api/code-index", decodeCodeIndexView, signal).then((v) => {
      if (v === null) {
        return;
      }
      root = v.root;
      for (const st of v.statuses) {
        statuses.set(st.workspace_root, st);
      }
      render();
    });
  };

  button.addEventListener("click", () => {
    const b = shown()?.build;
    const out =
      b !== undefined && buildRunning(b)
        ? cancelBuild.dispatch({ buildID: b.build_id })
        : startBuild.dispatch(undefined);
    button.disabled = true;
    void out.outcome.then((o) => {
      button.disabled = false;
      refusal = o.status === "error" ? o.error.message : "";
      render();
    });
  });
  onSSE("code_index_status", (_chat, st) => {
    statuses.set(st.workspace_root, st);
    refusal = "";
    render();
  });
  onBus(BUS_RECONCILE, ({ signal }) => {
    read(signal);
  });
  effect(() => {
    row.hidden = shownSettingsSignal.value?.code_analysis_enabled !== true;
  });
  render();
  read();
}
