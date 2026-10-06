// The LOCAL half of a tab. The SHARED half is `TabSubject` (internal/marotte/domain_tabs.go):
// membership, persisted and on the wire. `TabViewSpec` is behaviour and pixels (view, route, hooks,
// icon, name, dot), never persisted or transmitted. DOM-free; tabs.ts paints, tab-materialize.ts
// produces.

import type { Route } from "./route-path.js";
// The kinds' one definition is the Go const block, emitted as a registered enum, so both tables
// below fail the type gate on a new kind.
import type { TabKind } from "./types.js";
import {
  ICON_TAB_CHAT,
  ICON_TAB_SETTINGS,
  ICON_TAB_GIT,
  ICON_TAB_FILES,
  ICON_TAB_RUN,
  ICON_TAB_AGENT,
  ICON_TAB_EDITOR,
  ICON_TAB_HISTORY,
  ICON_TAB_DOCS,
  ICON_TAB_SPEC,
  ICON_TAB_WEB,
} from "./icons.js";

/** The view element each tab kind shows (omit `view` on a hand-built spec for the standard one).
 *  An exhaustive record over the WIRE's TabKind; re-exported by tabs.ts. */
export const TAB_VIEWS: Readonly<Record<TabKind, string>> = {
  chat: "#chat-view",
  settings: "#settings-view",
  git: "#git-view",
  files: "#files-view",
  editor: "#editor-view",
  history: "#history-view",
  docs: "#docs-view",
  run: "#run-view",
  subagent: "#subagent-view",
  spec: "#spec-view",
  web: "#web-view",
};

/** The leading glyph each tab kind renders: one per KIND, no per-tab override, so a `subagent`
 *  tab takes the shared hexagon, not its card's per-subagent glyph. */
export const TAB_ICONS: Readonly<Record<TabKind, string>> = {
  chat: ICON_TAB_CHAT,
  settings: ICON_TAB_SETTINGS,
  git: ICON_TAB_GIT,
  files: ICON_TAB_FILES,
  editor: ICON_TAB_EDITOR,
  history: ICON_TAB_HISTORY,
  docs: ICON_TAB_DOCS,
  run: ICON_TAB_RUN,
  subagent: ICON_TAB_AGENT,
  spec: ICON_TAB_SPEC,
  web: ICON_TAB_WEB,
};

/** The activity dot's states: six from a chat's live state (`tabStatusFor`) and "dirty", the
 *  editor's unsaved mark (a tab is never both). Grammar and rationale: css/12-tabs.css. */
export type TabDotStatus = "idle" | "working" | "waiting" | "input" | "failed" | "done" | "dirty";

/** Everything the strip needs about a tab that the server does not, produced only by
 *  `materializeTab`. No `id`/`kind`/`ref` (the subject's identity) and no `pinned` (mutable). A
 *  readonly SNAPSHOT taken at open; later changes go through the store's mutators. */
export interface TabViewSpec {
  /** The label: a DERIVED DEFAULT that may be a placeholder for an unfetched record; a caller holding
   *  a better name overrides it. */
  readonly name: string;
  /** The leading glyph, from TAB_ICONS. On the spec rather than looked up at
   *  render time so a spec is a complete description of a row. */
  readonly icon: string;
  /** CSS selector for the view element to show, from TAB_VIEWS. */
  readonly view: string;
  /** The typed route; every tab needs a real one for the store's view/render/URL promise. */
  readonly route: Route;
  /** Called when the tab becomes active. */
  readonly onShow?: (() => void) | undefined;
  /** Bring this view's DATA up to date. REQUIRED. Called only by tabs.ts `refreshRow` behind
   *  `viewStale`; writes no projection state and pushes no route. */
  readonly refresh: () => void;
  /** CLIENT-LOCAL teardown on close, identical on every device; the server's close does the rest.
   *  Deferred to the pending-op confirmation for this device's own close; immediate for a remote one. */
  readonly onClose?: (() => void) | undefined;
  /** Whether closing this tab tears down what it shows: a SUBJECT fact copied through, never derived
   *  from the kind. `owns: false` makes it a VIEW. Safe to snapshot: `owns` never changes after open. */
  readonly owns: boolean;
  /** The parent tab at open, making this a SUB-TAB (indented, sorted after it, not draggable, closed
   *  with it). `reparent_tab` can move it, so readers take the parent from the subject. */
  readonly parentId?: string | undefined;
  /** The dot's state at materialization, so a CREATED row (e.g. the boot restore) shows it. LIVE
   *  state: never persisted. */
  readonly dotStatus?: TabDotStatus | undefined;
}
