// A hidden surface (a background tab's view, a closed modal or PR dialog) leaves the page while hidden, so a
// page-wide walk such as a password manager's form scan skips its fields, and comes back exactly as it left.

import { beforeAll, describe, expect, it } from "vitest";
import { bindLoadingState, defineAction } from "./actions/index.js";
import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { byId, park, queryAll, unpark } from "./dom.js";
import { closeModal, createParkedDialog, initAllModals, openModal } from "./modals.js";
import { showOnlyView } from "./view-swap.js";
import { initClockSetting } from "./clock-setting.js";
import { adoptLinkGuard, formPayloadLink, installLinkGuard } from "./link-guard.js";
import type { EffectiveSettings } from "./wire/types.gen.js";

const frame = (): Promise<void> =>
  new Promise((r) => {
    requestAnimationFrame(() => {
      r();
    });
  });

const inPage = (id: string): boolean => document.getElementById(id) !== null;

/** What a form scan over the page finds: the fields a TreeWalker from `documentElement` reaches. */
const pageFieldIds = (): string[] =>
  [...document.querySelectorAll("input, textarea, select")].map((f) => f.id);

const show = (id: string): void => {
  showOnlyView(byId(id));
};

/** Scroll `el` to `top` the way a reader does: the offset lands and its scroll event fires. */
async function scrollTo(el: HTMLElement, top: number): Promise<void> {
  el.append(
    Object.assign(document.createElement("div"), { style: "flex: none; block-size: 4000px" }),
  );
  el.scrollTop = top;
  await frame();
  await frame();
  expect(el.scrollTop, "the scroller took the offset before the surface hid").toBe(top);
}

async function closed(content: HTMLElement): Promise<void> {
  closeModal(content as HTMLDivElement);
  await expect.poll(() => document.contains(content)).toBe(false);
}

beforeAll(() => {
  mountAppCSS();
  const page = new DOMParser().parseFromString(indexHtml, "text/html");
  document.body.replaceChildren(
    ...[...page.body.childNodes].map((n) => document.importNode(n, true)),
  );
  initAllModals();
  show("chat-view");
});

describe("the page while the chat is shown", () => {
  it("holds no field outside the chat view", () => {
    const chat = byId("chat-view");
    const outside = [...document.querySelectorAll("input, textarea, select")].filter(
      (f) => !chat.contains(f),
    );
    expect(outside.map((f) => f.id)).toEqual([]);
    expect(pageFieldIds()).toContain("prompt-input");
  });

  it("keeps the chat and web views in the page while hidden", () => {
    show("settings-view");
    expect(inPage("chat-view")).toBe(true);
    expect(inPage("web-view")).toBe(true);
    show("chat-view");
  });

  it("still finds a parked element by id and by selector", () => {
    expect(inPage("flag-telemetry")).toBe(false);
    expect(byId("flag-telemetry").id).toBe("flag-telemetry");
    expect(queryAll("[data-tab-view]").map((v) => v.id)).toContain("settings-view");
  });
});

