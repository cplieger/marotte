// The route VOCABULARY: parseRoute, buildPath, and the round trip between them.

import { describe, it, expect } from "vitest";
import * as fc from "fast-check";
import { parseRoute, buildPath, type Route, type SettingsTab, type DocsTab } from "./route-path";

describe("parseRoute (table-driven)", () => {
  const cases: { name: string; pathname: string; hash: string; expected: Route }[] = [
    { name: "root → default chat", pathname: "/", hash: "", expected: { kind: "chat", id: "" } },
    {
      name: "empty string → default chat",
      pathname: "",
      hash: "",
      expected: { kind: "chat", id: "" },
    },
    { name: "/chat/abc", pathname: "/chat/abc", hash: "", expected: { kind: "chat", id: "abc" } },
    {
      name: "/chat/with%20space",
      pathname: "/chat/with%20space",
      hash: "",
      expected: { kind: "chat", id: "with space" },
    },
    {
      name: "/chat/ (missing id) → default",
      pathname: "/chat/",
      hash: "",
      expected: { kind: "chat", id: "" },
    },
    { name: "/git", pathname: "/git", hash: "", expected: { kind: "git", tab: "changes" } },
    {
      name: "/git/prs",
      pathname: "/git/prs",
      hash: "",
      expected: { kind: "git", tab: "prs" },
    },
    {
      // The identity contains `#`, which is why it rides as ONE percent-encoded fragment value
      // rather than three fields — and why it cannot be a path segment. `%23` is that `#`.
      name: "/git/prs#pr=<encoded identity>",
      pathname: "/git/prs",
      hash: "#pr=github%3Agithub.com%3Acplieger%2Fmarotte%2342",
      expected: { kind: "git", tab: "prs", pr: "github:github.com:cplieger/marotte#42" },
    },
    {
      name: "/git/prs#pr= (empty value) → no pr",
      pathname: "/git/prs",
      hash: "#pr=",
      expected: { kind: "git", tab: "prs" },
    },
    {
      // Read ONLY on the tab that holds pull requests, so the round trip stays an identity for the
      // two that do not.
      name: "/git#pr=x (the fragment on another tab) → no pr",
      pathname: "/git",
      hash: "#pr=github:x#1",
      expected: { kind: "git", tab: "changes" },
    },
    {
      name: "/git/sources#pr=x → no pr",
      pathname: "/git/sources",
      hash: "#pr=github:x#1",
      expected: { kind: "git", tab: "sources" },
    },
    {
      // safeDecode is the decoder here too, so a bare `%` survives as itself.
      name: "/git/prs#pr=%zz (malformed percent) → raw value",
      pathname: "/git/prs",
      hash: "#pr=%zz",
      expected: { kind: "git", tab: "prs", pr: "%zz" },
    },
    {
      name: "/git/sources",
      pathname: "/git/sources",
      hash: "",
      expected: { kind: "git", tab: "sources" },
    },
    {
      name: "/git/changes (explicit) → changes",
      pathname: "/git/changes",
      hash: "",
      expected: { kind: "git", tab: "changes" },
    },
    {
      name: "/git/unknown → changes",
      pathname: "/git/bogus",
      hash: "",
      expected: { kind: "git", tab: "changes" },
    },
    { name: "/history", pathname: "/history", hash: "", expected: { kind: "history" } },
    {
      name: "/history/runs",
      pathname: "/history/runs",
      hash: "",
      expected: { kind: "history", tab: "runs" },
    },
    {
      // The canonical pane omits its segment, so the explicit spelling folds to the same object the
      // bare path parses to.
      name: "/history/chats (explicit) → chats",
      pathname: "/history/chats",
      hash: "",
      expected: { kind: "history" },
    },
    {
      name: "/history/unknown → chats",
      pathname: "/history/bogus",
      hash: "",
      expected: { kind: "history" },
    },
    {
      // The spec board is deleted outright — no shim, no redirect. A saved /specs bookmark is just
      // an unknown path now.
      name: "/specs (retired route) → default chat",
      pathname: "/specs",
      hash: "",
      expected: { kind: "chat", id: "" },
    },
    {
      name: "/files → the mounts listing",
      pathname: "/files",
      hash: "",
      expected: { kind: "files", path: "/" },
    },
    {
      name: "/files/ → the mounts listing",
      pathname: "/files/",
      hash: "",
      expected: { kind: "files", path: "/" },
    },
    {
      // A rootless `/files/<segs>` is the ABSOLUTE path `/<segs>`: the file surface has ONE
      // container-absolute path space, and `normalizeDirPath` (files-shared.ts) is its only door.
      // There is no workspace-relative reading and no reachability probe.
      name: "/files/src/main.go (a rootless link resolves as the ABSOLUTE path)",
      pathname: "/files/src/main.go",
      hash: "",
      expected: { kind: "files", path: "/src/main.go" },
    },
    {
      name: "/files/dir%20name/f.ts",
      pathname: "/files/dir%20name/f.ts",
      hash: "",
      expected: { kind: "files", path: "/dir name/f.ts" },
    },
    {
      name: "/files/workspace/_ui-qa (the canonical form)",
      pathname: "/files/workspace/_ui-qa",
      hash: "",
      expected: { kind: "files", path: "/workspace/_ui-qa" },
    },
    {
      name: "/files//workspace/_ui-qa (CHARACTERIZATION: the legacy form, collapsed — green before and after)",
      pathname: "/files//workspace/_ui-qa",
      hash: "",
      expected: { kind: "files", path: "/workspace/_ui-qa" },
    },
    {
      name: "/files// (CHARACTERIZATION: the root arm already answers this)",
      pathname: "/files//",
      hash: "",
      expected: { kind: "files", path: "/" },
    },
    {
      name: "/files/. → the mounts listing",
      pathname: "/files/.",
      hash: "",
      expected: { kind: "files", path: "/" },
    },
    {
      name: "/file/readme.md (no hash)",
      pathname: "/file/readme.md",
      hash: "",
      expected: { kind: "file", path: "readme.md" },
    },
    {
      name: "/file/src/app.ts#L42",
      pathname: "/file/src/app.ts",
      hash: "#L42",
      expected: { kind: "file", path: "src/app.ts", line: 42 },
    },
    {
      name: "/file/x.ts#L0 (invalid line)",
      pathname: "/file/x.ts",
      hash: "#L0",
      expected: { kind: "file", path: "x.ts" },
    },
    {
      name: "/file/x.ts#Lfoo (non-numeric)",
      pathname: "/file/x.ts",
      hash: "#Lfoo",
      expected: { kind: "file", path: "x.ts" },
    },
    {
      name: "/file/ (missing path) → default",
      pathname: "/file/",
      hash: "",
      expected: { kind: "chat", id: "" },
    },
    {
      // A turn permalink: the fragment names the session-absolute ordinal.
      name: "/chat/c1#turn-7",
      pathname: "/chat/c1",
      hash: "#turn-7",
      expected: { kind: "chat", id: "c1", turn: 7 },
    },
    {
      name: "/chat/c1#turn-0 (no turn 0) → no turn",
      pathname: "/chat/c1",
      hash: "#turn-0",
      expected: { kind: "chat", id: "c1" },
    },
    {
      name: "/chat/c1#turn-7x (trailing junk) → no turn",
      pathname: "/chat/c1",
      hash: "#turn-7x",
      expected: { kind: "chat", id: "c1" },
    },
    {
      // The baseline: a run URL with no fragment carries no node, which is what every caller that
      // means "the run" produces.
      name: "/run/wf_1 (no hash)",
      pathname: "/run/wf_1",
      hash: "",
      expected: { kind: "run", id: "wf_1" },
    },
    {
      // A node path contains `/`, which is why it is a fragment rather than a path segment — the
      // tab's identity stays `(run, workflowId)`.
      name: "/run/wf_1#node=wf_1%2Fiter-0%2Fwork",
      pathname: "/run/wf_1",
      hash: "#node=wf_1%2Fiter-0%2Fwork",
      expected: { kind: "run", id: "wf_1", node: "wf_1/iter-0/work" },
    },
    {
      name: "/run/wf_1#node= (empty value) → no node",
      pathname: "/run/wf_1",
      hash: "#node=",
      expected: { kind: "run", id: "wf_1" },
    },
    {
      name: "/run/wf_1#L12 (the editor's fragment) → no node",
      pathname: "/run/wf_1",
      hash: "#L12",
      expected: { kind: "run", id: "wf_1" },
    },
    {
      // safeDecode is the decoder, so a bare `%` survives as itself instead of throwing — a hash
      // arrives straight off location and off popstate.
      name: "/run/wf_1#node=%zz (malformed percent) → raw value",
      pathname: "/run/wf_1",
      hash: "#node=%zz",
      expected: { kind: "run", id: "wf_1", node: "%zz" },
    },
    {
      name: "/settings → general",
      pathname: "/settings",
      hash: "",
      expected: { kind: "settings", tab: "general" },
    },
    {
      name: "/settings/ → general",
      pathname: "/settings/",
      hash: "",
      expected: { kind: "settings", tab: "general" },
    },
    {
      name: "/settings/tools",
      pathname: "/settings/tools",
      hash: "",
      expected: { kind: "settings", tab: "tools" },
    },
    {
      name: "/settings/permissions",
      pathname: "/settings/permissions",
      hash: "",
      expected: { kind: "settings", tab: "permissions" },
    },
    {
      name: "/settings/instructions",
      pathname: "/settings/instructions",
      hash: "",
      expected: { kind: "settings", tab: "instructions" },
    },
    {
      name: "/settings/git (retired tab) → general",
      pathname: "/settings/git",
      hash: "",
      expected: { kind: "settings", tab: "general" },
    },
    {
      name: "/settings/unknown → general",
      pathname: "/settings/bogus",
      hash: "",
      expected: { kind: "settings", tab: "general" },
    },
    {
      name: "/web/<abs path> → the page it names",
      pathname: "/web/workspace/demo/index.html",
      hash: "",
      expected: { kind: "web", path: "/workspace/demo/index.html" },
    },
    {
      name: "/web alone → default chat",
      pathname: "/web",
      hash: "",
      expected: { kind: "chat", id: "" },
    },
    {
      name: "/unknown → default chat",
      pathname: "/unknown",
      hash: "",
      expected: { kind: "chat", id: "" },
    },
    {
      name: "trailing slashes stripped",
      pathname: "/git///",
      hash: "",
      expected: { kind: "git", tab: "changes" },
    },
  ];

  it.each(cases)("$name", ({ pathname, hash, expected }) => {
    expect(parseRoute(pathname, hash)).toEqual(expected);
  });
});

