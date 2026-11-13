import { describe, it, expect, vi } from "vitest";
import { errorNotice, FB_ROOT, listNotice } from "./files-shared.js";

describe("errorNotice", () => {
  it("offers Retry and the way to the root off the root", () => {
    const retry = vi.fn();
    const toRoot = vi.fn();
    const notice = errorNotice("Load failed", "/uploads", retry, toRoot);
    expect(notice.querySelector("span.fb-meta")?.textContent).toBe("Load failed");
    const buttons = [...notice.querySelectorAll("button")];
    expect(buttons.map((b) => b.textContent)).toEqual(["Retry", "Go to root"]);
    buttons[1]?.click();
    expect(toRoot).toHaveBeenCalledOnce();
    expect(retry).not.toHaveBeenCalled();
    buttons[0]?.click();
    expect(retry).toHaveBeenCalledOnce();
  });

  it("offers Retry alone at the root", () => {
    const notice = errorNotice("Load failed", FB_ROOT, vi.fn(), vi.fn());
    expect([...notice.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["Retry"]);
  });
});

describe("listNotice", () => {
  it("renders a status line that is not a row, with no button when given no actions", () => {
    const notice = listNotice("Something went wrong");
    expect(notice.classList.contains("fb-notice")).toBe(true);
    expect(notice.classList.contains("fb-row")).toBe(false);
    expect(notice.querySelector("span.fb-meta")?.textContent).toBe("Something went wrong");
    expect(notice.querySelector("button")).toBeNull();
  });

  it("renders one button per action, in order, each running its own action", () => {
    const retry = vi.fn();
    const home = vi.fn();
    const notice = listNotice("Load failed", [
      { label: "Retry", run: retry },
      { label: "Go to root", run: home },
    ]);
    const buttons = [...notice.querySelectorAll("button")];
    expect(buttons.map((b) => b.textContent)).toEqual(["Retry", "Go to root"]);
    expect(buttons.map((b) => b.className)).toEqual(["btn-small", "btn-small"]);
    expect(buttons.map((b) => b.type)).toEqual(["button", "button"]);
    buttons[1]?.click();
    expect(home).toHaveBeenCalledOnce();
    expect(retry).not.toHaveBeenCalled();
  });

  it("sets the message as text, never as markup", () => {
    const notice = listNotice("<b>x</b>");
    expect(notice.querySelector("b")).toBeNull();
    expect(notice.textContent).toBe("<b>x</b>");
  });
});
