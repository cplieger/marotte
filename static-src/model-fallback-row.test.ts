import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { http, HttpResponse, type JsonBodyType } from "msw";
import { setupWorker } from "msw/browser";
import { userEvent } from "vitest/browser";
import { settingsPayload } from "./__test-helpers__/settings.js";
import { rovingFocus } from "./legacy-ui/roving-focus.js";
import type { IdentityVerdict } from "./identity.js";
import type { ModelInfo } from "./types.js";
import type { ModelFallback } from "./wire/types.gen.js";

import { SIGN_IN_WORDS, buildFallbackRow } from "./model-fallback-row.js";

const worker = setupWorker();
let sent: unknown[] = [];

beforeAll(async () => {
  await worker.start({ quiet: true, onUnhandledFrame: "error" });
});

afterEach(() => {
  worker.resetHandlers();
  sent = [];
  document.body.replaceChildren();
});

afterAll(() => {
  worker.stop();
});

/** The server's answer to PUT /api/model-fallback, recording each body sent. */
function answer(status: number, body: JsonBodyType): void {
  worker.use(
    http.put("/api/model-fallback", async ({ request }) => {
      sent.push(await request.json());
      return HttpResponse.json(body, { status });
    }),
  );
}

const catalog: ModelInfo[] = [
  ["auto", "Auto"],
  ["model-a", "Alpha"],
  ["model-b", "Bravo"],
  ["model-c", "Charlie"],
].map(([id = "", name = ""]) => ({ model_id: id, model_name: name, rate_multiplier: 1 }));

const signedIn: IdentityVerdict = {
  state: "signed_in",
  email: "someone@example.invalid",
  method: "",
  startUrl: "",
  region: "",
  apiKey: false,
};

function fallback(over: Partial<ModelFallback> = {}): ModelFallback {
  return {
    model: "model-a",
    configured: "",
    vendor_default: "model-b",
    read_only_reason: "",
    ...over,
  };
}

function build(
  rows: ModelFallback[],
  { enabled = true, identity = signedIn, model = "model-a" } = {},
): HTMLElement | null {
  return (
    buildFallbackRow(
      {
        model,
        settings: settingsPayload({ model_fallback_enabled: enabled, model_fallbacks: rows }),
        catalog,
        identity,
      },
      () => undefined,
    )?.el ?? null
  );
}

function select(row: HTMLElement | null): HTMLSelectElement {
  const s = row?.querySelector("select");
  if (s === null || s === undefined) {
    throw new Error("no select");
  }
  return s;
}

function options(row: HTMLElement | null): [string, string][] {
  return [...select(row).options].map((o) => [o.value, o.textContent]);
}

