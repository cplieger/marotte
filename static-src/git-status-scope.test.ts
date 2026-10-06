// What a scoped git-status read puts on the wire and dedups against. `?paths=` keeps a per-edit trigger affordable;
// the client's share of the contract is the query and the dedupe key, and both fail invisibly. The action layer is
// mocked at the definition so the real `request` and `dedupe` builders run.
import { describe, it, expect, vi } from "vitest";

const { captured } = vi.hoisted(() => ({
  captured: { paths: [] as string[], keys: [] as string[] },
}));

interface ScopeArgs {
  paths?: readonly string[];
}
interface ApiCfg {
  request: (a: ScopeArgs) => { method: string; path: string };
}
interface DefCfg {
  dedupe: (a: ScopeArgs) => string;
  run: (a: ScopeArgs) => Promise<{ repos: never[] }>;
}

vi.mock("./actions/index.js", () => ({
  apiAction: (cfg: ApiCfg) => ({
    dispatch: (args: ScopeArgs) => {
      captured.paths.push(cfg.request(args).path);
      return Promise.resolve({ repos: [] });
    },
  }),
  defineAction: (cfg: DefCfg) => ({
    dispatch: (args: ScopeArgs) => {
      captured.keys.push(cfg.dedupe(args));
      return cfg.run(args);
    },
  }),
  pollAction: () => undefined,
}));
vi.mock("./bus.js", () => ({ onSSE: () => undefined }));

const store = await import("./git-status-store.js");

async function read(paths?: readonly string[]): Promise<string> {
  captured.paths.length = 0;
  await store.refreshGitStatus(paths);
  return captured.paths[captured.paths.length - 1] ?? "";
}

async function key(paths?: readonly string[]): Promise<string> {
  captured.keys.length = 0;
  await store.refreshGitStatus(paths);
  return captured.keys[captured.keys.length - 1] ?? "";
}

describe("the URL a git-status read builds", () => {
  it("asks for the whole tree when the caller can name nothing", async () => {
    expect(await read()).toBe("/api/git/status-all");
    expect(await read([])).toBe("/api/git/status-all");
  });

  it("names the paths it was given, comma-joined and encoded once", async () => {
    expect(await read(["subflux/main.go", "marotte/app.ts"])).toBe(
      "/api/git/status-all?paths=subflux%2Fmain.go%2Cmarotte%2Fapp.ts",
    );
  });

  // A comma in a path would split it into two paths that resolve to nothing, and the read would still report success.
  it("encodes a path that contains the separator", async () => {
    const url = await read(["repo/a,b.txt"]);
    expect(url).toBe("/api/git/status-all?paths=repo%2Fa%2Cb.txt");
    expect(decodeURIComponent(url.slice(url.indexOf("=") + 1))).toBe("repo/a,b.txt");
  });

  // A tool call reports the same file in `locations` and `diffs[].path`, so duplicates are the normal input.
  it("drops duplicates and empty entries rather than sending them", async () => {
    expect(await read(["a/x.go", "a/x.go", "", "b/y.go"])).toBe(
      "/api/git/status-all?paths=a%2Fx.go%2Cb%2Fy.go",
    );
  });

  // The server's cap binds; this stops a URL that is mostly discarded.
  it("caps how many paths it names", async () => {
    const many = Array.from({ length: 200 }, (_, i) => `r${String(i)}/f.go`);
    const url = await read(many);
    const sent = decodeURIComponent(url.slice(url.indexOf("=") + 1)).split(",");
    expect(sent).toHaveLength(64);
    expect(sent[0]).toBe("r0/f.go");
  });

  // A scope of only empties is not a scope: the server would answer an empty repository set from the snapshot, never
  // re-reading the tree.
  it("falls back to the whole tree when every path was empty", async () => {
    expect(await read(["", ""])).toBe("/api/git/status-all");
  });
});

describe("the key a git-status read dedups under", () => {
  // Two different scopes must not collapse under one key, or the second repository is never scanned and its badge
  // and decorations stay stale.
  it("gives two different scopes two different keys", async () => {
    const a = await key(["subflux/main.go"]);
    const b = await key(["marotte/app.ts"]);
    expect(a).not.toBe(b);
  });

  it("gives one scope one key, so a burst of identical reads is one read", async () => {
    expect(await key(["a/x.go"])).toBe(await key(["a/x.go"]));
    // Order and duplicates do not make a new scope.
    expect(await key(["a/x.go", "b/y.go"])).toBe(await key(["a/x.go", "b/y.go", "a/x.go"]));
  });

  it("gives the unscoped read its own key, apart from every scoped one", async () => {
    const whole = await key();
    expect(whole).toBe(await key([]));
    expect(whole).not.toBe(await key(["a/x.go"]));
  });
});
