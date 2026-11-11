import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import { announce, mountLiveRegions, _resetForTest } from "./announce.js";
import { createModal, _resetForTest as resetModal } from "./modal.js";

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  _resetForTest();
  resetModal();
  vi.useRealTimers();
  document.body.innerHTML = "";
});

function liveRegions(root: ParentNode = document): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>("[aria-live]")];
}

function politeIn(host: HTMLElement): HTMLElement {
  const found = [...host.children].find(
    (c): c is HTMLElement => c instanceof HTMLElement && c.getAttribute("aria-live") === "polite",
  );
  expect(found).toBeDefined();
  return found!;
}

/** Every element node added or removed anywhere in the document while `fn` runs. */
function elementsAddedOrRemoved(fn: () => void): Node[] {
  const observer = new MutationObserver(() => {
    /* records are taken synchronously below */
  });
  observer.observe(document, { childList: true, subtree: true });
  try {
    fn();
    return observer
      .takeRecords()
      .flatMap((r) => [...r.addedNodes, ...r.removedNodes])
      .filter((n) => n.nodeType === Node.ELEMENT_NODE);
  } finally {
    observer.disconnect();
  }
}

describe("announce into a modal's own pair", () => {
  it("lands in the open modal's and the root's polite regions and adds or moves no region, then or when the text lands", () => {
    mountLiveRegions(document.body);
    const modal = createModal(document.createElement("div"));
    modal.open();
    const target = politeIn(modal.el);

    expect(
      elementsAddedOrRemoved(() => {
        announce("x");
      }),
    ).toEqual([]);
    expect(
      elementsAddedOrRemoved(() => {
        vi.advanceTimersByTime(100);
      }),
    ).toEqual([]);
    expect(target.textContent).toBe("x");
    expect(politeIn(document.body).textContent).toBe("x");
  });

  it("lands in the root, cleared with the modal's, when the modal closes before the text lands", () => {
    mountLiveRegions(document.body);
    const modal = createModal(document.createElement("div"));
    modal.open();
    const inModal = politeIn(modal.el);
    const root = politeIn(document.body);
    announce("earlier");
    vi.advanceTimersByTime(100);
    expect(root.textContent).toBe("earlier");

    announce("saved");
    vi.advanceTimersByTime(50);
    // The landing runs in the close's own task (fake timers): the close's record is not yet delivered.
    modal.dispose();
    vi.advanceTimersByTime(50);
    expect(inModal.textContent).toBe("");
    expect(root.textContent).toBe("saved");
  });

  it("starts over, clearing every live region, when a modal opens before the text lands", () => {
    mountLiveRegions(document.body);
    const root = politeIn(document.body);
    const modal = createModal(document.createElement("div"));
    const inModal = politeIn(modal.el);
    inModal.textContent = "same";

    announce("same");
    vi.advanceTimersByTime(50);
    modal.open();
    vi.advanceTimersByTime(50);
    expect(inModal.textContent).toBe("");
    expect(root.textContent).toBe("");

    vi.advanceTimersByTime(100);
    expect(inModal.textContent).toBe("same");
    expect(root.textContent).toBe("same");
  });
});

describe("announce with no mounted pair", () => {
  it("writes nothing, creates nothing and arms nothing", () => {
    const added = elementsAddedOrRemoved(() => {
      announce("lost");
      announce("lost too", "assertive");
    });
    expect(added).toEqual([]);
    expect(liveRegions()).toEqual([]);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("never writes into a closed dialog's pair", () => {
    const modal = createModal(document.createElement("div"));
    modal.open();
    const inModal = politeIn(modal.el);
    modal.dispose();
    document.body.appendChild(modal.el);

    announce("nowhere");
    vi.runAllTimers();
    expect(inModal.textContent).toBe("");
  });

  it("writes nothing once the app clears the root host's children", () => {
    mountLiveRegions(document.body);
    const stale = politeIn(document.body);
    document.body.innerHTML = "";

    announce("gone");
    vi.runAllTimers();
    expect(liveRegions()).toEqual([]);
    expect(stale.textContent).toBe("");
  });
});

describe("mountLiveRegions", () => {
  it("mounts one polite and one assertive region, and a second call on the same host keeps them", () => {
    mountLiveRegions(document.body);
    const first = liveRegions();
    mountLiveRegions(document.body);
    const second = liveRegions();

    expect(second).toHaveLength(2);
    expect(second[0]).toBe(first[0]);
    expect(second[1]).toBe(first[1]);
    expect(second.map((r) => r.getAttribute("aria-live"))).toEqual(["polite", "assertive"]);
    expect(second.map((r) => r.getAttribute("role"))).toEqual(["status", "alert"]);
    expect(second.every((r) => r.parentElement === document.body)).toBe(true);
    expect(second.every((r) => r.classList.contains("uip-visually-hidden"))).toBe(true);
  });

  it("remounts a pair the app cleared out of the host", () => {
    mountLiveRegions(document.body);
    document.body.innerHTML = "";
    mountLiveRegions(document.body);

    expect(liveRegions()).toHaveLength(2);
    announce("back");
    vi.advanceTimersByTime(100);
    expect(politeIn(document.body).textContent).toBe("back");
  });

  it.each(["polite", "assertive"] as const)(
    "replaces the whole pair when only the %s region left the host",
    (lost) => {
      mountLiveRegions(document.body);
      const kept = lost === "polite" ? "assertive" : "polite";
      const survivor = document.querySelector<HTMLElement>(`[aria-live="${kept}"]`)!;
      document.querySelector(`[aria-live="${lost}"]`)!.remove();
      mountLiveRegions(document.body);

      const now = liveRegions();
      expect(now.map((r) => r.getAttribute("aria-live"))).toEqual(["polite", "assertive"]);
      expect(now).not.toContain(survivor);
    },
  );

  it("makes the newest mount the root, and its release hands the root back", () => {
    const appRoot = document.createElement("main");
    document.body.appendChild(appRoot);
    mountLiveRegions(document.body);
    const releaseApp = mountLiveRegions(appRoot);

    announce("here");
    vi.advanceTimersByTime(100);
    expect(politeIn(appRoot).textContent).toBe("here");
    expect(politeIn(document.body).textContent).toBe("");

    releaseApp();
    expect(liveRegions(appRoot)).toEqual([]);
    announce("back");
    vi.advanceTimersByTime(100);
    expect(politeIn(document.body).textContent).toBe("back");
  });

  it("removes the pair once every mount on the host is released", () => {
    const first = mountLiveRegions(document.body);
    const second = mountLiveRegions(document.body);
    first();
    expect(liveRegions()).toHaveLength(2);
    second();
    expect(liveRegions()).toEqual([]);

    announce("gone");
    expect(vi.getTimerCount()).toBe(0);
  });
});
