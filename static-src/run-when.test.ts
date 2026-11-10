import { describe, it, expect, vi, beforeEach, afterEach, beforeAll, afterAll } from "vitest";
import type * as Toast from "./toast.js";

vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("./__test-helpers__/toast-mock.js")).toastMock(),
}));

import { join as joinKey } from "@cplieger/keyenc";
import { buildEventForm, buildRuleList, eventState, ruleLine } from "./run-when.js";
import { buildRunDefaultsPicker } from "./run-defaults-picker.js";
import { formatStamp } from "./schedule-picker.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { dismissGap } from "./actions/launch-rules.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import type { EventSpec, LaunchRuleStatus, LaunchRuleView } from "./launch-rule-types.js";
import type { ConfiguredForge } from "./wire/types.gen.js";
import {
  restoreBrowserLanguages,
  setBrowserLanguages,
} from "./__test-helpers__/browser-languages.js";

const mockFetch = vi.fn();

beforeEach(() => {
  setBrowserLanguages(["fr-FR"]);
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});
afterEach(() => {
  restoreBrowserLanguages();
  document.body.replaceChildren();
});

const weekly: EventSpec = {
  connection: "github:github.com",
  repo: "cplieger/animap-source",
  kind: "run_concluded",
  filter: { workflow: ".github/workflows/weekly.yaml", branch: "main", result: ["success"] },
};

function eventRule(
  over: Partial<LaunchRuleView> = {},
  status: LaunchRuleStatus = {},
): LaunchRuleView {
  return {
    id: "e1",
    source: "bundled://weekly-questions",
    enabled: true,
    when: { event: weekly },
    status,
    cursor: { baselined: true },
    held: 0,
    ...over,
  };
}

const local = (h: number, m: number): string => new Date(2026, 10, 9, h, m).toISOString();

const github: ConfiguredForge = {
  id: "github:github.com",
  kind: "github",
  host: "github.com",
  username: "cplieger",
  account: "a1",
  status: "connected",
};

function flush(): Promise<void> {
  return new Promise((r) => setTimeout(r, 0));
}

function eventForm(over: Partial<Parameters<typeof buildEventForm>[0]> = {}): HTMLElement {
  const form = buildEventForm({
    forges: [github],
    declared: { repo: "", run_id: "", event_id: "", tag: "string" },
    autoApprove: false,
    picks: buildRunDefaultsPicker({ models: [], initial: {} }),
    onSave: () => Promise.resolve(null),
    onClose: () => undefined,
    onOpenPermissions: () => undefined,
    onOpenSources: () => undefined,
    ...over,
  });
  document.body.appendChild(form);
  return form;
}

function pick(form: HTMLElement, sel: string, value: string): void {
  const s = form.querySelector<HTMLSelectElement | HTMLInputElement>(sel);
  if (s === null) {
    throw new Error(`no ${sel}`);
  }
  s.value = value;
  s.dispatchEvent(new Event(s instanceof HTMLSelectElement ? "change" : "input"));
}

describe("ruleLine", () => {
  it("summarises an event rule", () => {
    const line = ruleLine(
      eventRule(
        {},
        {
          last_run_at: local(11, 33),
          last_event: joinKey("e1", "cplieger/animap-source", "run", "41414016210"),
          last_status: "started",
          checked_at: local(11, 55),
        },
      ),
      { autoApprove: false, declared: ["run_id", "event_id"] },
    );
    expect(
      line.startsWith(
        "When cplieger/animap-source's weekly.yaml finishes with success on main · last fired ",
      ),
    ).toBe(true);
    expect(line.endsWith("(run 41414016210) · watching, checked 11:55")).toBe(true);
  });

  it("shows auto-approve on an event rule while the setting is on", () => {
    const v = eventRule();
    expect(ruleLine(v, { autoApprove: true, declared: ["event_id"] })).toBe(
      "When cplieger/animap-source's weekly.yaml finishes with success on main · watching · auto-approve on",
    );
    expect(ruleLine(v, { autoApprove: false, declared: ["event_id"] })).not.toContain(
      "auto-approve",
    );
  });

  it("names the event by id, never title", () => {
    const labeled = eventRule(
      { when: { event: { ...weekly, kind: "issue_labeled", filter: { label: "report:fast" } } } },
      {
        last_run_at: local(9, 0),
        last_event: joinKey(
          "e1",
          "cplieger/animap",
          "issue",
          "12",
          "labeled",
          "20261104T101530123Z",
        ),
      },
    );
    expect(ruleLine(labeled, { autoApprove: false, declared: ["event_id"] })).toContain(
      " (#12) · ",
    );
    const run = eventRule(
      {},
      {
        last_run_at: local(9, 0),
        last_event: joinKey("e1", "cplieger/animap-source", "run", "41"),
      },
    );
    expect(ruleLine(run, { autoApprove: false, declared: ["event_id"] })).toContain(" (run 41) · ");
  });

  it("warns when the workflow does not declare event_id", () => {
    expect(ruleLine(eventRule(), { autoApprove: false, declared: ["run_id"] })).toMatch(
      / · no event_id$/,
    );
    expect(ruleLine(eventRule(), { autoApprove: false, declared: ["event_id"] })).not.toContain(
      "event_id",
    );
  });
});

