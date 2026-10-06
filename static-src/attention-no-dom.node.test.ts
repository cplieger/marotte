// The out-of-page attention system's DECISIONS (fold, orders, acknowledgement store,
// sinks, raise rule), in the `node` project with no DOM: if a decision starts reading
// `document` this file stops loading, and "the premise" below asserts that. The browser
// binding is attention-wiring.test.ts's. Pinned divergences from web-terminal-ui: the
// severity order, the icon mapping, chat-only candidates, and the two-halved raise rule.

import { readFileSync } from "node:fs";
import { join } from "node:path";
import ts from "typescript";
import { describe, it, expect, vi } from "vitest";

// The four modules the WIRING half imports (they pull in a DOM), stubbed: this file
// loading proves every decision takes its capabilities through an injected env.
vi.mock("./store.js", () => ({ getActiveId: (): string => "" }));
vi.mock("./tabs.js", () => ({
  cueCandidates: (): [] => [],
  subscribeTabCues: (): (() => void) => (): void => undefined,
  setOnTabClosed: (): void => undefined,
  // Present-but-inert so real-ESM linking succeeds; no case calls them.
  get: vi.fn(() => undefined),
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));
vi.mock("./bus.js", () => ({
  BUS_TAB_CHANGED: "tabs:changed",
  onBus: (): (() => void) => (): void => undefined,
}));
vi.mock("./dom.js", () => ({ $: {} }));

import {
  CUE_SEVERITY,
  CUE_SEEN_KEY,
  MAX_PERSISTED_CUE_SEEN,
  NO_ATTENTION,
  createAttention,
  createAttentionController,
  createCueSeen,
  cueIconName,
  iconVariantHref,
  isCueStatus,
  isUnseenCue,
  parseCueSeen,
  serializeCueSeen,
  summarize,
  titlePrefixFor,
  worseCue,
  type Attention,
  type AttentionEnv,
  type CueCandidate,
  type CueSeenStorage,
  type CueStatus,
} from "./attention.js";

function seen(entries: Record<string, CueStatus> = {}): Map<string, CueStatus> {
  return new Map(Object.entries(entries));
}

// 0. The premise. PLACEMENT is the realm: no `document` or `window` in node, so a decision
// reading either at load stops this file loading. The scan is THE INVARIANT: Node has a
// real `navigator`, so the realm cannot rule it out; the scan asserts attention.ts's module
// scope references nothing outside itself and executes nothing. The last case plants a
// read and watches the scan report it.

/** The decision module's path, resolved the way every fixture read in this
 *  package is (actions/lint.node.test.ts), so it survives a runner that moves
 *  process.cwd(). */
const DECISIONS = join(import.meta.dirname, "attention.ts");

/**
 * What attention.ts reads from OUTSIDE itself on import, and what it EXECUTES. `globals` is
 * every free identifier in import-time code, with no list of global names. `runs` is every
 * import-time call, `new`, `await`, tagged template, class or enum, catching the INDIRECT
 * read. `function` declarations and type positions are in neither.
 */
function moduleScopeEffects(source: string): { globals: string[]; runs: string[] } {
  const sf = ts.createSourceFile(
    "attention.ts",
    source,
    ts.ScriptTarget.ESNext,
    true,
    ts.ScriptKind.TS,
  );
  const declared = new Set<string>();
  const globals: string[] = [];
  const runs: string[] = [];
  const at = (node: ts.Node): string =>
    `line ${String(sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1)}`;
  const head = (node: ts.Node): string => `${node.getText(sf).split("\n")[0] ?? ""} (${at(node)})`;

  // Every name the module binds itself. A reference to one of these is not a
  // read from outside, whatever it is called.
  for (const st of sf.statements) {
    if (ts.isImportDeclaration(st)) {
      const clause = st.importClause;
      if (clause === undefined) {
        continue;
      }
      if (clause.name !== undefined) {
        declared.add(clause.name.text);
      }
      const bound = clause.namedBindings;
      if (bound === undefined) {
        continue;
      }
      if (ts.isNamespaceImport(bound)) {
        declared.add(bound.name.text);
      } else {
        for (const spec of bound.elements) {
          declared.add(spec.name.text);
        }
      }
    } else if (ts.isVariableStatement(st)) {
      for (const decl of st.declarationList.declarations) {
        if (ts.isIdentifier(decl.name)) {
          declared.add(decl.name.text);
        }
      }
    } else if (
      ts.isFunctionDeclaration(st) ||
      ts.isClassDeclaration(st) ||
      ts.isEnumDeclaration(st)
    ) {
      if (st.name !== undefined) {
        declared.add(st.name.text);
      }
    }
  }

  const walk = (node: ts.Node): void => {
    if (ts.isTypeNode(node) || ts.isTypeParameterDeclaration(node)) {
      return;
    }
    // A function VALUE is not invoked by being written down.
    if (ts.isFunctionExpression(node) || ts.isArrowFunction(node) || ts.isMethodDeclaration(node)) {
      return;
    }
    // A class or enum body runs code this walk does not model, so it is reported. attention.ts
    // has neither.
    if (ts.isClassDeclaration(node) || ts.isClassExpression(node) || ts.isEnumDeclaration(node)) {
      runs.push(head(node));
      return;
    }
    if (ts.isIdentifier(node)) {
      if (!declared.has(node.text)) {
        globals.push(`${node.text} (${at(node)})`);
      }
      return;
    }
    // `a.b` reads `a`; `b` is a property name, not a binding.
    if (ts.isPropertyAccessExpression(node) || ts.isElementAccessExpression(node)) {
      walk(node.expression);
      return;
    }
    if (ts.isPropertyAssignment(node)) {
      if (ts.isComputedPropertyName(node.name)) {
        walk(node.name.expression);
      }
      walk(node.initializer);
      return;
    }
    if (ts.isShorthandPropertyAssignment(node)) {
      if (!declared.has(node.name.text)) {
        globals.push(`${node.name.text} (${at(node)})`);
      }
      return;
    }
    // Recorded AND descended into, so `localStorage.getItem(k)` reports the call
    // and the global rather than one of the two.
    if (
      ts.isCallExpression(node) ||
      ts.isNewExpression(node) ||
      ts.isTaggedTemplateExpression(node) ||
      ts.isAwaitExpression(node)
    ) {
      runs.push(head(node));
    }
    node.forEachChild(walk);
  };

  for (const st of sf.statements) {
    // Erased or declaration-only: nothing here runs at import.
    if (
      ts.isImportDeclaration(st) ||
      ts.isInterfaceDeclaration(st) ||
      ts.isTypeAliasDeclaration(st) ||
      ts.isFunctionDeclaration(st) ||
      ts.isModuleDeclaration(st) ||
      ts.isExportDeclaration(st)
    ) {
      continue;
    }
    if (ts.isVariableStatement(st)) {
      for (const decl of st.declarationList.declarations) {
        if (decl.initializer !== undefined) {
          walk(decl.initializer);
        }
      }
      continue;
    }
    walk(st);
  }
  return { globals, runs };
}

describe("the premise: attention.ts reads nothing from outside itself at load", () => {
  it("runs in the node project, where document and window do not exist", () => {
    // Placement, not the invariant. Its job is to make the four mocks above
    // load-bearing and to keep the browser sibling's half of the split true.
    expect(typeof document, "a DOM here makes the four mocks above decoration").toBe("undefined");
    expect(typeof window).toBe("undefined");
  });

  it("loaded attention.js anyway", () => {
    // The import is at the top of the file, static, the shape production uses.
    // Reaching this line means the module evaluated.
    expect(typeof createAttention).toBe("function");
    expect(typeof createAttentionController).toBe("function");
  });

  it("references no identifier from outside the module in code that runs at import", () => {
    const { globals } = moduleScopeEffects(readFileSync(DECISIONS, "utf8"));
    expect(globals, "a decision now reads a global at load; move it into the binding").toEqual([]);
  });

  it("executes nothing at import", () => {
    // A module-scope call can read a global through a callee this scan cannot see.
    const { runs } = moduleScopeEffects(readFileSync(DECISIONS, "utf8"));
    expect(runs, "module scope must be imports, literals and declarations").toEqual([]);
  });

  it("would report a module-scope global read, so the two empty lists mean something", () => {
    // Guard the guard: a buggy scan returns [] forever. `navigator` is the global the realm
    // cannot rule out.
    const planted = `${readFileSync(DECISIONS, "utf8")}\nconst planted = navigator.userAgent;\n`;
    const { globals } = moduleScopeEffects(planted);
    expect(globals).toHaveLength(1);
    expect(globals[0]).toContain("navigator");
  });
});

describe("the cue set", () => {
  it("holds exactly the four states that want the reader", () => {
    // Of marotte's dot vocabulary only these four are things to tell someone about.
    expect([...CUE_SEVERITY]).toEqual(["input", "failed", "waiting", "done"]);
  });

  it("refuses every non-cue dot state, the editor's mark included", () => {
    for (const status of ["idle", "working", "dirty", "", "crashed", "INPUT"]) {
      expect(isCueStatus(status), `${status} must not be a cue`).toBe(false);
    }
    for (const status of CUE_SEVERITY) {
      expect(isCueStatus(status)).toBe(true);
    }
  });

  it("ranks a pending ask above a parked failure, unlike the reference", () => {
    // THE divergence from web-terminal-ui, matching tabStatusFor (see CUE_SEVERITY).
    expect(worseCue("input", "failed")).toBe("input");
    expect(worseCue("failed", "input")).toBe("input");
  });

  it("ranks the whole order transitively", () => {
    expect(worseCue("failed", "waiting")).toBe("failed");
    expect(worseCue("waiting", "done")).toBe("waiting");
    expect(worseCue("input", "done")).toBe("input");
    expect(worseCue("done", "waiting")).toBe("waiting");
  });

  it("treats no cue as the identity, so an empty fold has no worst", () => {
    expect(worseCue("", "done")).toBe("done");
    expect(worseCue("done", "")).toBe("done");
    expect(worseCue("", "")).toBe("");
  });
});

describe("the icon mapping", () => {
  it("paints `waiting` with the `input` asset", () => {
    // The favicon badge cannot carry the dot's fill-and-ring distinction (see CUE_ICON).
    expect(cueIconName("waiting")).toBe("input");
    expect(cueIconName("input")).toBe("input");
  });

  it("paints a failure with `alert` and a finished turn with `done`", () => {
    expect(cueIconName("failed")).toBe("alert");
    expect(cueIconName("done")).toBe("done");
  });

  it("names only assets that ship, so no cue can 404 the tab icon", () => {
    // The three shipped variants, guarded by favicon-variants.test.ts; a fourth would blank
    // the icon silently.
    const shipped = new Set(["input", "done", "alert"]);
    for (const cue of CUE_SEVERITY) {
      expect(shipped.has(cueIconName(cue)), `${cue} names a missing asset`).toBe(true);
    }
  });
});

describe("summarize folds the chat tabs into one value", () => {
  it("counts nothing when nothing is latched", () => {
    expect(
      summarize(
        [
          { id: "a", status: "idle" },
          { id: "b", status: "working" },
        ],
        seen(),
      ),
    ).toEqual(NO_ATTENTION);
  });

  it("counts one per chat holding an unacknowledged cue", () => {
    expect(
      summarize(
        [
          { id: "a", status: "done" },
          { id: "b", status: "waiting" },
          { id: "c", status: "working" },
        ],
        seen(),
      ),
    ).toEqual({ count: 2, worst: "waiting" });
  });

  it("reports the most severe cue as the worst, not the last one seen", () => {
    expect(
      summarize(
        [
          { id: "a", status: "done" },
          { id: "b", status: "input" },
          { id: "c", status: "failed" },
        ],
        seen(),
      ),
    ).toEqual({ count: 3, worst: "input" });
  });

  it("drops a chat whose current cue this reader already acknowledged", () => {
    expect(
      summarize(
        [
          { id: "a", status: "done" },
          { id: "b", status: "failed" },
        ],
        seen({ a: "done" }),
      ),
    ).toEqual({ count: 1, worst: "failed" });
  });

  it("re-raises a chat whose cue MOVED to another cue", () => {
    // An acknowledgement is of a state, not of a chat: a chat that finished, was
    // acknowledged, then failed is news again.
    expect(summarize([{ id: "a", status: "failed" }], seen({ a: "done" }))).toEqual({
      count: 1,
      worst: "failed",
    });
  });

  it("ignores an acknowledgement for a chat that is no longer latched", () => {
    expect(summarize([{ id: "a", status: "working" }], seen({ a: "done" }))).toEqual(NO_ATTENTION);
  });

  it("never counts the editor's dirty mark, whatever reaches it", () => {
    // Belt for tabs.ts cueCandidates: `dirty` is the one non-chat state that could arrive.
    expect(summarize([{ id: "editor:/a.go", status: "dirty" }], seen())).toEqual(NO_ATTENTION);
  });

  it("folds a RUN tab's cue into the count and the worst pick", () => {
    // A run tab is one more candidate speaking the same vocabulary (store.ts `runStatusFor`):
    // it counts and can win the severity pick.
    expect(
      summarize(
        [
          { id: "tab-chat", status: "done" },
          { id: "tab-run", status: "failed" },
        ],
        seen(),
      ),
    ).toEqual({ count: 2, worst: "failed" });
  });

  it("counts one per chat and never twice for the same id", () => {
    // Set-valued: the fold must not reintroduce a duplicate by summing per status.
    expect(summarize([{ id: "a", status: "done" }], seen()).count).toBe(1);
  });

  // What a FRESH DEVICE opens at: the `done` and `failed` latches seed from the chat header
  // (store.ts `latchFieldsFor`), so the first-load count is the open chat tabs holding a
  // finished or failed turn. Measured here rather than argued.
  it("opens at one per finished tab on a device with no acknowledgements", () => {
    // Eight open chat tabs: six `completed`, two `interrupted` (graded `failed`). A stopped
    // turn with a tab counts too: `done` is the dot for any turn that ended.
    const tabs = [
      { id: "t1", status: "done" },
      { id: "t2", status: "done" },
      { id: "t3", status: "done" },
      { id: "t4", status: "done" },
      { id: "t5", status: "done" },
      { id: "t6", status: "done" },
      { id: "t7", status: "failed" },
      { id: "t8", status: "failed" },
    ];

    // The active tab is acknowledged as it is observed (the controller's
    // watched-tab rule), so the reader sees one fewer than the tab count.
    expect(summarize(tabs, seen())).toEqual({ count: 8, worst: "failed" });
    expect(summarize(tabs, seen({ t1: "done" }))).toEqual({ count: 7, worst: "failed" });
  });

  it("takes the ALERT icon as soon as one tab holds a failure", () => {
    // `failed` outranks `done`, so one broken turn decides the favicon for the whole window.
    expect(cueIconName(summarize([{ id: "a", status: "done" }], seen()).worst as CueStatus)).toBe(
      "done",
    );
    expect(
      cueIconName(
        summarize(
          [
            { id: "a", status: "done" },
            { id: "b", status: "failed" },
          ],
          seen(),
        ).worst as CueStatus,
      ),
    ).toBe("alert");
  });

  it("clears as the reader visits each tab, so the count is transient", () => {
    // Acknowledgements persist per device, so the first-load count only falls.
    const tabs = [
      { id: "a", status: "done" },
      { id: "b", status: "done" },
      { id: "c", status: "failed" },
    ];
    expect(summarize(tabs, seen()).count).toBe(3);
    expect(summarize(tabs, seen({ a: "done" })).count).toBe(2);
    expect(summarize(tabs, seen({ a: "done", b: "done" })).count).toBe(1);
    expect(summarize(tabs, seen({ a: "done", b: "done", c: "failed" }))).toEqual(NO_ATTENTION);
  });

  it("counts a chat whose dot has never been written as nothing", () => {
    // `""` (an unpainted dot, an unknown chat) is not a cue, so a boot restore cannot spike
    // the count.
    expect(
      summarize(
        [
          { id: "a", status: "" },
          { id: "b", status: "" },
        ],
        seen(),
      ),
    ).toEqual(NO_ATTENTION);
  });
});

describe("isUnseenCue is the one predicate behind both surfaces", () => {
  it("agrees with the fold on every case", () => {
    const candidates: CueCandidate[] = [
      { id: "a", status: "done" },
      { id: "b", status: "idle" },
      { id: "c", status: "input" },
    ];
    const ack = seen({ a: "done" });
    const byPredicate = candidates.filter((c) => isUnseenCue(c.status, c.id, ack)).length;
    expect(summarize(candidates, ack).count).toBe(byPredicate);
  });
});

describe("the title format", () => {
  it("puts the count FIRST, because a tab strip truncates the tail", () => {
    expect(titlePrefixFor(3)).toBe("(3) ");
  });

  it("writes no prefix at zero, so the bookmark name stays clean", () => {
    expect(titlePrefixFor(0)).toBe("");
  });
});

interface SinkLog {
  titles: string[];
  badges: number[];
  icons: (string | null)[];
}

function fakeEnv(log: SinkLog, opts: { badge?: boolean; icon?: boolean } = {}): AttentionEnv {
  const env: AttentionEnv = {
    titlePrefix: (text) => log.titles.push(text),
  };
  if (opts.badge !== false) {
    env.setBadge = (count) => log.badges.push(count);
  }
  if (opts.icon !== false) {
    env.setIcon = (variant) => log.icons.push(variant);
  }
  return env;
}

function emptyLog(): SinkLog {
  return { titles: [], badges: [], icons: [] };
}

describe("createAttention drives each sink only on a real change", () => {
  it("applies everything on the first call, whatever the value", () => {
    const log = emptyLog();
    createAttention(fakeEnv(log)).apply(NO_ATTENTION);
    expect(log).toEqual({ titles: [""], badges: [0], icons: [null] });
  });

  it("touches nothing when the same value is applied again", () => {
    // Idempotence, not a debounce: a sweep re-deriving the same answer must be silent.
    const log = emptyLog();
    const surfaces = createAttention(fakeEnv(log));
    const value: Attention = { count: 2, worst: "done" };
    surfaces.apply(value);
    surfaces.apply({ ...value });
    expect(log).toEqual({ titles: ["(2) "], badges: [2], icons: ["done"] });
  });

  it("moves the title and badge on a count change without touching the icon", () => {
    const log = emptyLog();
    const surfaces = createAttention(fakeEnv(log));
    surfaces.apply({ count: 1, worst: "done" });
    surfaces.apply({ count: 2, worst: "done" });
    expect(log.titles).toEqual(["(1) ", "(2) "]);
    expect(log.badges).toEqual([1, 2]);
    expect(log.icons).toEqual(["done"]);
  });

  it("moves the icon on a worst change without touching the title or badge", () => {
    const log = emptyLog();
    const surfaces = createAttention(fakeEnv(log));
    surfaces.apply({ count: 1, worst: "done" });
    surfaces.apply({ count: 1, worst: "input" });
    expect(log.titles).toEqual(["(1) "]);
    expect(log.badges).toEqual([1]);
    expect(log.icons).toEqual(["done", "input"]);
  });

  it("hands the badge the SAME number as the title", () => {
    // Two surfaces disagreeing about how many things want you is worse than
    // either being absent, which is why both read one fold.
    const log = emptyLog();
    const surfaces = createAttention(fakeEnv(log));
    for (const count of [1, 4, 0, 7]) {
      surfaces.apply({ count, worst: count > 0 ? "done" : "" });
    }
    expect(log.badges).toEqual([1, 4, 0, 7]);
    expect(log.titles).toEqual(["(1) ", "(4) ", "", "(7) "]);
  });

  it("clears the icon with null rather than a variant name", () => {
    const log = emptyLog();
    const surfaces = createAttention(fakeEnv(log));
    surfaces.apply({ count: 1, worst: "failed" });
    surfaces.apply(NO_ATTENTION);
    expect(log.icons).toEqual(["alert", null]);
  });

  it("still writes the title when the badge and icon are both absent", () => {
    // The anti-ladder rule (see AttentionEnv): the title is gated on nothing.
    const log = emptyLog();
    createAttention(fakeEnv(log, { badge: false, icon: false })).apply({ count: 3, worst: "done" });
    expect(log.titles).toEqual(["(3) "]);
    expect(log.badges).toEqual([]);
    expect(log.icons).toEqual([]);
  });
});

describe("iconVariantHref follows the asset generator's naming", () => {
  it("rewrites marotte's one icon link", () => {
    expect(iconVariantHref("/favicon.svg", "input")).toBe("/favicon-input.svg");
    expect(iconVariantHref("/favicon.svg", "alert")).toBe("/favicon-alert.svg");
  });

  it("preserves a size suffix and the extension, so a link keeps its format", () => {
    expect(iconVariantHref("/favicon-32x32.png", "done")).toBe("/favicon-done-32x32.png");
  });

  it("leaves a link whose name is not favicon alone rather than 404ing it", () => {
    expect(iconVariantHref("/apple-touch-icon.png", "input")).toBeNull();
    expect(iconVariantHref("/logo.svg", "input")).toBeNull();
  });

  it("matches only a whole path segment named favicon", () => {
    expect(iconVariantHref("/assets/favicon.svg", "done")).toBe("/assets/favicon-done.svg");
    expect(iconVariantHref("/myfavicon.svg", "done")).toBeNull();
  });
});

describe("the acknowledgement store's key", () => {
  it("lives beside the UI state rather than inside it", () => {
    // Its own key: `marotte.ui-state` is the window's arrangement, on another cadence.
    expect(CUE_SEEN_KEY).toBe("marotte.cue-seen");
    expect(CUE_SEEN_KEY).not.toBe("marotte.ui-state");
  });
});

describe("parseCueSeen distrusts everything it reads", () => {
  it("round-trips what serializeCueSeen wrote", () => {
    const map = seen({ a: "done", b: "input" });
    expect([...parseCueSeen(serializeCueSeen(map))]).toEqual([...map]);
  });

  it("reads an absent or empty document as nothing acknowledged", () => {
    expect(parseCueSeen(null).size).toBe(0);
    expect(parseCueSeen("").size).toBe(0);
  });

  it("drops a whole document it cannot parse", () => {
    // Degrading to "nothing acknowledged" is always safe: a lost
    // acknowledgement only re-lights a cue the reader can dismiss again.
    expect(parseCueSeen("{oh no").size).toBe(0);
  });

  it("refuses a JSON value that is not an object of entries", () => {
    // A POPULATED array matters: Object.entries on ["done"] yields ["0", "done"], a cue for a
    // chat called "0".
    for (const raw of ["[]", '["done"]', '["done","input"]', "null", '"done"', "42", "true"]) {
      expect(parseCueSeen(raw).size, `${raw} must not parse as a cue map`).toBe(0);
    }
  });

  it("keeps the trustworthy entries of a partly-corrupt document", () => {
    const map = parseCueSeen('{"a":"done","b":"nonsense","":"input","c":7,"d":"failed"}');
    expect([...map]).toEqual([
      ["a", "done"],
      ["d", "failed"],
    ]);
  });

  it("stops at the cap, so a hostile document cannot make the restore unbounded", () => {
    const hostile: Record<string, string> = {};
    for (let i = 0; i < MAX_PERSISTED_CUE_SEEN * 3; i++) {
      hostile[`c${String(i)}`] = "done";
    }
    expect(parseCueSeen(JSON.stringify(hostile)).size).toBe(MAX_PERSISTED_CUE_SEEN);
  });
});

function fakeStorage(initial: string | null = null): CueSeenStorage & { raw: () => string | null } {
  let raw = initial;
  return {
    read: () => raw,
    write: (next) => {
      raw = next;
    },
    raw: () => raw,
  };
}

describe("createCueSeen persists and bounds the acknowledgements", () => {
  it("starts from what the reader already dismissed", () => {
    // Latches rebuild from server state on each reconnect, so a dismissed count must persist.
    const store = createCueSeen(fakeStorage('{"a":"done"}'));
    expect(store.map().get("a")).toBe("done");
  });

  it("writes through on every mark", () => {
    const storage = fakeStorage();
    createCueSeen(storage).mark("a", "input");
    expect(storage.raw()).toBe('{"a":"input"}');
  });

  it("ignores a non-cue status rather than storing it", () => {
    const storage = fakeStorage();
    const store = createCueSeen(storage);
    store.mark("a", "working");
    store.mark("editor:/x.go", "dirty");
    expect(store.map().size).toBe(0);
    expect(storage.raw()).toBeNull();
  });

  it("does not re-write an acknowledgement it already holds", () => {
    const storage = fakeStorage();
    const store = createCueSeen(storage);
    store.mark("a", "done");
    const after = storage.raw();
    store.mark("a", "done");
    expect(storage.raw()).toBe(after);
  });

  it("evicts oldest-first so the live map obeys the parser's cap", () => {
    // A chat that vanished while the page was closed leaves an entry nothing prunes; unbounded,
    // the parser would drop fresh acknowledgements.
    const store = createCueSeen(fakeStorage());
    for (let i = 0; i <= MAX_PERSISTED_CUE_SEEN; i++) {
      store.mark(`c${String(i)}`, "done");
    }
    expect(store.map().size).toBe(MAX_PERSISTED_CUE_SEEN);
    expect(store.map().has("c0")).toBe(false);
    expect(store.map().has(`c${String(MAX_PERSISTED_CUE_SEEN)}`)).toBe(true);
  });

  it("forgets an entry and writes that through", () => {
    const storage = fakeStorage('{"a":"done"}');
    const store = createCueSeen(storage);
    store.forget("a");
    expect(store.map().size).toBe(0);
    expect(storage.raw()).toBe("{}");
  });

  it("does not write when there was nothing to forget", () => {
    const storage = fakeStorage();
    createCueSeen(storage).forget("nobody");
    expect(storage.raw()).toBeNull();
  });
});

interface Harness {
  applied: Attention[];
  latest: () => Attention;
  setCandidates: (next: CueCandidate[]) => void;
  setActive: (id: string) => void;
  setVisible: (v: boolean) => void;
  setRowsInView: (ids: string[]) => void;
  stored: () => string | null;
  controller: ReturnType<typeof createAttentionController>;
}

function harness(opts: { stored?: string } = {}): Harness {
  let candidates: CueCandidate[] = [];
  let active = "";
  let visible = true;
  let rows: string[] = [];
  const storage = fakeStorage(opts.stored ?? null);
  const applied: Attention[] = [];
  const controller = createAttentionController({
    candidates: () => candidates,
    activeTabID: () => active,
    pageVisible: () => visible,
    rowsInView: () => rows,
    storage,
    surfaces: {
      apply: (next) => applied.push(next),
    },
  });
  return {
    applied,
    latest: () => applied[applied.length - 1] ?? NO_ATTENTION,
    setCandidates: (next) => {
      candidates = next;
    },
    setActive: (id) => {
      active = id;
    },
    setVisible: (v) => {
      visible = v;
    },
    setRowsInView: (ids) => {
      rows = ids;
    },
    stored: storage.raw,
    controller,
  };
}

describe("the raise rule", () => {
  it("raises a cue on a background chat", () => {
    const h = harness();
    h.setActive("a");
    h.setCandidates([
      { id: "a", status: "idle" },
      { id: "b", status: "done" },
    ]);
    h.controller.refresh();
    expect(h.latest()).toEqual({ count: 1, worst: "done" });
  });

  it("raises nothing for the chat the reader is watching", () => {
    const h = harness();
    h.setActive("a");
    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.refresh();
    expect(h.latest()).toEqual(NO_ATTENTION);
  });

  it("RAISES the active chat's cue while the page is hidden", () => {
    // Both halves of "watched": "active" alone swallows the single running chat's cue.
    const h = harness();
    h.setActive("a");
    h.setVisible(false);
    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.refresh();
    expect(h.latest()).toEqual({ count: 1, worst: "done" });
  });

  it("acknowledges the active chat's cue once the reader can see it again", () => {
    const h = harness();
    h.setActive("a");
    h.setVisible(false);
    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.refresh();
    expect(h.latest().count).toBe(1);

    h.setVisible(true);
    h.controller.refresh();
    expect(h.latest()).toEqual(NO_ATTENTION);
  });

  it("keeps an acknowledgement across a hide, so it does not re-raise", () => {
    const h = harness();
    h.setActive("a");
    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.refresh();
    h.setVisible(false);
    h.controller.refresh();
    expect(h.latest()).toEqual(NO_ATTENTION);
  });

  it("un-acknowledges a chat whose state moved off the cue", () => {
    // So its NEXT cue is fresh. Without this, a chat that finished, was
    // acknowledged, then started and finished another turn would stay silent.
    const h = harness();
    h.setActive("a");
    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.refresh();
    expect(h.stored()).toBe('{"a":"done"}');

    h.setCandidates([{ id: "a", status: "working" }]);
    h.controller.refresh();
    expect(h.stored()).toBe("{}");

    h.setActive("");
    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.refresh();
    expect(h.latest()).toEqual({ count: 1, worst: "done" });
  });

  it("treats an unpainted tab as no information, not as a cleared cue", () => {
    // Reading an unpainted dot or an unknown chat as "the cue ended" dropped the
    // acknowledgement on every reload.
    const h = harness({ stored: '{"a":"done"}' });
    h.setCandidates([{ id: "a", status: "" }]);
    h.controller.refresh();
    expect(h.stored()).toBe('{"a":"done"}');

    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.refresh();
    expect(h.latest()).toEqual(NO_ATTENTION);
  });

  it("does un-acknowledge on a real non-cue state", () => {
    // The contrast with the case above: `idle` and `working` are states the chat
    // is genuinely in, and they mean the cue is over.
    const h = harness({ stored: '{"a":"done"}' });
    h.setCandidates([{ id: "a", status: "idle" }]);
    h.controller.refresh();
    expect(h.stored()).toBe("{}");
  });

  it("reports zero once the last latched chat closes", () => {
    const h = harness();
    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.refresh();
    expect(h.latest().count).toBe(1);

    h.setCandidates([]);
    h.controller.refresh();
    expect(h.latest()).toEqual(NO_ATTENTION);
  });

  it("drops a departed chat's acknowledgement", () => {
    const h = harness();
    h.setActive("a");
    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.refresh();
    h.setCandidates([]);
    h.controller.forget("a");
    expect(h.stored()).toBe("{}");
  });
});

describe("acknowledging what the reader can see", () => {
  it("acknowledges every chat whose row is in view", () => {
    const h = harness();
    h.setActive("");
    h.setCandidates([
      { id: "a", status: "done" },
      { id: "b", status: "input" },
    ]);
    h.setRowsInView(["a", "b"]);
    h.controller.ackSeen();
    expect(h.latest()).toEqual(NO_ATTENTION);
  });

  it("KEEPS the cue of a chat scrolled out of the list", () => {
    // The forgotten chat is likely below the fold, so becoming visible must not clear wholesale.
    const h = harness();
    h.setActive("");
    h.setCandidates([
      { id: "a", status: "done" },
      { id: "b", status: "input" },
    ]);
    h.setRowsInView(["a"]);
    h.controller.ackSeen();
    expect(h.latest()).toEqual({ count: 1, worst: "input" });
  });

  it("acknowledges the active chat even when no row is in view", () => {
    // Mobile with the drawer closed: no sidebar row is visible, but the active
    // chat's transcript is what fills the screen.
    const h = harness();
    h.setActive("a");
    h.setCandidates([
      { id: "a", status: "done" },
      { id: "b", status: "done" },
    ]);
    h.setRowsInView([]);
    h.controller.ackSeen();
    expect(h.latest()).toEqual({ count: 1, worst: "done" });
  });

  it("acknowledges nothing at all while the page is hidden", () => {
    // A closed drawer reports no rows, but so would a page nobody is looking at;
    // acknowledging there would blank every cue on a background page.
    const h = harness();
    h.setActive("a");
    h.setVisible(false);
    h.setCandidates([{ id: "a", status: "done" }]);
    h.setRowsInView(["a"]);
    h.controller.ackSeen();
    expect(h.stored()).toBeNull();
  });

  it("acknowledges a RUN row in view, because the seen map is keyed by TAB id", () => {
    // Every id is a tab id (`data-tab-id`), so a run row acknowledges like a chat row; the chat
    // beside it keeps its cue.
    const h = harness();
    h.setActive("");
    h.setCandidates([
      { id: "tab-chat", status: "done" },
      { id: "tab-run", status: "failed" },
    ]);
    h.setRowsInView(["tab-run"]);
    h.controller.ackSeen();
    expect(h.latest()).toEqual({ count: 1, worst: "done" });
    expect(h.stored()).toBe('{"tab-run":"failed"}');
  });

  it("ignores an in-view row holding no cue", () => {
    const h = harness();
    h.setCandidates([{ id: "a", status: "working" }]);
    h.setRowsInView(["a"]);
    h.controller.ackSeen();
    expect(h.stored()).toBeNull();
  });
});

describe("switching to a chat acknowledges it", () => {
  it("acknowledges the incoming chat before the store's active id has moved", () => {
    // The tab store announces a switch from inside its emit, BEFORE store.setActive, so the
    // switch needs its own hook.
    const h = harness();
    h.setActive("a");
    h.setCandidates([
      { id: "a", status: "idle" },
      { id: "b", status: "done" },
    ]);
    h.controller.ackSwitch("b");
    expect(h.latest()).toEqual(NO_ATTENTION);
    expect(h.stored()).toBe('{"b":"done"}');
  });

  it("does not acknowledge a switch made while the page is hidden", () => {
    // The boot restore activates a tab too, and a page restored into a
    // background browser tab must not have its cue swallowed by that.
    const h = harness();
    h.setVisible(false);
    h.setCandidates([{ id: "b", status: "done" }]);
    h.controller.ackSwitch("b");
    expect(h.latest()).toEqual({ count: 1, worst: "done" });
  });

  it("ignores a switch to a tab that is not a chat", () => {
    const h = harness();
    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.ackSwitch("__settings__");
    expect(h.latest()).toEqual({ count: 1, worst: "done" });
  });
});
