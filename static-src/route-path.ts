// The URL vocabulary, DOM-free so the service worker shares it: `Route`, `parseRoute` and its
// inverse `buildPath`.

// Forge accounts live on the git view's Sources tab, so there is no "git" settings tab;
// /settings/git canonicalizes to General via parseSettingsTab's default branch.
export type SettingsTab = "general" | "tools" | "permissions" | "instructions";

// The git view's three sub-tabs. "changes" is the canonical default (its URL omits the segment:
// /git, not /git/changes), mirroring how SettingsTab's "general" maps to /settings.
export type GitTab = "changes" | "prs" | "sources";

// The configuration browser's sub-tabs. "steering" is the canonical default and its URL omits the
// segment (/docs, not /docs/steering), mirroring SettingsTab's "general" and GitTab's "changes".
export type DocsTab =
  | "steering"
  | "skills"
  | "prompts"
  | "agents"
  | "specs"
  | "hooks"
  | "workflows"
  | "memories"
  | "powers";

// History's two panes. "chats" is the canonical default and its URL omits the segment (/history,
// not /history/chats), mirroring GitTab's "changes".
export type HistoryTab = "chats" | "runs";

interface RouteChat {
  kind: "chat";
  id: string;
  /** The turn this URL asks to land on, by its session-absolute ordinal (`#turn-<n>`). A
   *  fragment, because the tab's identity is `(chat, id)`. */
  turn?: number;
}
interface RouteGit {
  kind: "git";
  tab: GitTab;
  /** The pull request this URL asks to be focused, as the OPAQUE identity `push-subject.ts`
   *  `prIdentity` spells. A fragment rather than a path segment, for `RouteRun.node`'s two
   *  reasons: the identity contains `#`, and the tab's identity is `(kind: git, ref: "")`, which
   *  must not gain a second shape. */
  pr?: string;
}
interface RouteFiles {
  kind: "files";
  path: string;
}
interface RouteWeb {
  kind: "web";
  path: string;
}
interface RouteFile {
  kind: "file";
  path: string;
  line?: number;
}
interface RouteHistory {
  kind: "history";
  /** Absent means the canonical Chats pane, so every caller that spells the bare `{kind:
   *  "history"}` still names a real location. */
  tab?: HistoryTab;
}
/** The Kiro configuration browser. */
interface RouteDocs {
  kind: "docs";
  tab: DocsTab;
}
/** A read-only review of one previous workflow run. */
interface RouteRun {
  kind: "run";
  id: string;
  /** The step this URL asks to be focused, as a node PATH. A fragment rather than a path
   *  segment, for two reasons: a node path contains `/` (`wf_1/iter-0/work`), and the tab's
   *  identity is `(kind: run, ref: workflowId)`, which must not gain a second shape —
   *  `subjectForRoute` drops this, so two nodes of one run are one tab. */
  node?: string;
}
/** One SUBAGENT execution, read on its own page. */
interface RouteSubagent {
  kind: "subagent";
  /** The chat whose transcript holds this delegate's blocks. */
  chat: string;
  /** The delegate's `agent_subtask_id`. */
  id: string;
}
/** One Kiro spec's documents, on the spec sub-tab. `dir` is the workspace-relative spec
 *  directory (`.kiro/specs/<name>` or `<repo>/.kiro/specs/<name>`), carried as ONE
 *  percent-encoded segment because `parseRoute` splits on `/` before it decodes, so the encoding
 *  is what keeps a directory one route value. */
interface RouteSpec {
  readonly kind: "spec";
  readonly dir: string;
}
interface RouteSettings {
  kind: "settings";
  tab: SettingsTab;
}

export type Route =
  | RouteChat
  | RouteGit
  | RouteFiles
  | RouteFile
  | RouteHistory
  | RouteDocs
  | RouteRun
  | RouteSubagent
  | RouteSpec
  | RouteSettings
  | RouteWeb;

/** Wrapper around decodeURIComponent that returns the raw input on malformed percent-encoded
 *  sequences instead of throwing. Browsers can navigate to URLs with bare `%` characters (e.g.
 *  pasted from external tools), and popstate fires with the raw pathname. */
function safeDecode(s: string): string {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
}

