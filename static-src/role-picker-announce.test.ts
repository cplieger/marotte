// A mode switch on a live chat is announced: the new mode's name reaches a polite
// live region (`role="status"` or `aria-live="polite"`), because the pill changes
// in place and a screen-reader user otherwise hears nothing. The app does not meet
// this yet, so the announcement case is recorded as failing and the fix flips it;
// the control case proves the switch itself reached the pill.
import { describe, it, expect, vi, afterAll, beforeEach, afterEach } from "vitest";

import indexHtml from "../static/index.html?raw";
import { makeSession } from "./__test-helpers__/model.js";
import { setActive, setCurrentMode, setSessions } from "./store.js";

function liveRegionText(): string {
  return [...document.querySelectorAll('[role="status"], [aria-live="polite"]')]
    .map((el) => el.textContent ?? "")
    .join("\n");
}

// The shipped page, mounted before the picker's module graph loads: that graph
// resolves page elements at import. One picker for the file, because
// `initRolePicker` installs a lasting effect and a second call would add another.
const host = document.createElement("div");
host.innerHTML = new DOMParser().parseFromString(indexHtml, "text/html").body.innerHTML;
document.body.appendChild(host);
const { initRolePicker } = await import("./role-picker.js");
initRolePicker();

afterAll(() => {
  host.remove();
});

beforeEach(() => {
  setSessions([makeSession({ id: "c-live", current_mode_id: "vibe" })]);
  setActive("c-live");
});

afterEach(() => {
  setSessions([]);
  setActive("");
});

describe("switching a live chat's mode", () => {
  it("shows the new mode on the pill", () => {
    setCurrentMode("c-live", "spec");

    expect(document.getElementById("role-pill-label")?.textContent).toBe("Spec");
  });

  it.fails("announces the new mode through a polite live region", async () => {
    setCurrentMode("c-live", "spec");

    await vi.waitFor(
      () => {
        expect(liveRegionText()).toContain("Spec");
      },
      { timeout: 1000 },
    );
  });
});
