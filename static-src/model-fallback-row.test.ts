import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { http, HttpResponse, type JsonBodyType } from "msw";
import { setupWorker } from "msw/browser";
import { userEvent } from "vitest/browser";
import { settingsPayload } from "./__test-helpers__/settings.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { humanName } from "./strings.js";
import type { IdentityVerdict } from "./identity.js";
import type { ModelInfo } from "./types.js";
import type { ModelFallback } from "./wire/types.gen.js";

import { FallbackSlot, SIGN_IN_WORDS } from "./model-fallback-row.js";

// A write's answer refetches the settings it changed (persist.ts); nothing here reads them.
const worker = setupWorker(http.get("/api/settings", () => HttpResponse.json(settingsPayload({}))));
let sent: unknown[] = [];
let sheet: HTMLStyleElement | null = null;

beforeAll(async () => {
  await worker.start({ quiet: true, onUnhandledFrame: "error" });
  sheet = mountAppCSS();
});

afterEach(() => {
  worker.resetHandlers();
  sent = [];
  document.body.replaceChildren();
  document.documentElement.removeAttribute("dir");
  vi.restoreAllMocks();
});

afterAll(() => {
  worker.stop();
  sheet?.remove();
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

/** Answers each write with the target it asked for, and records the body. */
function echo(): void {
  worker.use(
    http.put("/api/model-fallback", async ({ request }) => {
      const body = (await request.json()) as { target: string };
      sent.push(body);
      return HttpResponse.json(fallback({ configured: body.target }));
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

interface Card {
  readonly card: HTMLElement;
  readonly main: HTMLElement;
  readonly slot: FallbackSlot;
}

/** The model card as the picker builds it: the card, its first panel with a model row, and the slot's row last. */
function mount(
  rows: ModelFallback[],
  { enabled = true, identity = signedIn, model = "model-a" } = {},
): Card {
  const card = document.createElement("span");
  card.className = "pill-expand-content pill-model-list is-open";
  const main = document.createElement("div");
  main.className = "model-panel model-panel-main";
  const scroll = document.createElement("div");
  scroll.className = "pill-model-scroll";
  for (const m of catalog) {
    const row = document.createElement("div");
    row.className = "pill-model-item";
    row.setAttribute("role", "option");
    row.textContent = m.model_name;
    scroll.append(row);
  }
  main.append(scroll);
  card.append(main);
  const anchor = document.createElement("span");
  anchor.className = "pill-slot";
  anchor.style.cssText = "position: fixed; inset-block-end: 2rem; inset-inline-start: 2rem;";
  anchor.append(card);
  document.body.append(anchor);
  const slot = new FallbackSlot(() => undefined);
  slot.sync(
    { card, main },
    {
      model,
      settings: settingsPayload({ model_fallback_enabled: enabled, model_fallbacks: rows }),
      catalog,
      identity,
    },
  );
  return { card, main, slot };
}

function opener(c: Card): HTMLButtonElement | null {
  return c.main.querySelector<HTMLButtonElement>("button[aria-controls]");
}

function openerOf(c: Card): HTMLButtonElement {
  const b = opener(c);
  if (b === null) {
    throw new Error("no Fallback row");
  }
  return b;
}

function panelOf(c: Card): HTMLElement {
  const id = openerOf(c).getAttribute("aria-controls") ?? "";
  const p = c.card.querySelector<HTMLElement>(`[id="${id}"]`);
  if (p === null) {
    throw new Error("no Fallback panel");
  }
  return p;
}

function optionsOf(c: Card): HTMLElement[] {
  return [...panelOf(c).querySelectorAll<HTMLElement>('[role="listbox"] [role="option"]')];
}

/** Each option's label, and whether it carries the card's selected style. */
function listed(c: Card): [string, boolean][] {
  return optionsOf(c).map((o) => [
    o.querySelector(".pill-model-name")?.textContent ?? "",
    o.getAttribute("aria-selected") === "true" && o.classList.contains("active"),
  ]);
}

function hintOf(c: Card): string {
  return c.main.querySelector(".section-hint")?.textContent ?? "";
}

function backOf(c: Card): HTMLButtonElement {
  const buttons = [...panelOf(c).querySelectorAll<HTMLButtonElement>("button")];
  const back = buttons.at(-1);
  if (back === undefined) {
    throw new Error("no Back");
  }
  return back;
}

/** Runs every animation to its end, then lets the slide's cleanup run. */
async function settle(): Promise<void> {
  for (const a of document.getAnimations()) {
    a.finish();
  }
  await new Promise((r) => setTimeout(r, 0));
}

/** The panel the card shows, or is sliding to: a leaving panel paints only until its slide ends. */
function shownPanel(c: Card): "main" | "fallback" {
  const shows = (p: HTMLElement): boolean =>
    !p.hidden && !p.classList.contains("model-panel-leaving");
  const fb = panelOf(c);
  if (shows(fb) && !shows(c.main)) {
    return "fallback";
  }
  if (!shows(fb) && shows(c.main)) {
    return "main";
  }
  throw new Error(
    `both or neither panel shown (main ${String(shows(c.main))}, fallback ${String(shows(fb))})`,
  );
}

describe("the model picker's Fallback row", () => {
  it("absent while Model fallback is off", () => {
    expect(opener(mount([fallback({ configured: "model-c" })], { enabled: false }))).toBeNull();
  });

  it("is one row naming the account's choice, with a chevron, its name saying both", () => {
    const c = mount([fallback({ configured: "model-c" })]);
    const row = openerOf(c);
    expect(row.classList.contains("pill-model-item")).toBe(true);
    expect(row.querySelector(".pill-model-name")?.textContent).toBe("Fallback");
    expect(row.querySelector(".pill-model-meta")?.textContent).toBe("Charlie");
    expect(row.querySelector(".disclosure-chevron")?.getAttribute("aria-hidden")).toBe("true");
    expect(row.getAttribute("aria-label")).toBe("Fallback: Charlie");
    expect(row.getAttribute("aria-expanded")).toBe("false");
    expect(row.disabled).toBe(false);
    expect(c.main.lastElementChild?.contains(row)).toBe(true);
  });

  it("names Kiro default with its model, or alone when Kiro states none", () => {
    expect(openerOf(mount([fallback()])).getAttribute("aria-label")).toBe(
      "Fallback: Kiro default (Bravo)",
    );
    document.body.replaceChildren();
    expect(openerOf(mount([fallback({ vendor_default: "" })])).getAttribute("aria-label")).toBe(
      "Fallback: Kiro default",
    );
  });

  it("is absent for a model KAS states no fallback for, and for Auto", () => {
    expect(opener(mount([fallback()], { model: "model-c" }))).toBeNull();
    document.body.replaceChildren();
    expect(opener(mount([fallback({ model: "auto" })], { model: "auto" }))).toBeNull();
  });

  it("read-only for locked, multiple targets, signed out and API key", () => {
    const cases: [ModelFallback[], Partial<{ identity: IdentityVerdict }>, string][] = [
      [
        [fallback({ read_only_reason: "locked" })],
        {},
        "This fallback setting is locked by policy.",
      ],
      [
        [fallback({ configured: "model-b", read_only_reason: "multiple-targets" })],
        {},
        "This fallback chain has multiple targets and cannot be edited here.",
      ],
      [[], { identity: { state: "signed_out" } }, SIGN_IN_WORDS],
      [[fallback()], { identity: { ...signedIn, apiKey: true } }, SIGN_IN_WORDS],
    ];
    for (const [rows, opts, words] of cases) {
      document.body.replaceChildren();
      const c = mount(rows, opts);
      expect(openerOf(c).disabled, words).toBe(true);
      expect(hintOf(c), words).toBe(words);
      openerOf(c).click();
      expect(shownPanel(c), words).toBe("main");
    }
  });
});

describe("the Fallback list", () => {
  it("opens from the row: the chat's model named on top, Kiro default first, Back last", async () => {
    const c = mount([fallback({ configured: "model-c" })]);
    await userEvent.click(openerOf(c));
    await settle();

    expect(shownPanel(c)).toBe("fallback");
    expect(openerOf(c).getAttribute("aria-expanded")).toBe("true");
    const panel = panelOf(c);
    const note = panel.firstElementChild;
    expect(note?.textContent).toBe("Fallback for Alpha");
    expect(note?.matches("button, [role]")).toBe(false);
    expect(panel.querySelector('[role="listbox"]')?.getAttribute("aria-label")).toBe(
      "Fallback model",
    );
    // The old select's options: Kiro default, then every model but Auto and the chat's own.
    expect(listed(c)).toEqual([
      ["Kiro default (Bravo)", false],
      ["Bravo", false],
      ["Charlie", true],
    ]);
    const back = backOf(c);
    expect(panel.lastElementChild?.contains(back)).toBe(true);
    expect(back.textContent).toBe("Back");
    expect(back.classList.contains("pill-model-item")).toBe(true);
    expect(back.querySelector(".disclosure-chevron")?.getAttribute("aria-hidden")).toBe("true");
  });

  it("lists a stored target the catalog no longer offers", async () => {
    const c = mount([fallback({ configured: "model-z" })]);
    await userEvent.click(openerOf(c));
    expect(listed(c).map(([name]) => name)).toEqual([
      "Kiro default (Bravo)",
      "Bravo",
      "Charlie",
      humanName("model-z"),
    ]);
  });

  it("moves focus to the selected option, and back to the row on return", async () => {
    const c = mount([fallback({ configured: "model-b" })]);
    openerOf(c).focus();
    await userEvent.keyboard("{Enter}");
    expect(document.activeElement).toBe(optionsOf(c)[1]);
    await userEvent.keyboard("{ArrowDown}");
    expect(document.activeElement).toBe(optionsOf(c)[2]);
    await userEvent.keyboard("{ArrowUp}{ArrowUp}");
    expect(document.activeElement).toBe(optionsOf(c)[0]);
    await userEvent.keyboard("{ArrowLeft}");
    await settle();
    expect(shownPanel(c)).toBe("main");
    expect(document.activeElement).toBe(openerOf(c));
    expect(sent).toEqual([]);
  });

  it("a pick applies, returns to the row and names the new choice there", async () => {
    echo();
    const c = mount([fallback()]);
    await userEvent.click(openerOf(c));
    const charlie = optionsOf(c)[2];
    if (charlie === undefined) {
      throw new Error("no Charlie row");
    }
    await userEvent.click(charlie);
    await settle();

    expect(shownPanel(c)).toBe("main");
    expect(openerOf(c).getAttribute("aria-label")).toBe("Fallback: Charlie");
    expect(document.activeElement).toBe(openerOf(c));
    await vi.waitFor(() => {
      expect(sent).toEqual([{ model: "model-a", target: "model-c" }]);
      expect(openerOf(c).getAttribute("aria-busy")).toBeNull();
    });
    expect(openerOf(c).getAttribute("aria-label")).toBe("Fallback: Charlie");
  });

  it("Kiro default sends an empty target and repaints from the answer", async () => {
    answer(200, fallback({ vendor_default: "model-c" }));
    const c = mount([fallback({ configured: "model-c" })]);
    await userEvent.click(openerOf(c));
    const kiro = optionsOf(c)[0];
    if (kiro === undefined) {
      throw new Error("no Kiro default row");
    }
    await userEvent.click(kiro);
    await vi.waitFor(() => {
      expect(sent).toEqual([{ model: "model-a", target: "" }]);
      expect(openerOf(c).getAttribute("aria-label")).toBe("Fallback: Kiro default (Charlie)");
    });
    expect(openerOf(c).disabled).toBe(false);
  });

  it("re-picking the current choice returns without a write", async () => {
    echo();
    const c = mount([fallback({ configured: "model-b" })]);
    await userEvent.click(openerOf(c));
    const bravo = optionsOf(c)[1];
    if (bravo === undefined) {
      throw new Error("no Bravo row");
    }
    await userEvent.click(bravo);
    await settle();
    expect(shownPanel(c)).toBe("main");
    expect(sent).toEqual([]);
  });

  it("Back and Escape return with no change, and a second Escape is the card's", async () => {
    echo();
    const c = mount([fallback({ configured: "model-b" })]);
    const outer = vi.fn();
    document.addEventListener("keydown", outer);
    try {
      await userEvent.click(openerOf(c));
      await userEvent.click(backOf(c));
      await settle();
      expect(shownPanel(c)).toBe("main");
      expect(document.activeElement).toBe(openerOf(c));

      await userEvent.click(openerOf(c));
      await userEvent.keyboard("{Escape}");
      await settle();
      expect(shownPanel(c)).toBe("main");
      expect(document.activeElement).toBe(openerOf(c));
      expect(outer, "the first Escape stays in the list").not.toHaveBeenCalled();

      await userEvent.keyboard("{Escape}");
      expect(outer, "the second Escape reaches the card's popup").toHaveBeenCalledOnce();
    } finally {
      document.removeEventListener("keydown", outer);
    }
    expect(sent).toEqual([]);
    expect(openerOf(c).getAttribute("aria-label")).toBe("Fallback: Bravo");
  });

  it("returns on Right arrow in a right-to-left page", async () => {
    document.documentElement.setAttribute("dir", "rtl");
    const c = mount([fallback()]);
    await userEvent.click(openerOf(c));
    await userEvent.keyboard("{ArrowLeft}");
    expect(shownPanel(c)).toBe("fallback");
    await userEvent.keyboard("{ArrowRight}");
    await settle();
    expect(shownPanel(c)).toBe("main");
  });

  it("slides along the inline axis and takes each panel's own height", async () => {
    const c = mount([fallback()]);
    const before = c.card.getBoundingClientRect();
    await userEvent.click(openerOf(c));

    const slides = panelOf(c).getAnimations();
    expect(slides.length, "the list slides in").toBeGreaterThan(0);
    expect(c.main.getAnimations().length, "the models slide out").toBeGreaterThan(0);
    const resize = c.card.getAnimations();
    expect(resize).toHaveLength(1);
    expect(resize[0]?.effect?.getTiming().duration).toBe(200);
    await settle();

    expect(c.card.clientHeight).toBeCloseTo(panelOf(c).getBoundingClientRect().height, 0);
    expect(
      c.card.getBoundingClientRect().bottom,
      "the edge nearest the pill stays put",
    ).toBeCloseTo(before.bottom, 0);

    await userEvent.click(backOf(c));
    await settle();
    const back = c.card.getBoundingClientRect();
    expect(c.card.clientHeight).toBeCloseTo(c.main.getBoundingClientRect().height, 0);
    expect(back.height).toBeCloseTo(before.height, 0);
    expect(back.bottom).toBeCloseTo(before.bottom, 0);
  });

  it("swaps at once under reduced motion", async () => {
    const real = window.matchMedia.bind(window);
    vi.spyOn(window, "matchMedia").mockImplementation((q: string) =>
      q.includes("prefers-reduced-motion")
        ? ({ ...real(q), matches: q.includes("reduce") } as MediaQueryList)
        : real(q),
    );
    // A row's hover fade is a CSS transition; the slide and the resize are script animations.
    const slides = (): Animation[] =>
      document.getAnimations().filter((a) => !(a instanceof CSSTransition));
    const c = mount([fallback()]);
    await userEvent.click(openerOf(c));
    expect(shownPanel(c)).toBe("fallback");
    expect(c.main.hidden).toBe(true);
    expect(slides()).toEqual([]);
    expect(c.card.clientHeight).toBeCloseTo(panelOf(c).getBoundingClientRect().height, 0);
    await userEvent.click(backOf(c));
    expect(panelOf(c).hidden).toBe(true);
    expect(slides()).toEqual([]);
  });

  it("a slot reset puts the first panel back at once, as the card closes", async () => {
    const c = mount([fallback()]);
    await userEvent.click(openerOf(c));
    c.slot.reset();
    expect(shownPanel(c)).toBe("main");
    expect(openerOf(c).getAttribute("aria-expanded")).toBe("false");
  });
});

describe("the Fallback row's writes", () => {
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
    const c = mount([fallback()]);
    await userEvent.click(openerOf(c));
    await userEvent.click(optionsOf(c)[1] as HTMLElement);
    await vi.waitFor(() => {
      expect(sent).toHaveLength(1);
    });
    expect(openerOf(c).getAttribute("aria-label")).toBe("Fallback: Bravo");
    await userEvent.click(openerOf(c));
    await userEvent.click(optionsOf(c)[2] as HTMLElement);
    release();

    await vi.waitFor(() => {
      expect(sent).toEqual([
        { model: "model-a", target: "model-b" },
        { model: "model-a", target: "model-c" },
      ]);
      expect(openerOf(c).getAttribute("aria-busy")).toBeNull();
    });
    expect(openerOf(c).getAttribute("aria-label")).toBe("Fallback: Charlie");
  });

  it("a write refused for no sign-in shows the sign-in words and puts the choice back", async () => {
    answer(409, {
      error: "kiro-cli cannot change model fallback now. Sign in to Kiro, then try again.",
      code: "unavailable",
    });
    const c = mount([fallback({ configured: "model-c" })]);
    await userEvent.click(openerOf(c));
    await userEvent.click(optionsOf(c)[1] as HTMLElement);
    await vi.waitFor(() => {
      expect(sent).toEqual([{ model: "model-a", target: "model-b" }]);
      expect(hintOf(c)).toBe(SIGN_IN_WORDS);
    });
    await vi.waitFor(() => {
      expect(openerOf(c).getAttribute("aria-label")).toBe("Fallback: Charlie");
    });
    expect(openerOf(c).disabled).toBe(true);
  });
});
