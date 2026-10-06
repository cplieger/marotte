import { afterEach, describe, expect, it, vi } from "vitest";
import type * as Runs from "./actions/runs.js";
import type * as RunView from "./run-view.js";

const m = vi.hoisted(() => ({
  launched: [] as unknown[],
  opened: [] as string[],
}));

vi.mock("./actions/runs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Runs>()),
  launchRun: {
    name: "runs.launch",
    dispatch: vi.fn(
      (
        args: unknown,
        opts?: { onSuccess?: (d: { workflow_id: string; name: string }) => void },
      ) => {
        m.launched.push(args);
        opts?.onSuccess?.({ workflow_id: "wf_9", name: "agent-reviewer" });
        return Promise.resolve();
      },
    ),
  },
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
});