describe("a tab view hidden and shown again", () => {
  it("keeps the settings view's values, open details, scroll, listeners and observers", async () => {
    show("settings-view");
    const view = byId("settings-view");
    const toggle = byId<HTMLInputElement>("flag-telemetry");
    const select = byId<HTMLSelectElement>("memory-mode");
    const steering = byId<HTMLTextAreaElement>("steering-input");
    const details = byId<HTMLDetailsElement>("native-policy-explain-box");
    toggle.checked = !toggle.checked;
    const checked = toggle.checked;
    const option = select.options[select.options.length - 1]?.value ?? "";
    select.value = option;
    steering.value = "Answer in French.";
    details.open = true;
    let clicks = 0;
    toggle.addEventListener("click", () => {
      clicks++;
    });
    const shell = view.querySelector<HTMLElement>(".page-shell");
    if (shell === null) {
      throw new Error("the settings view has no .page-shell");
    }
    const resized: number[] = [];
    new ResizeObserver((entries) => {
      for (const e of entries) {
        resized.push(e.contentRect.width);
      }
    }).observe(shell);
    const spot = Object.assign(document.createElement("i"), {
      style: "position: fixed; inset: 0 auto auto 0; inline-size: 4px; block-size: 4px",
    });
    view.append(spot);
    const seen: boolean[] = [];
    new IntersectionObserver((entries) => {
      for (const e of entries) {
        seen.push(e.isIntersecting);
      }
    }).observe(spot);
    await scrollTo(view, 600);

    show("chat-view");
    expect(inPage("settings-view")).toBe(false);
    resized.length = 0;
    seen.length = 0;
    show("settings-view");
    await frame();
    await frame();

    expect(inPage("settings-view")).toBe(true);
    expect(toggle.checked).toBe(checked);
    expect(select.value).toBe(option);
    expect(steering.value).toBe("Answer in French.");
    expect(details.open).toBe(true);
    expect(view.scrollTop).toBe(600);
    toggle.click();
    expect(clicks).toBe(1);
    shell.style.inlineSize = "321px";
    await expect.poll(() => resized.length).toBeGreaterThan(0);
    spot.style.insetBlockStart = "-5000px";
    await expect.poll(() => seen).toContain(false);
    show("chat-view");
  });

  it("keeps a loading binding on a parked button alive", async () => {
    show("settings-view");
    const btn = byId<HTMLButtonElement>("engine-restart");
    let finish = (): void => undefined;
    const restart = defineAction<undefined, undefined>({
      name: "surface-parking.restart",
      run: () =>
        new Promise((r) => {
          finish = () => {
            r(undefined);
          };
        }),
    });
    const stop = bindLoadingState("surface-parking.restart", btn);
    show("chat-view");
    const done = restart.dispatch(undefined);
    expect(btn.disabled).toBe(true);
    finish();
    await done;
    show("settings-view");
    expect(btn.disabled).toBe(false);
    stop();
    show("chat-view");
  });

  it("keeps the editor's text and caret", () => {
    show("editor-view");
    const text = byId<HTMLTextAreaElement>("editor-content");
    const goTo = byId<HTMLInputElement>("editor-goto-input");
    text.value = "line one\nline two";
    text.setSelectionRange(5, 8);
    goTo.value = "2";
    show("chat-view");
    expect(inPage("editor-view")).toBe(false);
    show("editor-view");
    expect(text.value).toBe("line one\nline two");
    expect([text.selectionStart, text.selectionEnd]).toEqual([5, 8]);
    expect(goTo.value).toBe("2");
    show("chat-view");
  });

  it("keeps the file browser's path and list scroll", async () => {
    show("files-view");
    const path = byId<HTMLInputElement>("fb-path");
    path.value = "/workspace/marotte";
    const list = byId("files-view").querySelector<HTMLElement>(".fb-list-wrap");
    expect(list).not.toBeNull();
    await scrollTo(list as HTMLElement, 900);
    show("chat-view");
    expect(inPage("files-view")).toBe(false);
    show("files-view");
    expect(path.value).toBe("/workspace/marotte");
    expect(list?.scrollTop).toBe(900);
    await frame();
    show("chat-view");
    show("files-view");
    expect(list?.scrollTop, "a second hide keeps the restored offset").toBe(900);
    show("chat-view");
  });

  it("keeps the run view's draft and body scroll", async () => {
    show("run-view");
    const draft = byId<HTMLTextAreaElement>("run-composer-input");
    draft.value = "Skip the flaky test.";
    const body = byId("run-view").querySelector<HTMLElement>(":scope > .page-content");
    expect(body).not.toBeNull();
    await scrollTo(body as HTMLElement, 700);
    show("chat-view");
    expect(inPage("run-view")).toBe(false);
    show("run-view");
    expect(draft.value).toBe("Skip the flaky test.");
    expect(body?.scrollTop).toBe(700);
    show("chat-view");
  });
});

