// The ONE place a TabSubject becomes a TabViewSpec. Chat, editor and run behaviour is INJECTED and
// the singletons load lazily (static imports would close cycles). DOM-free; store reads untracked.

import type { TabKind, TabSubject } from "./types.js";
import type { Route } from "./route-path.js";
import { TAB_ICONS, TAB_VIEWS, type TabDotStatus, type TabViewSpec } from "./tab-view.js";
import { get, subagentStatusFor, turnLive } from "./store.js";
import { runLabelOf } from "./run-store.js";
import { FALLBACK_SUBAGENT_NAME, subagentLabel } from "./roles.js";
import { findSubagentInvocation } from "./subagent-slice.js";
// Acyclic: files-shared.ts imports only @cplieger/reactive.
import { FB_ROOT, normalizeDirPath } from "./files-shared.js";

// --- The injected half ---

/** Chat behaviour, from chat.ts. `dot` is injected because the pending-ask half of
 *  it reads the decision dock, which is not a leaf module; `close` is the chat's
 *  CLIENT-LOCAL teardown, whoever closed the tab. */
interface ChatTabOpener {
  show: (chatID: string) => void;
  refresh: (chatID: string) => void;
  close: (chatID: string) => void;
  dot: (chatID: string) => TabDotStatus | "";
}

/** Editor behaviour, from editor-openers.ts. A subject carries the path and nothing
 *  else; content, dirty state, mode and line selection live in `fileStates`. */
interface EditorTabOpener {
  show: (path: string) => void;
  refresh: (path: string) => void;
  close: (path: string) => void;
}

/** Run behaviour, from run-view.ts. No `owns` and no `cancel`: a run tab is always a
 *  VIEW, and nothing that closes a tab cancels a run. */
interface RunTabOpener {
  show: (workflowID: string) => void;
  refresh: (workflowID: string) => void;
}

/** Subagent behaviour, from subagent-view.ts. No `close` half: the tab is a reading
 *  surface over blocks the chat store owns, so it starts nothing and can stop
 *  nothing, and every door opens it with `owns: false`. */
interface SubagentTabOpener {
  show: (chatID: string, subtaskID: string) => void;
  refresh: (chatID: string, subtaskID: string) => void;
}

/** Spec behaviour, from spec-view.ts. No `close` half for the subagent opener's
 *  reason: the page is a view over documents Kiro writes on disk, so closing it
 *  stops nothing, and every door opens it with `owns: false`. */
interface SpecTabOpener {
  show: (dir: string) => void;
  refresh: (dir: string) => void;
}

export interface TabOpeners {
  readonly chat: ChatTabOpener;
  readonly editor: EditorTabOpener;
  readonly run: RunTabOpener;
  readonly subagent: SubagentTabOpener;
  readonly spec: SpecTabOpener;
}

let openers: TabOpeners | null = null;

/** Register the behaviours the factory cannot author. Called once, from the
 *  composition root; last registration wins. */
export function registerTabOpeners(next: TabOpeners): void {
  openers = next;
}

/** The registered openers, or a throw for EVERY kind, so an unwired composition root fails at the
 *  first tab rather than producing an inert `onShow`. */
function requireOpeners(kind: TabKind): TabOpeners {
  if (openers === null) {
    throw new Error(
      `tab-materialize: no openers registered while materializing a "${kind}" tab; ` +
        `the composition root must call registerTabOpeners before any tab is materialized`,
    );
  }
  return openers;
}

/** Drop the registration. Test isolation only; production never unregisters. */
export function _resetTabOpenersForTest(): void {
  openers = null;
}

// --- Fallback labels ---

/** The name a chat with no store row reads as. Byte-identical to chat.ts's
 *  NEW_CHAT_NAME and duplicated rather than shared, because chat.ts is on the
 *  INJECTED side of this seam and importing it would close the cycle. */
const FALLBACK_CHAT_NAME = "New conversation";

/** The name a run this client has fetched nothing for reads as. Matches
 *  handlers/run.ts's `runLabel` fallback. */
const FALLBACK_RUN_NAME = "Workflow run";

/** A chat's label from the chat store (`store.get` is an untracked peek); empty name = fallback. */
function chatName(chatID: string): string {
  const name = get(chatID)?.name ?? "";
  return name === "" ? FALLBACK_CHAT_NAME : name;
}

/** A run's label. `runLabel` is what the launcher called this EXECUTION and
 *  `workflowName` the recipe's own name, in that order, which is the preference
 *  the transcript's run card already applies. */