describe("the model picker's Fallback row", () => {
  it("absent while Model fallback is off", () => {
    expect(build([fallback({ configured: "model-c" })], { enabled: false })).toBeNull();
  });

  it("shows the stored account choice when the switch is on", () => {
    const row = build([fallback({ configured: "model-c" })]);
    expect(select(row).value).toBe("model-c");
    expect(options(row)[0]).toEqual(["", "Kiro default (Bravo)"]);
    expect(select(row).disabled).toBe(false);
  });

  it("names Kiro default alone when Kiro states no fallback", () => {
    expect(options(build([fallback({ vendor_default: "" })]))[0]).toEqual(["", "Kiro default"]);
  });

  it("offers no none value", () => {
    expect(options(build([fallback()])).map(([v]) => v)).toEqual(["", "model-b", "model-c"]);
  });

  it("is absent for a model KAS states no fallback for, and for Auto", () => {
    expect(build([fallback()], { model: "model-c" })).toBeNull();
    expect(build([fallback({ model: "auto" })], { model: "auto" })).toBeNull();
  });

  it("read-only for locked, multiple targets, signed out and API key", () => {
    const cases: [HTMLElement | null, string][] = [
      [
        build([fallback({ read_only_reason: "locked" })]),
        "This fallback setting is locked by policy.",
      ],
      [
        build([fallback({ configured: "model-b", read_only_reason: "multiple-targets" })]),
        "This fallback chain has multiple targets and cannot be edited here.",
      ],
      [build([], { identity: { state: "signed_out" } }), SIGN_IN_WORDS],
      [build([fallback()], { identity: { ...signedIn, apiKey: true } }), SIGN_IN_WORDS],
    ];
    for (const [row, words] of cases) {
      expect(select(row).disabled).toBe(true);
      expect(row?.querySelector(".section-hint")?.textContent).toBe(words);
    }
  });

  it("Kiro default sends an empty target and repaints from the answer", async () => {
    answer(200, fallback({ vendor_default: "model-c" }));
    const row = build([fallback({ configured: "model-c" })]);
    const s = select(row);
    s.value = "";
    s.dispatchEvent(new Event("change"));
    await vi.waitFor(() => {
      expect(sent).toEqual([{ model: "model-a", target: "" }]);
      expect(options(row)[0]).toEqual(["", "Kiro default (Charlie)"]);
    });
    expect(s.value).toBe("");
    expect(s.disabled).toBe(false);
  });

  it("keeps the card's roving focus out of its own arrow keys", async () => {
    // The picker card's rovingFocus reads no target, so the select's keys must not reach it.
    answer(200, fallback());
    const card = document.createElement("div");
    const item = document.createElement("div");
    item.className = "pill-model-item";
    item.tabIndex = -1;
    const row = build([fallback()]) as HTMLElement;
    card.append(item, row);
    document.body.append(card);
    const nav = rovingFocus(card, ".pill-model-item");
    const s = select(row);
    s.focus();

    for (const key of ["{ArrowDown}", "{ArrowUp}", "{Home}", "{End}"]) {
      await userEvent.keyboard(key);
      expect(document.activeElement, `focus stayed on the select after ${key}`).toBe(s);
    }
    // The keys changed the choice; a write still running would land in the next case's record.
    await vi.waitFor(() => {
      expect(sent.at(-1)).toEqual({ model: "model-a", target: "model-c" });
      expect(s.getAttribute("aria-busy")).toBeNull();
    });
    nav.dispose();
  });

  it("keeps focus on the select through a write", async () => {
    worker.use(
      http.put("/api/model-fallback", async ({ request }) => {
        const body = (await request.json()) as { target: string };
        sent.push(body);
        return HttpResponse.json(fallback({ configured: body.target }));
      }),
    );
    const row = build([fallback()]) as HTMLElement;
    document.body.append(row);
    const s = select(row);
    s.focus();

    await userEvent.keyboard("{ArrowDown}");
    await vi.waitFor(() => {
      expect(sent).toEqual([{ model: "model-a", target: "model-b" }]);
      expect(s.getAttribute("aria-busy")).toBeNull();
    });
    expect(document.activeElement).toBe(s);
    expect(s.value).toBe("model-b");
  });

  it("sends and keeps a second choice made while the first write runs", async () => {
    let release: () => void = () => undefined;
    const first = new Promise<void>((r) => {
      release = r;
    });
    worker.use(
      http.put("/api/model-fallback", async ({ request }) => {
        const body = (await request.json()) as { target: string };
        sent.push(body);
        if (sent.length === 1) {
          await first;
        }
        return HttpResponse.json(fallback({ configured: body.target }));
      }),
    );
    const row = build([fallback()]);
    const s = select(row);
    s.value = "model-b";
    s.dispatchEvent(new Event("change"));
    await vi.waitFor(() => {
      expect(sent).toHaveLength(1);
    });
    s.value = "model-c";
    s.dispatchEvent(new Event("change"));
    release();

    await vi.waitFor(() => {
      expect(sent).toEqual([
        { model: "model-a", target: "model-b" },
        { model: "model-a", target: "model-c" },
      ]);
      expect(s.getAttribute("aria-busy")).toBeNull();
    });
    expect(s.value).toBe("model-c");
  });

  it("a write refused for no sign-in shows the sign-in words and puts the choice back", async () => {
    answer(409, {
      error: "kiro-cli cannot change model fallback now. Sign in to Kiro, then try again.",
      code: "unavailable",
    });
    const row = build([fallback({ configured: "model-c" })]);
    const s = select(row);
    s.value = "model-b";
    s.dispatchEvent(new Event("change"));
    await vi.waitFor(() => {
      expect(sent).toEqual([{ model: "model-a", target: "model-b" }]);
      expect(row?.querySelector(".section-hint")?.textContent).toBe(SIGN_IN_WORDS);
    });
    expect(s.value).toBe("model-c");
    expect(s.disabled).toBe(true);
  });
});
