// The out-of-page attention system's DECISIONS (cue class, fold, orders, sinks, raise rule,
// acknowledgement), in the `node` project with no DOM: if a decision starts reading `document`
// this file stops loading, and "the premise" below asserts that. The browser binding is
// attention-wiring.test.ts's. Pinned divergences from web-terminal-ui: the severity order, the
// icon mapping, chat and run candidates, and seen state held by the server rather than the page.

import { readFileSync } from "node:fs";
import { join } from "node:path";
import ts from "typescript";
import { describe, it, expect, vi } from "vitest";

// The modules the WIRING half imports (they pull in a DOM), stubbed: this file
// loading proves every decision takes its capabilities through an injected env.
vi.mock("./store.js", () => ({
  getActiveId: (): string => "",
  watchSession: (): undefined => undefined,
}));
vi.mock("./tabs.js", () => ({
  cueCandidates: (): [] => [],
  openTabSubjects: (): [] => [],
  subscribeTabCues: (): (() => void) => (): void => undefined,
  // Present-but-inert so real-ESM linking succeeds; no case calls them.
  get: vi.fn(() => undefined),
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));
vi.mock("./bus.js", () => ({
  BUS_PAGE_RESUMED: "transport:resumed",
  BUS_RECONCILE: "transport:reconcile",
  BUS_TAB_CHANGED: "tabs:changed",
  onBus: (): (() => void) => (): void => undefined,
  onSSE: (): (() => void) => (): void => undefined,
}));
vi.mock("./dom.js", () => ({ $: {} }));
vi.mock("./notify.js", () => ({ closeNotificationsFor: (): Promise<void> => Promise.resolve() }));
vi.mock("./actions/tabs.js", () => ({ ackTabCommand: { dispatch: vi.fn() } }));
vi.mock("./run-store.js", () => ({ runState: (): undefined => undefined }));
vi.mock("./transport.js", () => ({ newOpID: (): string => "op" }));

import {
  CUE_SEVERITY,
  createAttention,
  createAttentionController,
  cueClass,
  cueIconName,
  iconVariantHref,
  isCueStatus,
  summarize,
  titlePrefixFor,
  worseCue,
  type AckResult,
  type Attention,
  type AttentionEnv,
  type CueCandidate,
} from "./attention.js";

const NO_ATTENTION: Attention = { count: 0, worst: "" };

/** A candidate; a finished tab whose position is past its seen marker unless stated. */
function tab(id: string, status: string, over: Partial<CueCandidate> = {}): CueCandidate {
  return { id, kind: "chat", status, seen: 0, position: 1, ...over };
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
    // Placement, not the invariant. Its job is to make the mocks above
    // load-bearing and to keep the browser sibling's half of the split true.
    expect(typeof document, "a DOM here makes the mocks above decoration").toBe("undefined");
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

/** Let the badge's one-write-per-turn window close, as the next event's task would. */
async function nextTurn(): Promise<void> {
  await Promise.resolve();
}

describe("assertBadge writes the OS badge whatever the page last wrote", () => {
  it("writes the count again after a write the OS may have dropped", async () => {
    // WebKit resolves setAppBadge before the write lands, so a lost clear leaves the icon lit
    // while every later apply of 0 matches the page's memory.
    const log = emptyLog();
    const surfaces = createAttention(fakeEnv(log));
    surfaces.apply({ count: 1, worst: "done" });
    await nextTurn();
    surfaces.apply(NO_ATTENTION);
    await nextTurn();
    surfaces.assertBadge();
    expect(log.badges).toEqual([1, 0, 0]);
  });

  it("leaves the title and icon to their change gate", async () => {
    const log = emptyLog();
    const surfaces = createAttention(fakeEnv(log));
    surfaces.apply({ count: 2, worst: "input" });
    await nextTurn();
    surfaces.assertBadge();
    expect(log).toEqual({ titles: ["(2) "], badges: [2, 2], icons: ["input"] });
  });

  it("costs one OS write when it follows a count change in the same turn", () => {
    const log = emptyLog();
    const surfaces = createAttention(fakeEnv(log));
    surfaces.apply({ count: 1, worst: "done" });
    surfaces.assertBadge();
    surfaces.assertBadge();
    expect(log.badges).toEqual([1]);
  });

  it("still writes a different count inside the same turn", () => {
    const log = emptyLog();
    const surfaces = createAttention(fakeEnv(log));
    surfaces.apply({ count: 1, worst: "done" });
    surfaces.apply(NO_ATTENTION);
    surfaces.apply({ count: 1, worst: "done" });
    expect(log.badges).toEqual([1, 0, 1]);
  });

  it("writes nothing before the first apply, when there is no count to assert", () => {
    const log = emptyLog();
    createAttention(fakeEnv(log)).assertBadge();
    expect(log.badges).toEqual([]);
  });
});

describe("release hands the page its own title and icon back", () => {
  it("restores the title and icon and leaves the badge alone", () => {
    const log = emptyLog();
    const surfaces = createAttention(fakeEnv(log));
    surfaces.apply({ count: 2, worst: "failed" });
    surfaces.release();
    expect(log).toEqual({ titles: ["(2) ", ""], badges: [2], icons: ["alert", null] });
  });

  it("repaints the title and icon on the next apply of the same value", () => {
    // A bfcache restore re-runs the fold with the value that was showing before the hide.
    const log = emptyLog();
    const surfaces = createAttention(fakeEnv(log));
    surfaces.apply({ count: 2, worst: "failed" });
    surfaces.release();
    surfaces.apply({ count: 2, worst: "failed" });
    expect(log.titles).toEqual(["(2) ", "", "(2) "]);
    expect(log.icons).toEqual(["alert", null, "alert"]);
    expect(log.badges).toEqual([2]);
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

describe("cueClass is the one predicate behind count, icon and acknowledgement", () => {
  const statuses = ["input", "waiting", "failed", "done", "working", "idle", "dirty", ""];
  // Every status by kind, with the position below, equal to and past the seen marker, and unknown.
  const want = (status: string, kind: "chat" | "run", past: boolean): string => {
    if (status === "input" || (kind === "run" && status === "waiting")) {
      return "needs";
    }
    return isCueStatus(status) && past ? "unread" : "";
  };
  for (const kind of ["chat", "run"] as const) {
    for (const status of statuses) {
      for (const [label, seen, position] of [
        ["below", 3, 2],
        ["equal", 3, 3],
        ["past", 3, 4],
        ["unknown", 0, 0],
      ] as const) {
        it(`classes a ${kind} at ${status === "" ? "no dot" : status} with its position ${label}`, () => {
          expect(cueClass(tab("t", status, { kind, seen, position }))).toBe(
            want(status, kind, position > seen),
          );
        });
      }
    }
  }
});

describe("summarize folds the tabs into one value", () => {
  it("counts nothing when nothing needs the reader or is unread", () => {
    expect(summarize([tab("a", "done", { seen: 1 }), tab("b", "working")], "")).toEqual(
      NO_ATTENTION,
    );
  });

  it("counts one per tab and paints the most severe, not the last", () => {
    const got = summarize([tab("a", "done"), tab("b", "input"), tab("c", "failed")], "");
    expect(got).toEqual({ count: 3, worst: "input" });
  });

  it("leaves the watched tab's unread state out and still counts its open ask", () => {
    expect(summarize([tab("a", "done"), tab("b", "done")], "a")).toEqual({
      count: 1,
      worst: "done",
    });
    expect(summarize([tab("a", "input"), tab("b", "done", { seen: 1 })], "a")).toEqual({
      count: 1,
      worst: "input",
    });
  });

  it("counts a paused run until it settles, whatever was acknowledged", () => {
    expect(summarize([tab("r", "waiting", { kind: "run", seen: 9, position: 0 })], "r")).toEqual({
      count: 1,
      worst: "waiting",
    });
  });
});

/** One server's seen markers and read bounds, as several controllers see it: the ack rule is the
 *  server's (clamp to the bound, keep only a value past the stored one). */
interface FakeServer {
  seen: Map<string, number>;
  bound: Map<string, number>;
  acks: { id: string; seen: number }[];
  ack: (id: string, seen: number) => AckResult;
}

function fakeServer(): FakeServer {
  const server: FakeServer = {
    seen: new Map(),
    bound: new Map(),
    acks: [],
    ack(id, seen) {
      server.acks.push({ id, seen });
      const clamped = Math.min(seen, server.bound.get(id) ?? Number.POSITIVE_INFINITY);
      if (clamped > (server.seen.get(id) ?? 0)) {
        server.seen.set(id, clamped);
      }
      return (server.seen.get(id) ?? 0) >= seen ? "acked" : "refused";
    },
  };
  return server;
}

interface Harness {
  applied: Attention[];
  asserts: () => number;
  retracted: string[];
  /** Every acknowledgement this controller sent, in order. */
  sent: { id: string; seen: number }[];
  latest: () => Attention;
  /** Set the tabs this window projects; `seen` is read from the server when one is attached. */
  setCandidates: (next: CueCandidate[]) => void;
  setActive: (id: string) => void;
  setVisible: (v: boolean) => void;
  setRowsInView: (ids: string[]) => void;
  /** What the next sendAck answers when no server is attached. */
  answer: (result: AckResult) => void;
  controller: ReturnType<typeof createAttentionController>;
}

function harness(server?: FakeServer): Harness {
  let candidates: CueCandidate[] = [];
  let active = "";
  let visible = true;
  let rows: string[] = [];
  let next: AckResult = "acked";
  const applied: Attention[] = [];
  const retracted: string[] = [];
  const sent: { id: string; seen: number }[] = [];
  let asserts = 0;
  const view = (): CueCandidate[] =>
    server === undefined
      ? candidates
      : candidates.map((c) => ({ ...c, seen: server.seen.get(c.id) ?? 0 }));
  const controller = createAttentionController({
    candidates: view,
    activeTabID: () => active,
    pageVisible: () => visible,
    rowsInView: () => rows,
    retract: (id) => retracted.push(id),
    sendAck: (id, seen) => {
      sent.push({ id, seen });
      return Promise.resolve(server === undefined ? next : server.ack(id, seen));
    },
    surfaces: {
      apply: (a) => applied.push(a),
      assertBadge: () => {
        asserts += 1;
      },
      release: (): void => undefined,
    },
  });
  return {
    applied,
    asserts: () => asserts,
    retracted,
    sent,
    latest: () => applied[applied.length - 1] ?? NO_ATTENTION,
    setCandidates: (c) => {
      candidates = c;
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
    answer: (result) => {
      next = result;
    },
    controller,
  };
}

/** Let an acknowledgement's answer land. */
async function settled(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

describe("the raise rule", () => {
  it("raises an unread background tab and sends nothing for it", () => {
    const h = harness();
    h.setActive("b");
    h.setCandidates([tab("a", "done")]);
    h.controller.refresh();
    expect(h.latest()).toEqual({ count: 1, worst: "done" });
    expect(h.sent).toEqual([]);
  });

  it("leaves the watched tab out and acknowledges it at its own position", () => {
    const h = harness();
    h.setActive("a");
    h.setCandidates([tab("a", "done", { seen: 2, position: 5 })]);
    h.controller.refresh();
    expect(h.latest()).toEqual(NO_ATTENTION);
    expect(h.sent).toEqual([{ id: "a", seen: 5 }]);
  });

  it("raises the active tab while the page is hidden, and acknowledges nothing", () => {
    const h = harness();
    h.setActive("a");
    h.setVisible(false);
    h.setCandidates([tab("a", "done")]);
    h.controller.refresh();
    expect(h.latest()).toEqual({ count: 1, worst: "done" });
    expect(h.sent).toEqual([]);
  });

  it("never acknowledges an open ask, which counts on screen until answered", () => {
    const h = harness();
    h.setActive("a");
    h.setCandidates([tab("a", "input")]);
    h.controller.refresh();
    expect(h.latest()).toEqual({ count: 1, worst: "input" });
    expect(h.sent).toEqual([]);
  });

  it("acknowledges nothing for a running tab", () => {
    const h = harness();
    h.setActive("a");
    h.setCandidates([tab("a", "working", { position: 4 })]);
    h.controller.refresh();
    expect(h.sent).toEqual([]);
  });
});

describe("an acknowledgement is sent once per position", () => {
  it("sends one acknowledgement over five refreshes", () => {
    const h = harness();
    h.setActive("a");
    h.setCandidates([tab("a", "done", { position: 3 })]);
    for (let i = 0; i < 5; i++) {
      h.controller.refresh();
    }
    expect(h.sent).toEqual([{ id: "a", seen: 3 }]);
  });

  it("resends on the next refresh after a failure", async () => {
    const h = harness();
    h.answer("failed");
    h.setActive("a");
    h.setCandidates([tab("a", "done", { position: 3 })]);
    h.controller.refresh();
    await settled();
    h.controller.refresh();
    expect(h.sent).toHaveLength(2);
  });

  it("does not resend a refused position on a refresh, and does after a sync point", async () => {
    const h = harness();
    h.answer("refused");
    h.setActive("a");
    h.setCandidates([tab("a", "done", { position: 3 })]);
    h.controller.refresh();
    await settled();
    h.controller.refresh();
    expect(h.sent).toHaveLength(1);
    h.controller.sync();
    expect(h.sent).toHaveLength(2);
  });

  it("sends a newer position for the same tab", () => {
    const h = harness();
    h.setActive("a");
    h.setCandidates([tab("a", "done", { position: 3 })]);
    h.controller.refresh();
    h.setCandidates([tab("a", "done", { position: 4 })]);
    h.controller.refresh();
    expect(h.sent).toEqual([
      { id: "a", seen: 3 },
      { id: "a", seen: 4 },
    ]);
  });
});

describe("acknowledging what the reader can see", () => {
  it("acknowledges every unread tab whose row is in view, chat and run alike", () => {
    const h = harness();
    h.setCandidates([
      tab("a", "done"),
      tab("r", "failed", { kind: "run", position: 3 }),
      tab("c", "done"),
    ]);
    h.setRowsInView(["a", "r"]);
    h.controller.ackSeen();
    expect(h.sent).toEqual([
      { id: "a", seen: 1 },
      { id: "r", seen: 3 },
    ]);
  });

  it("acknowledges nothing at all while the page is hidden", () => {
    const h = harness();
    h.setVisible(false);
    h.setCandidates([tab("a", "done")]);
    h.setRowsInView(["a"]);
    h.controller.ackSeen();
    expect(h.sent).toEqual([]);
  });

  it("acknowledges the tab switched to before the active id has moved", () => {
    const h = harness();
    h.setActive("old");
    h.setCandidates([tab("a", "done", { position: 2 })]);
    h.controller.ackSwitch("a");
    expect(h.sent).toEqual([{ id: "a", seen: 2 }]);
  });

  it("does not acknowledge a switch made while the page is hidden", () => {
    const h = harness();
    h.setVisible(false);
    h.setCandidates([tab("a", "done")]);
    h.controller.ackSwitch("a");
    expect(h.sent).toEqual([]);
  });
});

describe("every acknowledgement and sync point writes the badge again", () => {
  it("re-asserts on a switch that leaves the count unchanged", () => {
    const h = harness();
    h.setCandidates([tab("a", "done", { seen: 1 })]);
    h.controller.ackSwitch("a");
    h.controller.ackSwitch("a");
    expect(h.latest()).toEqual(NO_ATTENTION);
    expect(h.asserts()).toBe(2);
  });

  it("re-asserts on becoming visible or hidden, with nothing to acknowledge", () => {
    const h = harness();
    h.setCandidates([tab("a", "done")]);
    h.controller.ackSeen();
    h.setVisible(false);
    h.controller.ackSeen();
    expect(h.asserts()).toBe(2);
    expect(h.latest()).toEqual({ count: 1, worst: "done" });
  });

  it("sync recomputes from the candidates before it asserts", () => {
    const h = harness();
    h.setCandidates([tab("b", "failed")]);
    h.controller.sync();
    expect(h.latest()).toEqual({ count: 1, worst: "failed" });
    expect(h.asserts()).toBe(1);
  });

  it("leaves a plain recompute to the count gate", () => {
    const h = harness();
    h.setCandidates([tab("a", "done")]);
    h.controller.refresh();
    h.controller.refresh();
    expect(h.asserts()).toBe(0);
  });
});

describe("a tab that stops wanting the reader retracts its notifications once", () => {
  it("retracts an unread tab another device's acknowledgement caught up", () => {
    const server = fakeServer();
    const h = harness(server);
    h.setCandidates([tab("a", "done", { position: 2 })]);
    h.controller.refresh();
    server.seen.set("a", 2);
    h.controller.refresh();
    h.controller.refresh();
    expect(h.retracted).toEqual(["a"]);
  });

  it("retracts a paused run that resumes", () => {
    const h = harness();
    h.setCandidates([tab("r", "waiting", { kind: "run", position: 0 })]);
    h.controller.refresh();
    h.setCandidates([tab("r", "working", { kind: "run", position: 0 })]);
    h.controller.refresh();
    expect(h.retracted).toEqual(["r"]);
  });

  it("retracts a tab that closed while it was counted", () => {
    const h = harness();
    h.setCandidates([tab("a", "done")]);
    h.controller.refresh();
    h.setCandidates([]);
    h.controller.refresh();
    expect(h.retracted).toEqual(["a"]);
  });

  it("retracts nothing while a tab keeps its class", () => {
    const h = harness();
    h.setCandidates([tab("a", "done"), tab("b", "input")]);
    h.controller.refresh();
    h.controller.refresh();
    expect(h.retracted).toEqual([]);
  });
});

describe("two windows on one server", () => {
  it("a lagging window's acknowledgement never hides a newer turn", async () => {
    const server = fakeServer();
    const fresh = harness(server);
    const lagging = harness(server);
    // Both read the chat at position 1; then turn 2 closes, which only `fresh` has heard of.
    fresh.setCandidates([tab("a", "done", { position: 2 })]);
    lagging.setCandidates([tab("a", "done", { position: 1 })]);
    lagging.setActive("a");
    lagging.controller.refresh();
    await settled();

    expect(server.acks).toEqual([{ id: "a", seen: 1 }]);
    fresh.controller.refresh();
    expect(fresh.latest()).toEqual({ count: 1, worst: "done" });
    // The lagging window catches up while the reader looks elsewhere: it counts the new turn too.
    lagging.setActive("");
    lagging.setCandidates([tab("a", "done", { position: 2 })]);
    lagging.controller.refresh();
    expect(lagging.latest()).toEqual({ count: 1, worst: "done" });
  });

  it("an older acknowledgement landing after a newer one leaves the newer marker", async () => {
    const server = fakeServer();
    const reader = harness(server);
    const stale = harness(server);
    reader.setActive("a");
    reader.setCandidates([tab("a", "done", { position: 2 })]);
    reader.controller.refresh();
    stale.setActive("a");
    stale.setCandidates([tab("a", "done", { position: 1 })]);
    stale.controller.refresh();
    await settled();
    expect(server.seen.get("a")).toBe(2);
  });

  it("a refused position never holds back the next one, so both windows converge", async () => {
    const server = fakeServer();
    const reader = harness(server);
    const other = harness(server);
    // The reader holds position 5, which a rewind elsewhere took back to 3: the ack is clamped.
    server.bound.set("a", 3);
    reader.setActive("a");
    reader.setCandidates([tab("a", "done", { position: 5 })]);
    reader.controller.refresh();
    await settled();
    expect(server.seen.get("a")).toBe(3);

    // The next turn closes at 6 on the server; both windows hear of it.
    server.bound.set("a", 6);
    reader.setCandidates([tab("a", "done", { position: 6 })]);
    other.setCandidates([tab("a", "done", { position: 6 })]);
    reader.controller.refresh();
    await settled();
    other.controller.refresh();
    expect(server.seen.get("a")).toBe(6);
    expect(other.latest()).toEqual(NO_ATTENTION);
  });
});
