// The prompt form's NATIVE-submission backstop.
import { describe, it, expect } from "vitest";
import indexHtml from "../static/index.html?raw";

describe("the prompt form's native-submission backstop", () => {
  it("is declared in the served markup, with no action to navigate to", () => {
    const doc = new DOMParser().parseFromString(indexHtml, "text/html");
    const form = doc.getElementById("prompt-form");
    expect(form).not.toBeNull();
    expect(form?.getAttribute("method")).toBe("dialog");
    // An `action` is what a dialog-method submission ignores and every other method navigates to,
    // so its absence is half the contract.
    expect(form?.hasAttribute("action")).toBe(false);
  });

  it("fires submit and navigates nowhere when nothing cancels it", async () => {
    // The form goes in a CHILD iframe, because that is what makes a navigation observable: the
    // frame's second `load` event is the navigation, and the test's own document survives to report
    // it.
    const frame = document.createElement("iframe");
    frame.srcdoc =
      '<!doctype html><form id="f" method="dialog"><button type="submit">go</button></form>';
    const firstLoad = new Promise<void>((r) => {
      frame.addEventListener("load", () => r(), { once: true });
    });
    document.body.append(frame);
    await firstLoad;

    let navigations = 0;
    frame.addEventListener("load", () => {
      navigations += 1;
    });

    const doc = frame.contentDocument;
    if (doc === null) {
      throw new Error("the child frame has no document");
    }
    const form = doc.getElementById("f") as HTMLFormElement;
    expect(form.method).toBe("dialog");

    let fired = 0;
    let cancelled: boolean | null = null;
    form.addEventListener("submit", (e) => {
      fired += 1;
      // Deliberately NOT prevented: this is the path production takes only when its own handler
      // failed to.
      cancelled = e.defaultPrevented;
    });
    form.requestSubmit();
    // Long enough for a real navigation of an in-memory srcdoc frame to land: the `get` spelling of
    // this same form reaches the load handler here.
    await new Promise((r) => setTimeout(r, 250));

    expect(fired).toBe(1);
    expect(cancelled).toBe(false);
    expect(navigations).toBe(0);
    // The form is still the one we submitted, in the document we submitted it in — a navigation
    // would have replaced both.
    expect(frame.contentDocument?.getElementById("f")).toBe(form);

    frame.remove();
  });
});
