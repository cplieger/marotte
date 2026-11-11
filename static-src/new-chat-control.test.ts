// The sidebar's New chat button answers its press before the create does: it shows the busy face from
// the dispatch to the answer, whichever door started the create, and comes back on a refusal. Real
// actions framework and real stylesheet; only the transport, the network seam, is faked.
import { describe, it, expect, vi, beforeEach, afterEach, beforeAll, afterAll } from "vitest";

vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock())),
}));
vi.mock("./transport.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  send: vi.fn(),
  newOpID: vi.fn(() => "op-test"),
}));

import { send as transportSend, type SendResult } from "./transport.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { createChat } from "./actions/chat.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { initNewChatControl } from "./new-chat-control.js";

const mockSend = vi.mocked(transportSend);

const header = {
  id: "c-minted",
  name: "New conversation",
  model: "auto",
  usage: { context_pct: 0, context_size: 0, credits: 0, last_turn_ms: 0, has_real_data: false },
  created_at: 0,
  updated_at: 0,
  turn_count: 0,
};

let style: HTMLStyleElement;
let btn: HTMLButtonElement;
let answer: (r: SendResult) => void;

/** `#new-chat` as `static/index.html` authors it: a glyph, then the label. */
function mountButton(): HTMLButtonElement {
  const wrap = document.createElement("div");
  wrap.className = "sidebar-actions";
  const b = document.createElement("button");
  b.type = "button";
  b.id = "new-chat";
  b.className = "sidebar-action-btn";
  const glyph = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  glyph.setAttribute("class", "ic-ui");
  glyph.setAttribute("viewBox", "0 0 24 24");
  b.append(glyph, document.createTextNode("New chat"));
  wrap.appendChild(b);
  document.body.appendChild(wrap);
  return b;
}

/** Where the label starts: the busy face must not move it. */
function labelLeft(b: HTMLButtonElement): number {
  const range = document.createRange();
  range.selectNodeContents(b.lastChild as Text);
  return range.getBoundingClientRect().left;
}

const busy = (b: HTMLButtonElement): boolean =>
  b.disabled && b.getAttribute("aria-busy") === "true";

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

beforeEach(() => {
  resetActionFramework();
  vi.clearAllMocks();
  mockSend.mockImplementation(
    () =>
      new Promise<SendResult>((resolve) => {
        answer = resolve;
      }),
  );
  btn = mountButton();
});

afterEach(() => {
  btn.parentElement?.remove();
});

const createOnPress = (): void => {
  void createChat.dispatch({ opID: "op-1" });
};

describe("the New chat button while a create is in flight", () => {
  it("shows the busy face in the click, before the create answers", () => {
    initNewChatControl(btn, createOnPress);
    btn.click();
    expect(mockSend).toHaveBeenCalledTimes(1);
    expect(busy(btn), "disabled and aria-busy right after the click").toBe(true);
  });

  it("paints a spinning ring in the glyph's place and keeps the label where it was", () => {
    initNewChatControl(btn, createOnPress);
    const idleLeft = labelLeft(btn);
    btn.click();
    const glyph = btn.querySelector("svg") as SVGElement;
    const ring = getComputedStyle(btn, "::before");
    expect(getComputedStyle(glyph).display, "the pencil gives its place to the ring").toBe("none");
    expect(ring.content).not.toBe("none");
    expect(ring.animationName).toBe("vk-spin");
    expect(labelLeft(btn)).toBeCloseTo(idleLeft, 0);
    // The busy face, not the unavailable one: 40-a11y.css keeps a busy control at full opacity.
    expect(getComputedStyle(btn).opacity).toBe("1");
  });

  it("comes back when the create answers", async () => {
    initNewChatControl(btn, createOnPress);
    btn.click();
    answer({ ok: true, status: 200, body: { ok: true, chat: header } });
    await vi.waitFor(() => {
      expect(busy(btn)).toBe(false);
    });
    expect(btn.disabled).toBe(false);
    expect(btn.hasAttribute("aria-busy")).toBe(false);
    expect(getComputedStyle(btn.querySelector("svg") as SVGElement).display).not.toBe("none");
  });

  it("comes back when the server refuses the create", async () => {
    initNewChatControl(btn, createOnPress);
    btn.click();
    answer({ ok: false, status: 400, error: "refused" });
    await vi.waitFor(() => {
      expect(busy(btn)).toBe(false);
    });
    expect(btn.disabled).toBe(false);
  });

  it("shows a create started from another door, the shortcut or the empty composer", () => {
    initNewChatControl(btn, createOnPress);
    void createChat.dispatch({ opID: "op-2" });
    expect(busy(btn)).toBe(true);
  });
});