function runName(workflowID: string): string {
  const label = runLabelOf(workflowID);
  return label === "" ? FALLBACK_RUN_NAME : label;
}

/** A spec's tab label: the spec directory's last segment, which is the feature
 *  name Kiro wrote it under. */
function specName(dir: string): string {
  const name = dir.replace(/\/+$/, "").split("/").pop() ?? dir;
  return name === "" ? dir : name;
}

/** A file's tab label: its last path segment. Identical to what openEditorView
 *  computes today, deliberately, so a converted call site renames nothing. */
function fileName(path: string): string {
  return path.split("/").pop() ?? path;
}

/** A browser's tab label: the folder's last segment, or "Files" at the mounts
 *  listing. `dir` is always `normalizeDirPath` output, so `"/"` is the only
 *  rootless spelling that reaches this. */
function filesTabName(dir: string): string {
  return dir === FB_ROOT ? "Files" : (dir.split("/").pop() ?? dir);
}

/** A preview tab's label: the page's file name, except an index page, which takes
 *  its folder's name so a strip of several demos stays readable. */
function webTabName(path: string): string {
  const parts = path.split("/").filter((p) => p !== "");
  const file = parts.at(-1) ?? path;
  if (/^index\.html?$/i.test(file) && parts.length >= 2) {
    return parts.at(-2) ?? file;
  }
  return file;
}

// --- The subagent ref codec ---

/** The one composite ref on this wire: `<chatID>/<agentSubtaskID>`, since a delegate is findable
 *  only through its chat. `ids.ValidChatID` admits no slash, so the FIRST slash is the seam. The
 *  codec lives with the module that owns every ref's meaning. */
export function subagentRef(chatID: string, subtaskID: string): string {
  return `${chatID}/${subtaskID}`;
}

/** Split a subagent ref; both halves empty for a malformed persisted ref (rendered as "cannot find
 *  the delegate", never thrown). */
export function parseSubagentRef(ref: string): { chatID: string; subtaskID: string } {
  const cut = ref.indexOf("/");
  if (cut <= 0 || cut === ref.length - 1) {
    return { chatID: "", subtaskID: "" };
  }
  return { chatID: ref.slice(0, cut), subtaskID: ref.slice(cut + 1) };
}

/** A delegate's label AND dot from ONE scan of its invocation, so a restored tab reads like a
 *  linked one and the row never names a delegate beside an empty slot. An unfetched chat falls
 *  back until the next materialization or `subagent-dots.ts` corrects it. */
function subagentTabFacts(
  chatID: string,
  subtaskID: string,
): { name: string; dot: TabDotStatus | "" } {
  const session = chatID === "" ? undefined : get(chatID);
  const tc = session === undefined ? undefined : findSubagentInvocation(session, subtaskID);
  if (tc === undefined) {
    return { name: FALLBACK_SUBAGENT_NAME, dot: "" };
  }
  // The chat's turn liveness, as `subagent-dots.ts` passes, so no stale spinner is seeded. `session`
  // is defined wherever `tc` is; the absent arm claims nothing.
  const live = session === undefined || turnLive(session);
  return { name: subagentLabel(tc), dot: subagentStatusFor(tc.status, live) };
}

// --- Pass-through subject facts ---

/** The sub-tab position as the store spells it (ABSENT for no parent, where a subject says "").
 *  `insertRow` and the server's Open both promote an orphan to top level. */
function parentOf(subject: TabSubject): { parentId?: string } {
  return subject.parent === "" ? {} : { parentId: subject.parent };
}

/** The dot, as the store spells it. `""` means "nothing painted", which the spec
 *  represents as an ABSENT field so a reader can tell it from "painted, then
 *  cleared". */
function dotOf(status: TabDotStatus | ""): { dotStatus?: TabDotStatus } {
  return status === "" ? {} : { dotStatus: status };
}

/** Run a singleton's loader through a LAZY import, swallowing a failed chunk load as app.ts does.
 *  Lazy for every singleton: they reach tabs.ts, which calls materializeTab, so a static import
 *  closes a cycle. Specifiers stay literal at each call site for the bundler. */
function lazily(load: Promise<unknown>): void {
  void load.catch(() => {
    /* noop */
  });
}

// --- The factory ---

/** Produce the local half of a tab from the shared half. Exhaustive over TabKind with NO default:
 *  a new kind falls off the end and `strictNullChecks` rejects it. Never calls a toggle-style
 *  opener (it would close the active tab it describes); only the LOADER halves. */
