// Drift guard: `scrollMock` must stay TOTAL. A missing export fails no single suite but aborts
// the full run with an unattributed "error when mocking a module".
import { describe, it, expect, vi } from "vitest";

// The real `scroll.ts` reads these elements at import; an auto-creating Proxy answers any id.
vi.mock("../dom.js", () => ({
  $: new Proxy(
    {},
    {
      get: (_t, prop: string) => {
        const id = String(prop);
        let e = document.getElementById(id);
        if (e === null) {
          e = document.createElement(id === "scrollBottom" ? "button" : "div");
          e.id = id;
          if (id === "scrollBottom") {
            e.appendChild(document.createElement("span"));
          }
          document.body.appendChild(e);
        }
        return e;
      },
    },
  ),
  // Browser Mode links for real, so an export the graph imports must exist. The real body, because
  // a placeholder marks its host busy through it.
  setBusy: (el: Element, busy: boolean) => {
    if (busy) {
      el.setAttribute("aria-busy", "true");
    } else {
      el.removeAttribute("aria-busy");
    }
  },
}));

import { scrollMock } from "./scroll-mock.js";
import * as scroll from "../scroll.js";

// Types are erased at runtime, so this compares the value surface an ESM link resolves.
const real = Object.keys(scroll as Record<string, unknown>).sort();
const mocked = Object.keys(scrollMock).sort();

describe("the scroll.js mock helper stays total", () => {
  it("names every value scroll.ts exports", () => {
    expect(real.length, "the real module exported nothing; the import is wrong").toBeGreaterThan(0);
    const missing = real.filter((k) => !mocked.includes(k));
    expect(missing, `scroll-mock.ts is missing: ${missing.join(", ")}`).toEqual([]);
  });

  it("names nothing scroll.ts does not export, so a rename cannot hide behind it", () => {
    const extra = mocked.filter((k) => !real.includes(k));
    expect(extra, `scroll-mock.ts has stale entries: ${extra.join(", ")}`).toEqual([]);
  });
});
