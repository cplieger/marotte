// Which loaded items of the workspace's composed configuration kiro-cli does not use (another layer replaced or
// switched them off), and the agent files it could not load: one store, read by the Docs rows and the MCP cards.

import { el, signal, type ReadonlySignal } from "@cplieger/reactive";
import { apiGetTyped } from "./api-client.js";
import { BUS_RECONCILE, onBus, onSSE } from "./bus.js";
import { decodeConfigStandingResponse } from "./wire/decoders.gen.js";
import type { AgentLoadError, ConfigStanding, ConfigStandingResponse } from "./wire/types.gen.js";

interface StandingView {
  /** File-backed items, by the Docs row's `path`. */
  readonly files: ReadonlyMap<string, ConfigStanding>;
  /** MCP servers, by server name. */
  readonly servers: ReadonlyMap<string, ConfigStanding>;
  /** Agent load errors, by the Docs row's `path`, whatever that row's standing. */
  readonly loadErrors: ReadonlyMap<string, AgentLoadError>;
}

const MCP_SERVER_TYPE = "mcp-server";

const view = signal<StandingView>({ files: new Map(), servers: new Map(), loadErrors: new Map() });

export const configStanding: ReadonlySignal<StandingView> = view;

let following = false;
/** Numbers each read so only the newest answer lands. */
let reads = 0;

/** Re-read GET /api/config/standing. The first call also follows `config_standing_changed` and every stream gap. A
 *  failed read keeps the last answer. */
export function refreshConfigStanding(): void {
  if (!following) {
    following = true;
    onSSE("config_standing_changed", refreshConfigStanding);
    // A gap may have swallowed the change signal, and the next one comes only on a change.
    onBus(BUS_RECONCILE, refreshConfigStanding);
  }
  const read = ++reads;
  void apiGetTyped("/api/config/standing", decodeConfigStandingResponse).then((d) => {
    if (read === reads && d !== null) {
      view.value = toView(d);
    }
  });
}

function toView(d: ConfigStandingResponse): StandingView {
  const files = new Map<string, ConfigStanding>();
  const servers = new Map<string, ConfigStanding>();
  for (const s of d.standing) {
    const into = s.type === MCP_SERVER_TYPE ? servers : files;
    if (!into.has(s.key)) {
      into.set(s.key, s);
    }
  }
  return { files, servers, loadErrors: new Map(Object.entries(d.load_errors)) };
}

function layerName(s: ConfigStanding): string {
  if (s.decided_by_name !== "") {
    return s.decided_by_name;
  }
  return s.decided_by !== "" ? s.decided_by : "another layer";
}

/** The badge text: what became of the item, and the layer that decided it. */
export function standingLabel(s: ConfigStanding): string {
  switch (s.kind) {
    case "displaced":
      return `Replaced by ${layerName(s)}`;
    case "disabled":
      return `Off by ${layerName(s)}`;
    default:
      return "Not in use";
  }
}

/** The badge tooltip: the layer, and for a displaced item the compose rule that replaced it. */
export function standingDetail(s: ConfigStanding): string {
  const layer = layerName(s);
  switch (s.kind) {
    case "displaced": {
      const rule = s.compose ?? "";
      return rule === ""
        ? `${layer} has an item that replaces this one.`
        : `${layer} has an item that replaces this one (compose rule: ${rule}).`;
    }
    case "disabled":
      return `A ${layer} setting switched this off.`;
    default:
      return `kiro-cli loaded this but does not use it (${s.kind}, decided by ${layer}).`;
  }
}

export function standingBadge(s: ConfigStanding): HTMLElement {
  const badge = el("span", { className: "docs-badge docs-badge-standing" }, standingLabel(s));
  badge.setAttribute("data-tooltip", standingDetail(s));
  return badge;
}

/** The line a row shows under its badges for an agent file kiro-cli could not load. */
export function loadErrorText(e: AgentLoadError): string {
  return `kiro-cli could not load this agent: ${e.error}`;
}
