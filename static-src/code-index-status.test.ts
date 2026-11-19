// The Kiro Code Analysis sub-row: the wording of each status kiro-cli reports, and the row's one button.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { dispatch } from "./bus.js";
import { codeIndexLine, mountCodeIndexStatus } from "./code-index-status.js";
import { shownSettingsSignal } from "./persist.js";
import type { CodeIndexStatusPayload, EffectiveSettings } from "./wire/types.gen.js";

const ROOT = "/workspace";

function status(over: Partial<CodeIndexStatusPayload>): CodeIndexStatusPayload {
  return { workspace_root: ROOT, store: { state: "none" }, ...over };
}

const building: CodeIndexStatusPayload = status({
  build: { build_id: "b1", state: "running", phase: "parse", message: "Parsing 40 of 120 files" },
});
const ready: CodeIndexStatusPayload = status({
  store: { state: "ready", summary: { files: 1234, functions: 5678 } },
  build: { build_id: "b1", state: "succeeded", phase: "save", message: "Saved" },
});

describe("codeIndexLine", () => {
  const cases: [string, CodeIndexStatusPayload | undefined, string, string, "build" | "cancel"][] =
    [
      ["no status reported yet", undefined, "No index yet", "", "build"],
      ["no saved index", status({}), "No index yet", "", "build"],
      [
        "a build at its second phase",
        building,
        "Building: Parsing (2 of 4)",
        "Parsing 40 of 120 files",
        "cancel",
      ],
      [
        "a build at its first phase",
        status({ build: { build_id: "b1", state: "running", phase: "scan", message: "Scanning" } }),
        "Building: Scanning (1 of 4)",
        "Scanning",
        "cancel",
      ],
      [
        "a build kiro-cli names no phase for",
        status({ build: { build_id: "b1", state: "running", message: "Starting" } }),
        "Building",
        "Starting",
        "cancel",
      ],
      [
        "a build waiting behind another",
        status({
          build: { build_id: "b2", state: "pending", message: "Waiting for another build" },
        }),
        "Build queued",
        "Waiting for another build",
        "cancel",
      ],
      ["a saved index", ready, "Ready: 1,234 files, 5,678 functions", "", "build"],
      [
        "an index that cannot be read",
        status({ store: { state: "unreadable" } }),
        "The index cannot be read",
        "",
        "build",
      ],
      [
        "a failed build",
        status({
          build: {
            build_id: "b1",
            state: "failed",
            phase: "parse",
            message: "",
            reason: "error",
            detail: "disk full",
          },
        }),
        "Build failed",
        "error: disk full",
        "build",
      ],
      [
        "a cancelled build",
        status({
          build: {
            build_id: "b1",
            state: "canceled",
            phase: "graph",
            message: "",
            reason: "client",
          },
        }),
        "Build cancelled",
        "client",
        "build",
      ],
    ];
  for (const [name, st, text, detail, action] of cases) {
    it(name, () => {
      expect(codeIndexLine(st)).toEqual({ text, detail, action });
    });
  }
});

describe("mountCodeIndexStatus", () => {
  const posts: { path: string; body: unknown }[] = [];
  let answer: (path: string) => Response;

  beforeEach(() => {
    document.body.innerHTML = `
      <div id="code-index-status" hidden>
        <span id="code-index-status-text"></span>
        <span id="code-index-status-detail"></span>
        <button id="code-index-action" type="button"></button>
      </div>`;
    posts.length = 0;
    answer = () => new Response(null, { status: 204 });
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const path = new URL(input instanceof Request ? input.url : String(input), location.href)
          .pathname;
        if (path === "/api/code-index") {
          return Promise.resolve(Response.json({ root: ROOT, statuses: [building] }));
        }
        posts.push({
          path,
          body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
        });
        return Promise.resolve(answer(path));
      }),
    );
    shownSettingsSignal.value = { code_analysis_enabled: true } as EffectiveSettings;
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    shownSettingsSignal.value = null;
    document.body.innerHTML = "";
  });

  const el = (id: string): HTMLElement => document.getElementById(id) as HTMLElement;

  it("shows the switch's status, with Cancel replacing Build while a build runs", async () => {
    mountCodeIndexStatus();
    await vi.waitFor(() => {
      expect(el("code-index-status-text").textContent).toBe("Building: Parsing (2 of 4)");
    });
    expect(el("code-index-status").hidden).toBe(false);
    expect(el("code-index-status-detail").textContent).toBe("Parsing 40 of 120 files");
    expect(el("code-index-action").textContent).toBe("Cancel");

    dispatch({ type: "code_index_status", chat_id: "", payload: ready });

    expect(el("code-index-status-text").textContent).toBe("Ready: 1,234 files, 5,678 functions");
    expect(el("code-index-status-detail").hidden).toBe(true);
    expect(el("code-index-action").textContent).toBe("Build");
  });

  it("is hidden while the switch is off", () => {
    shownSettingsSignal.value = { code_analysis_enabled: false } as EffectiveSettings;
    mountCodeIndexStatus();
    expect(el("code-index-status").hidden).toBe(true);
  });

  it("cancels the running build by its id", async () => {
    mountCodeIndexStatus();
    await vi.waitFor(() => {
      expect(el("code-index-action").textContent).toBe("Cancel");
    });
    el("code-index-action").click();
    await vi.waitFor(() => {
      expect(posts).toEqual([{ path: "/api/code-index/cancel", body: { build_id: "b1" } }]);
    });
  });

  it("starts a build and shows kiro-cli's refusal under the line", async () => {
    answer = () => Response.json({ error: "The code index is not enabled." }, { status: 409 });
    mountCodeIndexStatus();
    await vi.waitFor(() => {
      expect(el("code-index-action").textContent).toBe("Cancel");
    });
    dispatch({ type: "code_index_status", chat_id: "", payload: ready });
    el("code-index-action").click();

    await vi.waitFor(() => {
      expect(el("code-index-status-detail").textContent).toBe("The code index is not enabled.");
    });
    expect(posts.map((p) => p.path)).toEqual(["/api/code-index/build"]);
    expect(el("code-index-status-detail").hidden).toBe(false);
    expect((el("code-index-action") as HTMLButtonElement).disabled).toBe(false);
  });
});