export function parseRoute(pathname: string, hash: string): Route {
  // Normalise: strip leading/trailing slashes for clean segment splitting.
  const segments = pathname.replace(/^\/+|\/+$/g, "").split("/");
  const head = segments[0] ?? "";

  switch (head) {
    case "git": {
      const tab = parseGitTab(segments[1]);
      // Built conditionally, matching the `run` arm below: exactOptionalPropertyTypes forbids
      // assigning `undefined` to an optional property. Read only on `prs`, so the round trip stays
      // an identity for the two tabs that hold no pull requests.
      const pr = tab === "prs" ? parseHashPR(hash) : undefined;
      return pr === undefined ? { kind: "git", tab } : { kind: "git", tab, pr };
    }

    case "history": {
      // Built conditionally, like the `git` arm's `pr`: the canonical pane omits the field, so
      // `/history` parses to the same object every caller writes.
      const tab = parseHistoryTab(segments[1]);
      return tab === "chats" ? { kind: "history" } : { kind: "history", tab };
    }

    case "docs":
      return { kind: "docs", tab: parseDocsTab(segments[1]) };

    case "run": {
      const id = safeDecode(segments[1] ?? "");
      if (id !== "") {
        // Built conditionally, matching the `file` arm below: exactOptionalPropertyTypes forbids
        // assigning `undefined` to an optional property.
        const node = parseHashNode(hash);
        return node !== undefined ? { kind: "run", id, node } : { kind: "run", id };
      }
      break;
    }

    case "spec": {
      // Exactly one further segment: the directory is one encoded value, so a second segment means
      // the caller wrote the directory unencoded.
      if (segments.length === 2) {
        const dir = safeDecode(segments[1] ?? "");
        if (dir !== "") {
          return { kind: "spec", dir };
        }
      }
      break;
    }

    case "settings":
      return { kind: "settings", tab: parseSettingsTab(segments[1]) };

    case "chat": {
      const id = safeDecode(segments[1] ?? "");
      if (id === "") {
        break;
      }
      // /chat/{id}/subagent/{subtaskId} — a delegate of this conversation, read on its own page.
      if (segments[2] === "subagent") {
        const subtask = safeDecode(segments.slice(3).join("/"));
        if (subtask !== "") {
          return { kind: "subagent", chat: id, id: subtask };
        }
      }
      const turn = parseHashTurn(hash);
      return turn !== undefined ? { kind: "chat", id, turn } : { kind: "chat", id };
    }

    case "files": {
      // "/" rather than ".": ONE container-absolute path space. A literal because the router must
      // not import a feature module; files-path-space.test.ts pins the pair.
      if (segments.length <= 1) {
        return { kind: "files", path: "/" };
      }
      // Leading slashes collapse and exactly ONE goes back, so the canonical `/files/workspace/x`
      // and the legacy `/files//workspace/x` land on one path.
      const raw = safeDecode(segments.slice(1).join("/")).replace(/^\/+/, "");
      return { kind: "files", path: raw === "" || raw === "." ? "/" : `/${raw}` };
    }

    case "web": {
      const raw = safeDecode(segments.slice(1).join("/")).replace(/^\/+/, "");
      if (raw !== "") {
        return { kind: "web", path: `/${raw}` };
      }
      break;
    }

    case "file": {
      if (segments.length <= 1) {
        break;
      } // missing path → fall through to default
      const filePath = safeDecode(segments.slice(1).join("/"));
      if (filePath === "") {
        break;
      }
      const line = parseHashLine(hash);
      return line !== undefined
        ? { kind: "file", path: filePath, line }
        : { kind: "file", path: filePath };
    }
  }

  // Default: chat with no specific ID (last active, or empty state).
  return { kind: "chat", id: "" };
}

// parseSettingsTab normalises an unknown / missing tab segment to "general" so bogus URLs still
// land somewhere useful instead of 404-ing.
function parseSettingsTab(seg: string | undefined): SettingsTab {
  switch (seg) {
    case "tools":
    case "permissions":
    case "instructions":
      return seg;
    default:
      return "general";
  }
}

// parseDocsTab normalises an unknown / missing sub-tab segment to "steering" (the canonical
// default), mirroring parseSettingsTab.
function parseDocsTab(seg: string | undefined): DocsTab {
  switch (seg) {
    case "skills":
    case "prompts":
    case "agents":
    case "specs":
    case "hooks":
    case "workflows":
    case "memories":
    case "powers":
      return seg;
    default:
      return "steering";
  }
}

// parseGitTab normalises an unknown / missing sub-tab segment to "changes" (the canonical default),
// mirroring parseSettingsTab.
function parseGitTab(seg: string | undefined): GitTab {
  switch (seg) {
    case "prs":
    case "sources":
      return seg;
    default:
      return "changes";
  }
}