describe("eventState", () => {
  it("renders each state", () => {
    const cases: [string, LaunchRuleView, string, boolean][] = [
      ["paused", eventRule({ enabled: false }), "paused", false],
      [
        "blocked",
        eventRule({}, { blocked: "this rule's workflow file is gone" }),
        "blocked: this rule's workflow file is gone. Edit the rule",
        true,
      ],
      [
        "backed off",
        eventRule(
          {},
          { paused: "paused for the forge budget: core has 412 requests left, resumes 15:29" },
        ),
        "paused for the forge budget: core has 412 requests left, resumes 15:29",
        false,
      ],
      ["launching", eventRule({}, { row_text: "starting run wf_1" }), "starting run wf_1", false],
      ["baseline", eventRule({ cursor: { baselined: false } }), "taking a baseline", false],
      [
        "baseline, walk stopped short",
        eventRule(
          { cursor: { baselined: false } },
          {
            baseline_stop:
              "the last walk stopped after 3 pages: the forge's page limit was reached",
          },
        ),
        "taking a baseline; the last walk stopped after 3 pages: the forge's page limit was reached",
        false,
      ],
      [
        "queue full",
        eventRule({}, { row_text: "queue full: 20 events held, newer events wait at the forge" }),
        "queue full: 20 events held, newer events wait at the forge",
        true,
      ],
      ["watching", eventRule(), "watching", false],
      [
        "watching, checked",
        eventRule({}, { checked_at: local(13, 55) }),
        "watching, checked 13:55",
        false,
      ],
      [
        "forge error",
        eventRule({ cursor: { baselined: true, blocked: "connection signed out" } }),
        "connection signed out",
        true,
      ],
    ];
    for (const [name, v, text, error] of cases) {
      expect(eventState(v), name).toEqual({ text, error });
    }
  });

  it("adds a segment per gap", () => {
    const v = eventRule(
      {},
      {
        gaps: [
          { from: local(2, 10), to: local(9, 0), cause: "outage", created_before: local(1, 0) },
          { from: local(2, 10), to: local(9, 0), cause: "aged_out", run_id: 41414016210 },
          { from: local(9, 0), to: local(9, 5), cause: "result_window", omitted_at_least: 45 },
        ],
      },
    );
    const line = ruleLine(v, { autoApprove: false, declared: ["event_id"] });
    expect(line).toContain(
      ` · gap: no complete check between ${formatStamp(local(2, 10))} and ${formatStamp(local(9, 0))}; runs created before ${formatStamp(local(1, 0))} that ended in that time were not checked · `,
    );
    expect(line).toContain(
      " · run 41414016210 was still running at its last check and was not seen to end",
    );
    expect(line).toContain(" · the forge did not list at least 45 runs created since ");
  });
});

