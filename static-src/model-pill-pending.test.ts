// ---------------------------------------------------------------------------
// The model pill's PENDING badge: a header's `pending_model` goes in and the
// button's `.pending` class, tooltip and accessible name come out.
//
// A pure read of the header, so a pick made on ANOTHER device and a pick applied at
// a turn's close both reach the badge with no local queue in between — which is why
// the whole chain runs here (`context-ui.ts` lifts the field, `status.ts` writes all
// three channels) and nothing is staged: no case depends on the catalog, which is
// the input `effort-pill.test.ts` stages for the tier half of the same button.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import type { Session } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";

// Every element `updateContextBar` writes, because it paints the whole bar in one
// pass and the registry's getters throw on a missing id.
document.body.innerHTML = `
  <button id="switch-model-btn">
    <span id="ctx-model-pill"></span><span id="ctx-effort-pill" class="hidden"></span>
  </button>
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

/** One chat, with an EMPTY transcript in the shape the store holds one: the counts
 *  beside the ring are a walk over `turn_order`. */
function session(id: string, over: Partial<Session> = {}): Session {
  return {
    ...makeSession({
      id,
      name: id,
      model: "claude-opus-5",
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

/** Mount one chat as the active one and let its paint land: `updateContextBar`
 *  coalesces into a rAF, so a read taken in the same task sees the previous one. */
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
    await paint(session("c-pending", { pending_model: "claude-sonnet-5" }));

    expect($.switchModelBtn.classList.contains("pending")).toBe(true);
    // The two channels beside the class, which is what a reader and a screen reader
    // actually get: the tooltip states WHEN the pick applies, and an aria-label wins
    // over a button's own text, so it is the only path the selection is announced by.
    expect($.switchModelBtn.getAttribute("data-tooltip")).toBe(
      "Switch to claude sonnet 5 after current turn",
    );
    expect($.switchModelBtn.getAttribute("aria-label")).toBe(
      "Switch model, currently claude opus 5, switching to claude sonnet 5 after this turn",
    );
  });

  it("clears the badge when a header arrives with the field empty", async () => {
    await paint(session("c-applied", { pending_model: "claude-sonnet-5" }));
    expect($.switchModelBtn.classList.contains("pending")).toBe(true);

    // What a pick APPLIED at the turn's close looks like on the wire: the same chat,
    // the new model, and the field empty. Every device clears on that frame, so the
    // badge may not survive it.
    await paint(session("c-applied", { model: "claude-sonnet-5", pending_model: "" }));

    expect($.switchModelBtn.classList.contains("pending")).toBe(false);
    expect($.switchModelBtn.getAttribute("data-tooltip")).toBe("Switch model");
    expect($.switchModelBtn.getAttribute("aria-label")).toBe(
      "Switch model, currently claude sonnet 5",
    );
  });

  it("marks nothing when the header carries no field at all", async () => {
    // An older server, and every chat before its first pick: the field is absent
    // rather than empty, which the reader must not see as a pick in flight.
    await paint(session("c-none"));

    expect($.switchModelBtn.classList.contains("pending")).toBe(false);
    expect($.switchModelBtn.getAttribute("data-tooltip")).toBe("Switch model");
  });
});