describe("parseRoute/buildPath round-trip (property-based)", () => {
  // No "git": forge accounts live on the git view's Sources tab.
  const settingsTabs: SettingsTab[] = ["general", "tools", "permissions", "instructions"];

  // Exhaustive BY CONSTRUCTION: `satisfies Record<DocsTab, true>` makes a missing tab a compile
  // error, so a seventh sub-tab cannot be added to the type without appearing here — and once it is
  // here, the round-trip below fails until parseDocsTab learns it.
  const DOCS_TABS = {
    steering: true,
    skills: true,
    prompts: true,
    agents: true,
    specs: true,
    hooks: true,
    workflows: true,
    memories: true,
    powers: true,
  } satisfies Record<DocsTab, true>;
  const docsTabs = Object.keys(DOCS_TABS) as DocsTab[];

  // Container-absolute paths, which is the only space a files route has. The URL drops the leading
  // slash (`/files/a/b`), so no generated route serialises with an empty segment.
  const arbFilesRoute: fc.Arbitrary<Route> = fc.oneof(
    fc.constant<Route>({ kind: "files", path: "/" }),
    fc
      .array(
        fc
          .string({ minLength: 1, maxLength: 15 })
          .filter((s) => !s.includes("/") && !s.includes("#") && s !== "." && s !== ""),
        { minLength: 1, maxLength: 4 },
      )
      .map((segs): Route => ({ kind: "files", path: `/${segs.join("/")}` })),
  );

  // Arbitrary for a canonical Route (one that round-trips cleanly).
  const arbRoute: fc.Arbitrary<Route> = fc.oneof(
    // chat with non-empty id (empty id maps to "/" which is the default)
    fc
      .string({ minLength: 1, maxLength: 30 })
      .filter((s) => !s.includes("/") && !s.includes("#"))
      .map((id): Route => ({ kind: "chat", id })),
    // chat WITH a turn permalink
    fc
      .tuple(
        fc
          .string({ minLength: 1, maxLength: 30 })
          .filter((s) => !s.includes("/") && !s.includes("#")),
        fc.integer({ min: 1, max: 100000 }),
      )
      .map(([id, turn]): Route => ({ kind: "chat", id, turn })),
    fc.constantFrom<Route>(
      { kind: "git", tab: "changes" },
      { kind: "git", tab: "prs" },
      { kind: "git", tab: "sources" },
    ),
    // git WITH a pr: the identity is OPAQUE, so it is generated with the two characters that made
    // it a fragment in the first place — `/` and `#` — rather than filtered to a shape a path
    // segment could have carried.
    fc
      .tuple(
        fc.string({ minLength: 1, maxLength: 12 }).filter((s) => s.trim() !== ""),
        fc.string({ minLength: 1, maxLength: 12 }).filter((s) => s.trim() !== ""),
        fc.integer({ min: 1, max: 99999 }),
      )
      .map(([forge, repo, n]): Route => ({
        kind: "git",
        tab: "prs",
        pr: `${forge}:${repo}#${String(n)}`,
      })),
    // history: the bare spelling, and both panes stated. "chats" omits the segment (/history) and
    // folds back to the bare object; "runs" carries it.
    fc.constantFrom<Route>(
      { kind: "history" },
      { kind: "history", tab: "chats" },
      { kind: "history", tab: "runs" },
    ),
    arbFilesRoute,
    // web: an absolute page path, any segment text but a separator
    fc
      .array(
        fc
          .string({ minLength: 1, maxLength: 15 })
          .filter((s) => !s.includes("/") && s !== "." && s !== ""),
        { minLength: 1, maxLength: 4 },
      )
      .map((segs): Route => ({ kind: "web", path: `/${segs.join("/")}` })),
    // file without line
    fc
      .array(
        fc
          .string({ minLength: 1, maxLength: 15 })
          .filter((s) => !s.includes("/") && !s.includes("#") && s !== ""),
        { minLength: 1, maxLength: 4 },
      )
      .map((segs): Route => ({ kind: "file", path: segs.join("/") })),
    // file with line
    fc
      .tuple(
        fc.array(
          fc
            .string({ minLength: 1, maxLength: 15 })
            .filter((s) => !s.includes("/") && !s.includes("#") && s !== ""),
          { minLength: 1, maxLength: 4 },
        ),
        fc.integer({ min: 1, max: 10000 }),
      )
      .map(([segs, line]): Route => ({ kind: "file", path: segs.join("/"), line })),
    // run without a node — the "the run" spelling, byte-identical to what it has always been
    fc
      .string({ minLength: 1, maxLength: 30 })
      .filter((s) => !s.includes("/") && !s.includes("#"))
      .map((id): Route => ({ kind: "run", id })),
    // run WITH a node: a multi-segment path, which is the case a path segment could not carry
    fc
      .tuple(
        fc
          .string({ minLength: 1, maxLength: 20 })
          .filter((s) => !s.includes("/") && !s.includes("#")),
        fc.array(
          fc
            .string({ minLength: 1, maxLength: 15 })
            .filter((s) => !s.includes("/") && !s.includes("#") && s !== ""),
          { minLength: 1, maxLength: 4 },
        ),
      )
      .map(([id, segs]): Route => ({ kind: "run", id, node: segs.join("/") })),
    // settings
    fc.constantFrom(...settingsTabs).map((tab): Route => ({ kind: "settings", tab })),
    // docs — every sub-tab. "steering" omits the segment (/docs), the rest carry it, and all six
    // must survive the trip.
    fc.constantFrom(...docsTabs).map((tab): Route => ({ kind: "docs", tab })),
  );

  it("buildPath(route) round-trips through parseRoute to the canonical form", () => {
    fc.assert(
      fc.property(arbRoute, (route) => {
        const path = buildPath(route);
        const hashIdx = path.indexOf("#");
        const pathname = hashIdx >= 0 ? path.slice(0, hashIdx) : path;
        const hash = hashIdx >= 0 ? path.slice(hashIdx) : "";
        const parsed = parseRoute(pathname, hash);
        expect(parsed).toEqual(canonicalize(route));
      }),
      { numRuns: 500 },
    );
  });

  it.each([
    "/workspace/demo/my page.html",
    "/workspace/demo/a#b?.html",
    "/workspace/demo/100%.html",
    "/workspace/demo/café-日本.html",
    "/workspace/demo/a%2fb.html",
  ])("round-trips the web page %s", (path) => {
    const url = buildPath({ kind: "web", path });
    expect(url).not.toContain("#");
    expect(parseRoute(url, "")).toEqual({ kind: "web", path });
  });

  it("serialises a files route with no empty segment", () => {
    fc.assert(
      fc.property(arbFilesRoute, (route) => {
        const path = buildPath(route);
        expect(path.startsWith("/files//")).toBe(false);
        expect(path === "/files" || path.startsWith("/files/")).toBe(true);
      }),
      { numRuns: 500 },
    );
  });
});