describe("buildEventForm", () => {
  it("names triggered runs in the unattended note", () => {
    const form = eventForm({ autoApprove: true });
    expect(form.querySelector(".sched-note-body")?.textContent).toContain(
      "Nobody is watching a triggered run.",
    );
    expect(form.querySelector(".sched-note-state")?.textContent).toBe(
      "Auto-approve is on for every scheduled and triggered run, not only this one.",
    );
  });

  it("lists event-set inputs read-only", () => {
    const form = eventForm();
    expect(form.querySelector(".rw-event-inputs")?.textContent).toBe(
      "Set by the event: repo, run_id, event_id",
    );
    const fields = [...form.querySelectorAll<HTMLInputElement>(".rw-inputs input")];
    expect(fields.map((f) => f.getAttribute("aria-label"))).toEqual(["Input tag"]);
    pick(form, ".rw-kind", "issue_labeled");
    expect(form.querySelector(".rw-event-inputs")?.textContent).toBe(
      "Set by the event: repo, event_id",
    );
  });

  it("warns when the workflow does not declare event_id", () => {
    const without = eventForm({ declared: { repo: "" } });
    expect(without.querySelector(".rw-no-event-id")?.textContent).toBe(
      "This workflow does not declare event_id, so it cannot skip a repeated event",
    );
    expect(eventForm().querySelector(".rw-no-event-id")).toBeNull();
  });

  it("keeps a cleared input cleared across a When change", async () => {
    const onSave = vi.fn<Parameters<typeof buildEventForm>[0]["onSave"]>(() =>
      Promise.resolve(null),
    );
    const form = eventForm({ rule: eventRule({ inputs: { tag: "v1" } }), onSave });
    const tag = (): HTMLInputElement =>
      form.querySelector<HTMLInputElement>('.rw-inputs input[aria-label="Input tag"]')!;
    expect(tag().value).toBe("v1");
    tag().value = "";
    pick(form, ".rw-kind", "pr_labeled");
    expect(tag().value).toBe("");
    form.querySelector<HTMLButtonElement>(".rw-save")!.click();
    await flush();
    expect(onSave.mock.calls[0]?.[0]).not.toHaveProperty("inputs");
  });

  it("says to connect a forge when there is no connection", () => {
    const form = eventForm({ forges: [] });
    expect(form.textContent).toContain("Connect a forge in Sources first");
    expect(form.querySelector(".rw-repo")).toBeNull();
  });

  it("names a connection that needs a new sign-in, with the way to Sources", () => {
    const onOpenSources = vi.fn();
    const gitlab: ConfiguredForge = {
      id: "gitlab:gitlab.com",
      kind: "gitlab",
      host: "gitlab.com",
      username: "cplieger",
      account: "a2",
      status: "reconnect",
    };
    const form = eventForm({ forges: [github, gitlab], onOpenSources });
    const options = [...form.querySelectorAll<HTMLOptionElement>(".rw-conn option")];
    expect(options.map((o) => [o.value, o.disabled])).toEqual([
      ["github:github.com", false],
      ["gitlab:gitlab.com", true],
    ]);
    expect(options[1]?.textContent).toBe("gitlab.com · cplieger (sign in again in Sources)");
    form.querySelector<HTMLButtonElement>(".rw-open-sources")!.click();
    expect(onOpenSources).toHaveBeenCalledTimes(1);
  });

  it("says to sign in again when the only connection needs it", () => {
    const form = eventForm({ forges: [{ ...github, status: "reconnect" }] });
    expect(form.textContent).not.toContain("Connect a forge");
    expect(form.textContent).toContain("github.com · cplieger needs a new sign-in");
    expect(form.querySelector(".rw-open-sources")).not.toBeNull();
    expect(form.querySelector(".rw-repo")).toBeNull();
  });

  it("says the opened-by field takes one login per line", () => {
    const form = eventForm();
    pick(form, ".rw-kind", "pr_opened");
    const row = form.querySelector(".rw-authors")?.closest(".sched-row");
    expect(row?.querySelector(".sched-hint")?.textContent).toBe(
      "One login per line, such as renovate[bot]",
    );
  });

  it("writes every field label in sentence case", () => {
    const form = eventForm();
    const labels = (): string[] =>
      [...form.querySelectorAll(".sched-label:not(.rw-input-name)")].map(
        (l) => l.textContent ?? "",
      );
    const seen = new Set<string>();
    for (const kind of ["issue_labeled", "pr_opened", "run_concluded"]) {
      pick(form, ".rw-kind", kind);
      for (const l of labels()) {
        seen.add(l);
      }
    }
    expect([...seen].filter((l) => l.charAt(0) !== l.charAt(0).toUpperCase())).toEqual([]);
  });

  it("puts the trust and catch-up hints under the When fields", () => {
    const form = eventForm();
    const order = (a: Element, b: Element): boolean =>
      (a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0;
    const trust = form.querySelector(".rw-trust")!;
    const windowLine = form.querySelector(".rw-window")!;
    const kindFields = form.querySelector(".rw-kind-fields")!;
    const interval = form.querySelector(".rw-interval")!;
    expect(order(kindFields, trust) && order(trust, interval)).toBe(true);
    expect(order(kindFields, windowLine) && order(windowLine, interval)).toBe(true);
  });

  it("shows an input's description as hint text that stays once a value is typed", () => {
    const form = eventForm({ declared: { event_id: "", tag: "The release tag to stamp" } });
    const tag = form.querySelector<HTMLInputElement>('.rw-inputs input[aria-label="Input tag"]')!;
    tag.value = "v1";
    expect(tag.placeholder).toBe("");
    expect(tag.closest(".rw-input-field")?.querySelector(".sched-hint")?.textContent).toBe(
      "The release tag to stamp",
    );
    expect(form.querySelector(".rw-inputs-title")?.textContent).toBe("Inputs");
  });

  it("keeps the form open on a refusal", async () => {
    const onSave = vi.fn(() => Promise.resolve("`run_id` is set by the event"));
    const onClose = vi.fn();
    const form = eventForm({ onSave, onClose });
    pick(form, ".rw-repo", " o/r ");
    form.querySelector<HTMLInputElement>(".rw-inputs input")!.value = "v1";
    form.querySelector<HTMLButtonElement>(".rw-save")!.click();
    await flush();
    expect(onSave).toHaveBeenCalledWith(
      {
        enabled: true,
        event: {
          connection: "github:github.com",
          repo: "o/r",
          kind: "run_concluded",
          filter: { result: ["success"] },
          interval_min: 5,
        },
        inputs: { tag: "v1" },
      },
      {},
    );
    expect(form.querySelector(".rw-error")?.textContent).toBe("`run_id` is set by the event");
    expect(form.isConnected).toBe(true);
    expect(onClose).not.toHaveBeenCalled();
  });
});

describe("buildRuleList", () => {
  it("dismisses a gap", async () => {
    const gap = { from: local(2, 10), to: local(9, 0), cause: "outage" as const };
    const v = eventRule({}, { gaps: [gap] });
    mockFetch.mockResolvedValue(new Response(JSON.stringify(eventRule()), { status: 200 }));
    const list = buildRuleList({
      rules: [v],
      autoApprove: false,
      declared: ["event_id"],
      onToggle: () => undefined,
      onEdit: () => undefined,
      onDelete: () => undefined,
      onDismissGap: (r) => dismissGap.dispatch(r.id),
      onAddClock: () => undefined,
      onAddEvent: () => undefined,
    });
    document.body.appendChild(list);
    expect(list.querySelector(".rw-line")?.textContent).toContain("gap: ");
    list.querySelector<HTMLButtonElement>(".rw-dismiss-gap")!.click();
    await vi.waitFor(() => {
      expect(list.querySelector(".rw-dismiss-gap")).toBeNull();
    });
    const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect([init.method, url]).toEqual(["POST", "/api/launch-rules/e1/dismiss-gap"]);
    expect(list.querySelector(".rw-line")?.textContent).not.toContain("gap: ");
  });
});

describe("the Run when… surfaces in real layout", () => {
  let style: HTMLStyleElement;

  beforeAll(() => {
    style = mountAppCSS();
  });

  afterAll(() => {
    style.remove();
  });

  afterEach(() => {
    delete document.documentElement.dataset["theme"];
  });

  /** The form inside the popup's own surface, at the phone's inner width. */
  function inPopup(form: HTMLElement, width = 302): HTMLElement {
    const panel = document.createElement("div");
    panel.className = "sched-popup";
    panel.style.inlineSize = `${String(width)}px`;
    panel.append(form);
    document.body.append(panel);
    return form;
  }

  function painted(colour: string, under: readonly string[] = []): [number, number, number] {
    const canvas = document.createElement("canvas");
    canvas.width = 1;
    canvas.height = 1;
    const cx = canvas.getContext("2d", { willReadFrequently: true })!;
    for (const layer of [...under, colour]) {
      cx.fillStyle = layer;
      cx.fillRect(0, 0, 1, 1);
    }
    const d = cx.getImageData(0, 0, 1, 1).data;
    return [d[0] ?? 0, d[1] ?? 0, d[2] ?? 0];
  }

  function ratio(a: [number, number, number], b: [number, number, number]): number {
    const lum = (c: [number, number, number]): number => {
      const ch = (v: number): number => {
        const x = v / 255;
        return x <= 0.03928 ? x / 12.92 : Math.pow((x + 0.055) / 1.055, 2.4);
      };
      return 0.2126 * ch(c[0]) + 0.7152 * ch(c[1]) + 0.0722 * ch(c[2]);
    };
    const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x) as [number, number];
    return (hi + 0.05) / (lo + 0.05);
  }

  /** The text's ink against every background from the page down to it, composited. */
  function contrastOf(text: Element): number {
    const layers: string[] = [];
    for (let cur: Element | null = text; cur !== null; cur = cur.parentElement) {
      layers.unshift(getComputedStyle(cur).backgroundColor);
    }
    const bg = painted(layers.at(-1)!, ["#fff", ...layers.slice(0, -1)]);
    return ratio(painted(getComputedStyle(text).color, ["#fff", ...layers]), bg);
  }

  it("heads Add a rule in the neutral ink, not the caution title's", () => {
    const list = buildRuleList({
      rules: [],
      autoApprove: false,
      declared: undefined,
      onToggle: () => undefined,
      onEdit: () => undefined,
      onDelete: () => undefined,
      onDismissGap: () => Promise.resolve(null),
      onAddClock: () => undefined,
      onAddEvent: () => undefined,
    });
    inPopup(list);
    const heading = [...list.querySelectorAll(".rw-add > *")].find(
      (n) => n.textContent === "Add a rule",
    )!;
    const probe = document.createElement("span");
    probe.style.color = "var(--c-text-secondary)";
    list.append(probe);
    expect(getComputedStyle(heading).color).toBe(getComputedStyle(probe).color);
  });

  it.each(["dark", "light"])("paints a rule's error line in the error ink (%s)", (t) => {
    document.documentElement.dataset["theme"] = t;
    const list = buildRuleList({
      rules: [eventRule({}, { blocked: "the connection was removed." })],
      autoApprove: false,
      declared: ["event_id"],
      onToggle: () => undefined,
      onEdit: () => undefined,
      onDelete: () => undefined,
      onDismissGap: () => Promise.resolve(null),
      onAddClock: () => undefined,
      onAddEvent: () => undefined,
    });
    inPopup(list);
    const line = list.querySelector(".rw-line")!;
    expect(line.textContent).toContain("blocked: the connection was removed");
    const probe = document.createElement("span");
    probe.style.color = "var(--c-red)";
    list.append(probe);
    expect(getComputedStyle(line).color).toBe(getComputedStyle(probe).color);
  });

  it("pauses a rule with the house switch, its 36x20 track the target", () => {
    const toggled: boolean[] = [];
    const list = buildRuleList({
      rules: [eventRule()],
      autoApprove: false,
      declared: ["event_id"],
      onToggle: (_v, on) => toggled.push(on),
      onEdit: () => undefined,
      onDelete: () => undefined,
      onDismissGap: () => Promise.resolve(null),
      onAddClock: () => undefined,
      onAddEvent: () => undefined,
    });
    inPopup(list);
    const track = list.querySelector<HTMLElement>(".rw-rule label.toggle > .toggle-slider")!;
    const box = track.getBoundingClientRect();
    expect([box.width, box.height]).toEqual([36, 20]);
    track.click();
    expect(toggled).toEqual([false]);
  });

  it("starts the rule's switch at the rule text's edge", () => {
    const list = buildRuleList({
      rules: [eventRule()],
      autoApprove: false,
      declared: ["event_id"],
      onToggle: () => undefined,
      onEdit: () => undefined,
      onDelete: () => undefined,
      onDismissGap: () => Promise.resolve(null),
      onAddClock: () => undefined,
      onAddEvent: () => undefined,
    });
    inPopup(list);
    const line = list.querySelector<HTMLElement>(".rw-rule .rw-line")!;
    const track = list.querySelector<HTMLElement>(".rw-rule .toggle-slider")!;
    expect(track.getBoundingClientRect().left).toBe(line.getBoundingClientRect().left);
  });

  it.each(["dark", "light"])("keeps the form's text at 4.5:1 or better in the %s theme", (t) => {
    document.documentElement.dataset["theme"] = t;
    const form = inPopup(eventForm({ declared: { tag: "string" } }));
    const texts = [
      form.querySelector(".rw-no-event-id")!,
      form.querySelector(".sched-note-title")!,
      ...form.querySelectorAll(".sched-label, .sched-hint, .sched-note-body"),
    ];
    const low = texts
      .map((n) => [n.textContent?.slice(0, 24), Number(contrastOf(n).toFixed(2))] as const)
      .filter(([, r]) => r < 4.5);
    expect(low).toEqual([]);
  });

  it("gives the form's fields one height and one type size", () => {
    const form = inPopup(eventForm({ declared: { event_id: "", tag: "string" } }));
    const fields = [
      ...form.querySelectorAll<HTMLElement>(
        "input:not([type='checkbox']), select, .rw-form textarea",
      ),
    ].filter((f) => f.getClientRects().length > 0);
    const heights = new Set(fields.map((f) => f.getBoundingClientRect().height));
    const sizes = new Set(fields.map((f) => getComputedStyle(f).fontSize));
    expect([...heights], fields.map((f) => f.className).join(", ")).toHaveLength(1);
    expect([...sizes]).toHaveLength(1);
    const label = getComputedStyle(form.querySelector(".sched-label")!);
    const unit = [...form.querySelectorAll(".sched-row > *")].find(
      (n) => n.textContent === "minutes",
    )!;
    expect([getComputedStyle(unit).fontSize, getComputedStyle(unit).color]).toEqual([
      label.fontSize,
      label.color,
    ]);
  });

  it("marks the More disclosure with the house chevron, turned down when open", () => {
    const form = inPopup(eventForm());
    const more = form.querySelector<HTMLDetailsElement>(".rw-more")!;
    const chevron = more.querySelector<HTMLElement>(":scope > summary > .disclosure-chevron");
    expect(chevron).not.toBeNull();
    expect(chevron!.getBoundingClientRect().width).toBeGreaterThan(0);
    const turn = (): string => getComputedStyle(chevron!).getPropertyValue("--chev-turn").trim();
    expect(turn()).toBe("-90deg");
    more.open = true;
    expect(turn()).toBe("0deg");
  });

  it.each([302, 488])(
    "lays every input out the same way at a %dpx panel: name, then field, then its hint",
    (width) => {
      const form = inPopup(
        eventForm({ declared: { event_id: "", max_iterations: "string", prompt: "prompt" } }),
        width,
      );
      const rows = [...form.querySelectorAll<HTMLElement>(".rw-input-field")];
      expect(rows).toHaveLength(2);
      const lefts = new Set<number>();
      for (const row of rows) {
        const name = row.querySelector(".rw-input-name")!.getBoundingClientRect();
        const field = row.querySelector("input")!.getBoundingClientRect();
        const hint = row.querySelector(".sched-hint")!.getBoundingClientRect();
        expect(field.top, "the field under its name").toBeGreaterThanOrEqual(name.bottom);
        expect(hint.top, "the hint under its field").toBeGreaterThanOrEqual(field.bottom);
        lefts.add(Math.round(field.left));
      }
      expect([...lefts], "one inline start for every field").toHaveLength(1);
    },
  );

  it("keeps each hold field beside its label on a phone", () => {
    const form = inPopup(eventForm());
    form.querySelector<HTMLDetailsElement>(".rw-more")!.open = true;
    for (const sel of [".rw-hold-policy", ".rw-hold-cap"]) {
      const field = form.querySelector(sel)!;
      const label = field.parentElement!.querySelector(".sched-label")!.getBoundingClientRect();
      const box = field.getBoundingClientRect();
      const mid = (r: DOMRect): number => r.top + r.height / 2;
      expect(Math.abs(mid(box) - mid(label)), sel).toBeLessThanOrEqual(box.height / 2);
    }
  });
});
