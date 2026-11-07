import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type * as Toast from "./toast.js";

vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("./__test-helpers__/toast-mock.js")).toastMock(),
}));

import { join as joinKey } from "@cplieger/keyenc";
import { buildEventForm, buildRuleList, eventState, ruleLine } from "./run-when.js";
import { buildRunDefaultsPicker } from "./run-defaults-picker.js";
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
          { from: local(2, 10), to: local(9, 0), cause: "outage" },
          { from: local(2, 10), to: local(9, 0), cause: "aged_out", run_id: 41414016210 },
          { from: local(9, 0), to: local(9, 5), cause: "result_window", omitted_at_least: 45 },
        ],
      },
    );
    const line = ruleLine(v, { autoApprove: false, declared: ["event_id"] });
    expect(line).toContain(" · gap: no complete check between ");
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

  it("says to connect a forge when no connection is usable", () => {
    const form = eventForm({ forges: [{ ...github, status: "reconnect" }] });
    expect(form.textContent).toContain("Connect a forge in Sources first");
    expect(form.querySelector(".rw-repo")).toBeNull();
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