// parseHistoryTab normalises an unknown / missing segment to "chats" (the canonical default),
// mirroring parseGitTab.
function parseHistoryTab(seg: string | undefined): HistoryTab {
  return seg === "runs" ? "runs" : "chats";
}

function parseHashLine(hash: string): number | undefined {
  const m = /^#L(\d+)/.exec(hash);
  if (m === null) {
    return undefined;
  }
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  const n = parseInt(m[1]!, 10);
  return Number.isFinite(n) && n > 0 ? n : undefined;
}

/** The turn a `/chat/{id}` URL asks to land on, off `#turn-<n>` (`turnAnchorID`). */
function parseHashTurn(hash: string): number | undefined {
  const m = /^#turn-(\d+)$/.exec(hash);
  if (m === null) {
    return undefined;
  }
  const n = Number(m[1]);
  return Number.isSafeInteger(n) && n > 0 ? n : undefined;
}

/** The step a `/run/{id}` URL asks to be focused, off `#node=<path>`. */
function parseHashNode(hash: string): string | undefined {
  const m = /^#node=(.*)$/.exec(hash);
  if (m === null) {
    return undefined;
  }
  const path = safeDecode(m[1] ?? "");
  return path === "" ? undefined : path;
}

/** The pull request a `/git/prs` URL asks to be focused, off `#pr=<identity>`. */
function parseHashPR(hash: string): string | undefined {
  const m = /^#pr=(.*)$/.exec(hash);
  if (m === null) {
    return undefined;
  }
  const identity = safeDecode(m[1] ?? "");
  return identity === "" ? undefined : identity;
}

export function buildPath(route: Route): string {
  switch (route.kind) {
    case "chat":
      if (route.id === "") {
        return "/";
      }
      return route.turn !== undefined && route.turn > 0
        ? `/chat/${encodeURIComponent(route.id)}#turn-${String(route.turn)}`
        : `/chat/${encodeURIComponent(route.id)}`;
    case "git":
      // The identity rides as a fragment, on the tab that holds pull requests and only when there
      // is one: the absent case stays byte-identical to what every existing caller produces.
      if (route.tab === "prs" && route.pr !== undefined && route.pr !== "") {
        return `/git/prs#pr=${encodeURIComponent(route.pr)}`;
      }
      // Changes is the canonical default; omit the tab segment.
      return route.tab === "changes" ? "/git" : `/git/${route.tab}`;
    case "history":
      // Chats is the canonical default; omit the tab segment.
      return route.tab === "runs" ? "/history/runs" : "/history";
    case "docs":
      // Steering is the canonical default; omit the tab segment.
      return route.tab === "steering" ? "/docs" : `/docs/${route.tab}`;
    case "run":
      // The node rides as a fragment, and only when there is one: the absent case stays
      // byte-identical to what every existing caller produces.
      return route.node !== undefined && route.node !== ""
        ? `/run/${encodeURIComponent(route.id)}#node=${encodeURIComponent(route.node)}`
        : `/run/${encodeURIComponent(route.id)}`;
    case "subagent":
      return `/chat/${encodeURIComponent(route.chat)}/subagent/${encodeURIComponent(route.id)}`;
    case "spec":
      return `/spec/${encodeURIComponent(route.dir)}`;
    case "files":
      // The canonical form drops the leading slash, so `/workspace/_ui-qa` serialises to
      // `/files/workspace/_ui-qa` and the root listing has no segment of its own. The parser also
      // accepts the legacy `/files//<abs>`, which nothing emits.
      return route.path === "/" || route.path === "." || route.path === ""
        ? "/files"
        : `/files/${encodePath(route.path.replace(/^\/+/, ""))}`;
    case "file":
      return route.line !== undefined && route.line > 0
        ? `/file/${encodePath(route.path)}#L${String(route.line)}`
        : `/file/${encodePath(route.path)}`;
    case "web":
      return `/web/${encodePath(route.path.replace(/^\/+/, ""))}`;
    case "settings":
      // General is the canonical default; omit the tab segment.
      return route.tab === "general" ? "/settings" : `/settings/${route.tab}`;
  }
}

// encodePath URL-encodes each path segment while preserving the separators, so "dir/my notes.md"
// serialises to "dir/my%20notes.md" rather than collapsing to an unreadable blob.
function encodePath(path: string): string {
  return path.split("/").map(encodeURIComponent).join("/");
}