export function materializeTab(subject: TabSubject): TabViewSpec {
  const reg = requireOpeners(subject.kind);
  switch (subject.kind) {
    case "chat": {
      const chatID = subject.ref;
      return {
        name: chatName(chatID),
        icon: TAB_ICONS.chat,
        view: TAB_VIEWS.chat,
        route: { kind: "chat", id: chatID },
        // From the SUBJECT. A side conversation's own subject carries owns:true
        // because it owns its bridge; a tab that only WATCHES another chat's work
        // carries owns:false. Neither is a property of the kind.
        owns: subject.owns,
        ...parentOf(subject),
        ...dotOf(reg.chat.dot(chatID)),
        onShow: () => {
          reg.chat.show(chatID);
        },
        refresh: () => {
          reg.chat.refresh(chatID);
        },
        onClose: () => {
          reg.chat.close(chatID);
        },
      };
    }
    case "editor": {
      const path = subject.ref;
      return {
        name: fileName(path),
        icon: TAB_ICONS.editor,
        view: TAB_VIEWS.editor,
        // No line or mode: those are the OPENER's arguments (`fileStates`, its pushRoute). Matches
        // openEditorView's route.
        route: { kind: "file", path },
        owns: subject.owns,
        ...parentOf(subject),
        onShow: () => {
          reg.editor.show(path);
        },
        refresh: () => {
          reg.editor.refresh(path);
        },
        onClose: () => {
          reg.editor.close(path);
        },
      };
    }
    case "run": {
      const workflowID = subject.ref;
      // A RUN TAB IS ALWAYS A VIEW: `owns: false`, no `onClose`, so dismissing it stops nothing. A
      // parentless run can outlive every view, so stopping is the CANCEL VERB, offered regardless of door
      // (run-view.ts). The launching chat's × still cancels its runs.
      return {
        name: runName(workflowID),
        icon: TAB_ICONS.run,
        view: TAB_VIEWS.run,
        route: { kind: "run", id: workflowID },
        owns: false,
        ...parentOf(subject),
        onShow: () => {
          reg.run.show(workflowID);
        },
        refresh: () => {
          reg.run.refresh(workflowID);
        },
      };
    }
    case "spec": {
      const dir = subject.ref;
      return {
        name: specName(dir),
        icon: TAB_ICONS.spec,
        view: TAB_VIEWS.spec,
        route: { kind: "spec", dir },
        owns: false,
        ...parentOf(subject),
        onShow: () => {
          reg.spec.show(dir);
        },
        refresh: () => {
          reg.spec.refresh(dir);
        },
      };
    }
    case "subagent": {
      const { chatID, subtaskID } = parseSubagentRef(subject.ref);
      const facts = subagentTabFacts(chatID, subtaskID);
      return {
        name: facts.name,
        icon: TAB_ICONS.subagent,
        view: TAB_VIEWS.subagent,
        route: { kind: "subagent", chat: chatID, id: subtaskID },
        owns: subject.owns,
        ...parentOf(subject),
        ...dotOf(facts.dot),
        onShow: () => {
          reg.subagent.show(chatID, subtaskID);
        },
        refresh: () => {
          reg.subagent.refresh(chatID, subtaskID);
        },
        // No onClose: the page projects blocks the chat store owns; `subagent-view.ts`'s demand effect
        // releases it.
      };
    }
    case "settings":
      return {
        name: "Settings",
        icon: TAB_ICONS.settings,
        view: TAB_VIEWS.settings,
        // The CANONICAL sub-tab (a singleton's Ref is empty); setSettingsTab / applyRoute correct it.
        route: { kind: "settings", tab: "general" },
        owns: subject.owns,
        ...parentOf(subject),
        // No onShow: this tab's whole activation was the data half, which is now
        // `refresh`. The panel it loads is the ACTIVE one, so a deep link's own
        // forceSettingsTab has already landed by the time the dispatcher calls it.
        refresh: () => {
          lazily(
            import("./settings-tabs.js").then(({ refreshSettingsPanel }) => {
              refreshSettingsPanel();
            }),
          );
        },
      };
    case "git":
      return {
        name: "Git",
        icon: TAB_ICONS.git,
        view: TAB_VIEWS.git,
        route: { kind: "git", tab: "changes" },
        owns: subject.owns,
        ...parentOf(subject),
        // Same divergence as settings: navigate.ts's path-link door passes no
        // onShow, so /git reached from a chat's file link did not wire its panel
        // while the sidebar's door did.
        onShow: () => {
          lazily(
            import("./git.js").then(({ loadGitRepos }) => {
              loadGitRepos();
            }),
          );
        },
        refresh: () => {
          lazily(
            import("./git.js").then(({ refreshGitView }) => {
              refreshGitView();
            }),
          );
        },
      };
    case "files": {
      // Normalised ONCE, and that value is spent on the name, the route and both lazy
      // calls: a persisted ref is bounded only by MaxRefBytes, so "/workspace/x/" is
      // legal and spending it raw would put a non-canonical folder in the URL.
      const dir = normalizeDirPath(subject.ref);
      return {
        name: filesTabName(dir),
        icon: TAB_ICONS.files,
        view: TAB_VIEWS.files,
        // The folder this tab was OPENED at; `setFilesRoute` keeps it in step once
        // the tab navigates, or the next unrelated emit overwrites the URL.
        route: { kind: "files", path: dir },
        owns: subject.owns,
        ...parentOf(subject),
        // ONE lazy import doing bind-then-load: split across `onShow` and `refresh`, the
        // two import() promises would decide the order and a load could precede the bind.
        refresh: () => {
          lazily(
            import("./files.js").then(({ showFilesTab }) => {
              showFilesTab(dir);
            }),
          );
        },
        onClose: () => {
          lazily(
            import("./files.js").then(({ releaseFilesTab }) => {
              releaseFilesTab(dir);
            }),
          );
        },
      };
    }
    case "web": {
      const path = subject.ref;
      return {
        name: webTabName(path),
        icon: TAB_ICONS.web,
        view: TAB_VIEWS.web,
        route: { kind: "web", path },
        owns: subject.owns,
        ...parentOf(subject),
        refresh: () => {
          lazily(
            import("./web-view.js").then(({ showWebTab }) => {
              showWebTab(path);
            }),
          );
        },
        onClose: () => {
          lazily(
            import("./web-view.js").then(({ releaseWebTab }) => {
              releaseWebTab(path);
            }),
          );
        },
      };
    }
    case "history":
      return {
        name: "History",
        icon: TAB_ICONS.history,
        view: TAB_VIEWS.history,
        route: { kind: "history", tab: "chats" },
        owns: subject.owns,
        ...parentOf(subject),
        onShow: () => {
          lazily(
            import("./history.js").then(({ loadHistoryView }) => {
              loadHistoryView();
            }),
          );
        },
        refresh: () => {
          lazily(
            import("./history.js").then(({ refreshHistoryView }) => {
              refreshHistoryView();
            }),
          );
        },
        // Unlike docs, this page needs a close hook: it holds a dispatch, an
        // AbortController and a debounce timer.
        onClose: () => {
          lazily(
            import("./history.js").then(({ teardownHistoryView }) => {
              teardownHistoryView();
            }),
          );
        },
      };
    case "docs":
      return {
        name: "Kiro docs",
        icon: TAB_ICONS.docs,
        view: TAB_VIEWS.docs,
        route: { kind: "docs", tab: "steering" },
        owns: subject.owns,
        ...parentOf(subject),
        onShow: () => {
          lazily(
            import("./docs.js").then(({ showDocsTab }) => {
              showDocsTab();
            }),
          );
        },
        refresh: () => {
          lazily(
            import("./docs.js").then(({ refreshDocsView }) => {
              refreshDocsView();
            }),
          );
        },
      };
  }
}

// --- The inverse ---

/** The subject a URL route names, the inverse of each case's `route`, total with no default, so a
 *  new kind is one compile error for both directions. A singleton's sub-position is DROPPED; a
 *  FILES ref is the ORIGIN a route mints (`filesTabForRoute` addresses open browsers). */
export function subjectForRoute(route: Route): { kind: TabKind; ref: string } {
  switch (route.kind) {
    case "chat":
      return { kind: "chat", ref: route.id };
    // The one case where the two vocabularies differ: the route kind is `file`
    // and the tab kind is `editor`.
    case "file":
      return { kind: "editor", ref: route.path };
    case "run":
      return { kind: "run", ref: route.id };
    case "subagent":
      return { kind: "subagent", ref: subagentRef(route.chat, route.id) };
    case "spec":
      return { kind: "spec", ref: route.dir };
    case "settings":
      return { kind: "settings", ref: "" };
    case "git":
      return { kind: "git", ref: "" };
    case "files":
      return { kind: "files", ref: route.path };
    case "history":
      return { kind: "history", ref: "" };
    case "docs":
      return { kind: "docs", ref: "" };
    case "web":
      return { kind: "web", ref: route.path };
  }
}
