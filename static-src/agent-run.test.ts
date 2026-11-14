import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import { expectAnchored, resizeViewport } from "./__test-helpers__/anchored-panel.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type * as Runs from "./actions/runs.js";
import type * as RunView from "./run-view.js";
import type * as Toast from "./toast.js";

const m = vi.hoisted(() => ({
  launched: [] as unknown[],
  opened: [] as string[],
  warnings: undefined as string[] | undefined,
  notices: [] as [string, string][],
}));

vi.mock("./actions/runs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Runs>()),
  launchRun: {
    name: "runs.launch",
    dispatch: vi.fn(
      (
        args: unknown,
        opts?: {
          onSuccess?: (d: { workflow_id: string; name: string; warnings?: string[] }) => void;
        },
      ) => {
        m.launched.push(args);
        opts?.onSuccess?.({
          workflow_id: "wf_9",
          name: "agent-reviewer",
          ...(m.warnings === undefined ? {} : { warnings: m.warnings }),
        });
        return Promise.resolve();
      },
    ),
  },
}));

vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  notice: vi.fn((message: string, level: string) => {
    m.notices.push([message, level]);
    return () => undefined;
  }),
}));

vi.mock("./run-view.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunView>()),
  openRunView: vi.fn((id: string) => {
    m.opened.push(id);
    return Promise.resolve();
  }),
}));

import { agentRunButton } from "./agent-run.js";

afterEach(() => {
  m.launched.length = 0;
  m.opened.length = 0;
  m.notices.length = 0;
  m.warnings = undefined;
  document.body.replaceChildren();
});

function mountButton(name: string): HTMLButtonElement {
  const btn = agentRunButton(name);
  if (btn === null) {
    throw new Error(`no Run button for ${name}`);
  }
  document.body.append(btn);
  return btn;
}

function openForm(name: string): HTMLFormElement {
  mountButton(name).click();
  const form = document.querySelector<HTMLFormElement>(".agent-run-form");
  if (form === null) {
    throw new Error("the prompt form did not open");
  }
  return form;
}

function openPanels(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>(".sched-popup")].filter((p) =>
    p.classList.contains("is-open"),
  );
}

function pressEscape(): void {
  document.activeElement?.dispatchEvent(
    new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
  );
}

describe("the agent Run control", () => {
  it("offers no button for a name KAS cannot address", () => {
    expect(agentRunButton("code reviewer")).toBeNull();
    expect(agentRunButton("a/b")).toBeNull();
    expect(agentRunButton("")).toBeNull();
  });

  it("launches the agent as an agent:// run with the typed prompt", async () => {
    const form = openForm("reviewer");
    const field = form.querySelector("textarea");
    if (field === null) {
      throw new Error("no prompt field");
    }
    field.value = "  check the diff  ";
    form.requestSubmit();
    expect(m.launched).toEqual([
      { source: "agent://reviewer", inputs: { prompt: "check the diff" } },
    ]);
    await vi.waitFor(() => {
      expect(m.opened).toEqual(["wf_9"]);
    });
  });

  it("shows the launch's warnings, and no toast when there are none", () => {
    const submit = (): void => {
      const form = openForm("reviewer");
      const field = form.querySelector("textarea");
      if (field === null) {
        throw new Error("no prompt field");
      }
      field.value = "check the diff";
      form.requestSubmit();
    };
    submit();
    expect(m.notices).toEqual([]);
    document.body.replaceChildren();
    m.warnings = ["[workflow] step 'a' modelId 'm-x' is not a served model", "second advisory"];
    submit();
    expect(m.notices).toEqual([[m.warnings.join("\n"), "warning"]]);
  });

  it("sends nothing for a blank prompt", () => {
    const form = openForm("reviewer");
    const field = form.querySelector("textarea");
    if (field === null) {
      throw new Error("no prompt field");
    }
    field.value = "   ";
    form.requestSubmit();
    expect(m.launched).toEqual([]);
  });

  it("announces a named dialog and moves focus into its prompt", () => {
    const btn = mountButton("reviewer");
    btn.click();
    const panel = document.querySelector<HTMLElement>(".sched-popup");
    expect(panel?.getAttribute("role")).toBe("dialog");
    expect(panel?.getAttribute("aria-label")).toBe("Run reviewer");
    expect(btn.getAttribute("aria-haspopup")).toBe("dialog");
    expect(btn.getAttribute("aria-expanded")).toBe("true");
    expect(document.activeElement).toBe(panel?.querySelector("textarea"));
  });

  it("returns focus to the Run button when Escape dismisses it", async () => {
    const btn = mountButton("reviewer");
    btn.click();
    await vi.waitFor(() => {
      pressEscape();
      expect(btn.getAttribute("aria-expanded")).toBe("false");
    });
    expect(document.activeElement).toBe(btn);
    expect(openPanels()).toEqual([]);
  });

  it("returns focus to the Run button after a launch", () => {
    const btn = mountButton("reviewer");
    btn.click();
    const form = document.querySelector<HTMLFormElement>(".agent-run-form");
    const field = form?.querySelector("textarea");
    if (form == null || field == null) {
      throw new Error("the prompt form did not open");
    }
    field.value = "check the diff";
    form.requestSubmit();
    expect(document.activeElement).toBe(btn);
    expect(btn.getAttribute("aria-expanded")).toBe("false");
  });

  it("reopens the same panel after a dismissal instead of stacking a new one", async () => {
    const btn = mountButton("reviewer");
    btn.click();
    await vi.waitFor(() => {
      pressEscape();
      expect(btn.getAttribute("aria-expanded")).toBe("false");
    });
    btn.click();
    expect(btn.getAttribute("aria-expanded")).toBe("true");
    expect(document.querySelectorAll(".sched-popup")).toHaveLength(1);
  });

  it("closes the prompt, and leaves no panel behind, when its row is rebuilt under it", async () => {
    const row = document.createElement("div");
    document.body.append(row);
    const btn = agentRunButton("reviewer")!;
    row.append(btn);
    btn.click();
    expect(openPanels()).toHaveLength(1);
    row.replaceChildren(agentRunButton("reviewer")!);
    await vi.waitFor(() => {
      expect(document.querySelectorAll(".sched-popup")).toHaveLength(0);
    });
  });
});

describe("the agent Run panel's placement", () => {
  let style: HTMLStyleElement;
  let entry: { readonly width: number; readonly height: number } | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
    style = mountAppCSS();
  });

  afterAll(async () => {
    style.remove();
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  // A row near the window's bottom edge: the panel must open above it rather than off-screen.
  describe.each([
    [390, 844],
    [1440, 900],
  ])("at %dx%d", (width, height) => {
    it("opens the prompt anchored to its Run button", async () => {
      await resizeViewport(width, height);
      const row = document.createElement("div");
      row.style.cssText = "position: absolute; inset-inline-end: 8px; inset-block-end: 8px";
      document.body.append(row);
      const btn = agentRunButton("reviewer")!;
      row.append(btn);
      btn.click();
      const panel = document.querySelector<HTMLElement>(".sched-popup.is-open")!;
      expectAnchored(panel, btn);
    });
  });
});