function canonicalize(route: Route): Route {
  switch (route.kind) {
    case "settings":
      return route;
    case "file":
      if (route.line === undefined || route.line <= 0) {
        return { kind: "file", path: route.path };
      }
      return route;
    case "run":
      // An empty node is dropped on both sides: buildPath omits the fragment and parseRoute reads
      // `#node=` as no node. Mirrors the file/line case.
      if (route.node === undefined || route.node === "") {
        return { kind: "run", id: route.id };
      }
      return route;
    case "git":
      // Same shape one route over: an empty pr, or a pr on a tab that holds no pull requests, is
      // dropped by buildPath and never produced by parseRoute.
      if (route.pr === undefined || route.pr === "" || route.tab !== "prs") {
        return { kind: "git", tab: route.tab };
      }
      return route;
    case "history":
      // The canonical pane is the ABSENT field: buildPath omits its segment and parseRoute never
      // produces `tab: "chats"`.
      return route.tab === "runs" ? route : { kind: "history" };
    default:
      return route;
  }
}
describe("parseRoute adversarial inputs (no-throw)", () => {
  it("never throws on arbitrary pathname strings", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.string({ minLength: 0, maxLength: 200 }), (pathname) => {
        const r = parseRoute(pathname, "");
        return typeof r.kind === "string";
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("never throws on adversarial percent-encoded paths", () => {
    expect.assertions(1);
    const arbPath = fc.oneof(
      fc.string().map((s) => "/" + s),
      fc.string().map((s) => "/chat/" + encodeURIComponent(s)),
      fc
        .string()
        .map((s) => "/file/" + s.replace(/[^%]/g, (c) => "%" + c.charCodeAt(0).toString(16))),
      fc.constant("/%"),
      fc.constant("/%zz"),
      fc.constant("/%0"),
      fc.constant("/chat/%2"),
      fc.constant("/file/\x00bar"),
    );
    const result = fc.check(
      fc.property(arbPath, (pathname) => {
        const r = parseRoute(pathname, "");
        return typeof r.kind === "string";
      }),
      { numRuns: 300 },
    );
    expect(result.failed).toBe(false);
  });
});

// The spec route: one encoded segment holding a whole directory.

describe("the spec route", () => {
  it.each([".kiro/specs/x", "repo/.kiro/specs/feature-x"])("round-trips %s", (dir) => {
    const path = buildPath({ kind: "spec", dir });
    expect(path).toBe(`/spec/${encodeURIComponent(dir)}`);
    expect(path.split("/")).toHaveLength(3);
    expect(parseRoute(path, "")).toEqual({ kind: "spec", dir });
  });

  it("refuses an unencoded directory and an empty one", () => {
    expect(parseRoute("/spec/.kiro/specs/x", "")).toEqual({ kind: "chat", id: "" });
    expect(parseRoute("/spec/", "")).toEqual({ kind: "chat", id: "" });
    expect(parseRoute("/spec", "")).toEqual({ kind: "chat", id: "" });
  });
});

// The git `#pr=` fragment, stated rather than sampled.

describe("the git #pr= fragment", () => {
  const identity = "github:github.com:cplieger/marotte#42";

  it("round-trips through the URL it emits", () => {
    const route: Route = { kind: "git", tab: "prs", pr: identity };
    const url = buildPath(route);
    expect(url).toBe(`/git/prs#pr=${encodeURIComponent(identity)}`);
    const [pathname, hash] = [url.slice(0, url.indexOf("#")), url.slice(url.indexOf("#"))];
    expect(parseRoute(pathname, hash)).toEqual(route);
  });

  it("emits nothing for a pr on a tab that holds no pull requests", () => {
    expect(buildPath({ kind: "git", tab: "changes", pr: identity })).toBe("/git");
    expect(buildPath({ kind: "git", tab: "sources", pr: identity })).toBe("/git/sources");
  });

  it("leaves the absent case byte-identical to what every existing caller produces", () => {
    expect(buildPath({ kind: "git", tab: "changes" })).toBe("/git");
    expect(buildPath({ kind: "git", tab: "prs" })).toBe("/git/prs");
    expect(buildPath({ kind: "git", tab: "prs", pr: "" })).toBe("/git/prs");
  });
});
