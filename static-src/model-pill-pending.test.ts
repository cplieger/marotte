import { describe, it, expect } from "vitest";
import type { Session } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";

// Every element updateContextBar writes: the registry's getters throw on a missing id.
document.body.innerHTML = `
  <button id="switch-model-btn">
    <span id="ctx-model-pill"></span><span id="ctx-effort-pill" class="hidden"></span>
  </button>
  <span id="context-indicator"></span>
  <span id="context-ring-fill"></span>
  <span id="context-ring-wedge"></span>
  <span id="context-label"></span>
  <span id="ctx-tokens"></span>
  <span id="ctx-credits"></span>
  <span id="ctx-turns"></span>
  <span id="ctx-last-turn"></span>
  <span id="ctx-entries"></span>
  <span id="ctx-tools"></span>
  <span id="ctx-metering"></span>`;

const store = await import("./store.js");
const { refreshContextUI } = await import("./context-ui.js");
const { $ } = await import("./dom.js");

function session(id: string, over: Partial<Session> = {}): Session {
  return {
    ...makeSession({
      id,
      name: id,
      model: "model-a-5",
      effort: "",
      effort_levels: [],
      usage: {
        context_pct: 0,
        context_size: 200_000,
        credits: 0,
        last_turn_ms: 0,
        has_real_data: false,
      },
    }),
    ...over,
  };
}

/** updateContextBar coalesces into a rAF, so wait for the paint before reading. */
async function paint(s: Session): Promise<void> {
  store.setSessions([s]);
  store.setActive(s.id);
  refreshContextUI(s);
  await new Promise((resolve) => {
    requestAnimationFrame(() => {
      resolve(undefined);
    });
  });
}

describe("the model pill's pending badge", () => {
  it("marks the button while the header carries a pending pick", async () => {
    await paint(session("c-pending", { pending_model: "model-b-5" }));

    expect($.switchModelBtn.classList.contains("pending")).toBe(true);
    // The tooltip is the accessible name, because an icon-only pill shows no model name to hover for.
    const name = "Switch model, currently model a 5, switching to model b 5 after this turn";
    expect($.switchModelBtn.getAttribute("data-tooltip")).toBe(name);
    expect($.switchModelBtn.getAttribute("aria-label")).toBe(name);
  });

  it("clears the badge when a header arrives with the field empty", async () => {
    await paint(session("c-applied", { pending_model: "model-b-5" }));
    expect($.switchModelBtn.classList.contains("pending")).toBe(true);

    await paint(session("c-applied", { model: "model-b-5", pending_model: "" }));

    expect($.switchModelBtn.classList.contains("pending")).toBe(false);
    expect($.switchModelBtn.getAttribute("data-tooltip")).toBe("Switch model, currently model b 5");
    expect($.switchModelBtn.getAttribute("aria-label")).toBe("Switch model, currently model b 5");
  });

  it("marks nothing when the header carries no field at all", async () => {
    await paint(session("c-none"));

    expect($.switchModelBtn.classList.contains("pending")).toBe(false);
    expect($.switchModelBtn.getAttribute("data-tooltip")).toBe("Switch model, currently model a 5");
  });
});