describe("a modal closed and opened again", () => {
  const cases: readonly (readonly [string, string, string])[] = [
    ["login-modal", "modal-region", "eu-west-3"],
    ["tool-modal", "tool-sort", ""],
    ["mcp-modal", "mcp-remote-name", "linear"],
    ["git-output-modal", "", ""],
    ["filepicker-modal", "filepicker-path", "/workspace/uploads"],
    ["shortcuts-modal", "", ""],
  ];

  for (const [modalId, fieldId, value] of cases) {
    it(`leaves the page while closed and keeps its state: ${modalId}`, async () => {
      const content = byId<HTMLDivElement>(modalId);
      expect(document.contains(content)).toBe(false);
      const opener = byId<HTMLButtonElement>("settings-btn");
      opener.focus();
      openModal(content);
      expect(document.contains(content)).toBe(true);

      const field = fieldId === "" ? null : byId<HTMLInputElement | HTMLSelectElement>(fieldId);
      const want =
        field instanceof HTMLSelectElement
          ? (field.options[field.options.length - 1]?.value ?? "")
          : value;
      if (field !== null) {
        field.value = want;
      }
      const scroller = content.closest("dialog") as HTMLDialogElement;
      await closed(content);
      expect(document.activeElement).toBe(opener);

      openModal(content);
      expect(scroller.open).toBe(true);
      expect(field?.value ?? "").toBe(want);
      await closed(content);
    });
  }
});

describe("a PR dialog closed and opened again", () => {
  it("leaves the page while closed and keeps the create form's fields", async () => {
    const dlg = byId<HTMLDialogElement>("pr-create-dialog");
    expect(document.contains(dlg)).toBe(false);
    const ctl = createParkedDialog(dlg, {});
    const opener = byId<HTMLButtonElement>("settings-btn");
    opener.focus();
    ctl.open();
    byId<HTMLInputElement>("pr-title").value = "fix: keep hidden surfaces off the page";
    byId<HTMLInputElement>("pr-draft").checked = true;
    ctl.close();
    await expect.poll(() => document.contains(dlg)).toBe(false);
    expect(document.activeElement).toBe(opener);

    ctl.open();
    expect(dlg.open).toBe(true);
    expect(byId<HTMLInputElement>("pr-title").value).toBe("fix: keep hidden surfaces off the page");
    expect(byId<HTMLInputElement>("pr-draft").checked).toBe(true);
    ctl.close();
    await expect.poll(() => document.contains(dlg)).toBe(false);
    ctl.dispose();
  });

  it("parks the merge dialog too", () => {
    expect(document.contains(byId("pr-merge-dialog"))).toBe(false);
  });
});

describe("park", () => {
  it("leaves a surface with playing media in the page, since leaving it would pause the media", () => {
    const host = document.createElement("div");
    const audio = document.createElement("audio");
    Object.defineProperty(audio, "paused", { get: () => false });
    host.append(audio);
    document.body.append(host);
    park(host);
    expect(host.ownerDocument).toBe(document);
    expect(document.contains(host)).toBe(true);
    host.remove();
  });

  it("puts a surface back where it was", () => {
    const before = document.createElement("i");
    const surface = document.createElement("section");
    const after = document.createElement("b");
    document.body.append(before, surface, after);
    park(surface);
    expect(surface.isConnected).toBe(true);
    expect(document.contains(surface)).toBe(false);
    unpark(surface, () => undefined);
    expect(before.nextSibling).toBe(surface);
    expect(surface.nextSibling).toBe(after);
    before.remove();
    surface.remove();
    after.remove();
  });
});

describe("a page-wide redraw", () => {
  const parkedHost = (): HTMLElement => {
    const host = document.createElement("section");
    document.body.append(host);
    return host;
  };

  it("reaches a clock on a parked surface", () => {
    initClockSetting();
    const host = parkedHost();
    const t = document.createElement("time");
    t.dateTime = "2026-01-02T15:04:05Z";
    t.setAttribute("data-clock", "short");
    t.textContent = "stale";
    host.append(t);
    park(host);
    const select = byId<HTMLSelectElement>("clock-choice");
    select.value = select.value === "h12" ? "h23" : "h12";
    select.dispatchEvent(new Event("change", { bubbles: true }));
    expect(t.textContent).not.toBe("stale");
    unpark(host, () => undefined);
    host.remove();
  });

  it("re-forms a guarded link on a parked surface", async () => {
    const stop = installLinkGuard();
    adoptLinkGuard({ guard_payload_links: true } as EffectiveSettings);
    const host = parkedHost();
    const anchor = document.createElement("a");
    anchor.textContent = "link";
    host.append(anchor);
    formPayloadLink(anchor, "https://example.com/?d=aGVsbG8gd29ybGQ");
    park(host);
    adoptLinkGuard({ guard_payload_links: false } as EffectiveSettings);
    await expect.poll(() => host.querySelector("a[href]")?.textContent).toBe("link");
    stop();
    unpark(host, () => undefined);
    host.remove();
  });
});
