// The OS file dialog both Upload buttons open. Headless Chromium keeps a detached input's pending
// chooser alive, so the WebKit loss cannot be reproduced here; these pin the shape that avoids it
// (the input connected, its `change` listener live) plus a deliberate invariant: `click()` runs
// synchronously in the press's own dispatch, where user activation is certain in every engine.
import {
  afterAll,
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
  type MockInstance,
} from "vitest";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

const toastError = vi.hoisted(() => vi.fn());
const toastInfo = vi.hoisted(() => vi.fn());
vi.mock("./toast.js", () => ({ error: toastError, info: toastInfo }));

import { openFileDialog } from "./file-dialog.js";

/** What the dialog's input looked like at the moment `click()` ran. */
interface AtClick {
  input: HTMLInputElement;
  connected: boolean;
  parent: Element | null;
  rect: DOMRect;
}

let clicks: AtClick[] = [];
let clickSpy: MockInstance<HTMLInputElement["click"]>;
/** Files the fake OS dialog picks during `click()`, or null to leave the dialog open. */
let pickOnClick: File[] | null = null;

function pick(input: HTMLInputElement, files: File[]): void {
  const dt = new DataTransfer();
  for (const f of files) {
    dt.items.add(f);
  }
  input.files = dt.files;
  input.dispatchEvent(new Event("change", { bubbles: true }));
}

function lastInput(): HTMLInputElement {
  const last = clicks.at(-1);
  if (last === undefined) {
    throw new Error("the dialog was never opened");
  }
  return last.input;
}

function fileInputs(): HTMLInputElement[] {
  return [...document.querySelectorAll<HTMLInputElement>('input[type="file"]')];
}

let appCSS: HTMLStyleElement;
beforeAll(() => {
  appCSS = mountAppCSS();
});
afterAll(() => {
  appCSS.remove();
});

beforeEach(() => {
  clicks = [];
  pickOnClick = null;
  toastError.mockClear();
  toastInfo.mockClear();
  clickSpy = vi.spyOn(HTMLInputElement.prototype, "click").mockImplementation(function (
    this: HTMLInputElement,
  ) {
    clicks.push({
      input: this,
      connected: this.isConnected,
      parent: this.parentElement,
      rect: this.getBoundingClientRect(),
    });
    if (pickOnClick !== null) {
      pick(this, pickOnClick);
    }
  });
});

afterEach(() => {
  clickSpy.mockRestore();
  document.body.replaceChildren();
});

function pressOpening(onPick: (files: FileList) => void): HTMLButtonElement {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.addEventListener("click", () => {
    openFileDialog(btn, onPick);
  });
  document.body.appendChild(btn);
  return btn;
}

describe("the dialog's input at the moment it opens", () => {
  it("is in the document", () => {
    pressOpening(vi.fn()).click();
    expect(clicks).toHaveLength(1);
    expect(clicks[0]?.connected).toBe(true);
  });

  it("already has its change listener, so a pick answered during click() is not lost", () => {
    const onPick = vi.fn();
    const file = new File(["x"], "a.txt");
    pickOnClick = [file];
    pressOpening(onPick).click();
    expect(onPick).toHaveBeenCalledOnce();
    expect(onPick.mock.calls[0]?.[0]?.[0]).toBe(file);
  });

  it("is clicked inside the press's own dispatch, before the press returns", () => {
    const btn = pressOpening(vi.fn());
    btn.click();
    // No await between the press and this line: the click must already have run.
    expect(clickSpy).toHaveBeenCalledOnce();
  });

  it("is a multi-select file input", () => {
    pressOpening(vi.fn()).click();
    expect(lastInput().type).toBe("file");
    expect(lastInput().multiple).toBe(true);
  });

  it("sits inside the trigger's open modal dialog, which makes the rest of the page inert", () => {
    const dlg = document.createElement("dialog");
    document.body.appendChild(dlg);
    const btn = document.createElement("button");
    btn.addEventListener("click", () => {
      openFileDialog(btn, vi.fn());
    });
    dlg.appendChild(btn);
    dlg.showModal();
    btn.click();
    expect(clicks[0]?.parent).toBe(dlg);
    dlg.close();
  });

  it("lies over the pressed button, where iPadOS anchors its source menu", () => {
    const btn = pressOpening(vi.fn());
    btn.style.margin = "120px 0 0 200px";
    btn.click();
    expectOver(clicks[0]?.rect, btn.getBoundingClientRect());
  });

  it("lies over the pressed button inside a modal dialog caught mid-way through its entry", () => {
    const dlg = document.createElement("dialog");
    dlg.className = "uip-modal";
    // A frozen frame of the entry: the transform makes the dialog the input's containing block.
    dlg.style.animation = "none";
    dlg.style.transform = "scale(0.5) translateY(3rem)";
    const panel = document.createElement("div");
    panel.className = "uip-modal-dialog";
    const btn = document.createElement("button");
    btn.textContent = "Upload";
    btn.addEventListener("click", () => {
      openFileDialog(btn, vi.fn());
    });
    panel.append(document.createElement("p"), btn);
    dlg.appendChild(panel);
    document.body.appendChild(dlg);
    dlg.showModal();
    btn.click();
    expectOver(clicks[0]?.rect, btn.getBoundingClientRect());
    dlg.close();
  });
});

/** The input's box at `click()` covers the button's centre. */
function expectOver(input: DOMRect | undefined, btn: DOMRect): void {
  expect(input).toBeDefined();
  if (input === undefined) {
    return;
  }
  const cx = btn.left + btn.width / 2;
  const cy = btn.top + btn.height / 2;
  expect(input.left).toBeLessThanOrEqual(cx);
  expect(input.right).toBeGreaterThanOrEqual(cx);
  expect(input.top).toBeLessThanOrEqual(cy);
  expect(input.bottom).toBeGreaterThanOrEqual(cy);
  expect(input.width).toBeGreaterThan(1);
}

describe("after the dialog answers", () => {
  it("hands over the picked files and leaves no input behind", () => {
    const onPick = vi.fn();
    pressOpening(onPick).click();
    const a = new File(["a"], "a.txt");
    const b = new File(["b"], "b.txt");
    pick(lastInput(), [a, b]);
    expect(onPick).toHaveBeenCalledOnce();
    const files = onPick.mock.calls[0]?.[0] as FileList;
    expect(Array.from(files)).toEqual([a, b]);
    expect(fileInputs()).toEqual([]);
  });

  it("says no file was attached on a cancel, and leaves no input behind", () => {
    const onPick = vi.fn();
    pressOpening(onPick).click();
    lastInput().dispatchEvent(new Event("cancel", { bubbles: true }));
    expect(onPick).not.toHaveBeenCalled();
    expect(toastInfo).toHaveBeenCalledExactlyOnceWith("No file was attached.");
    expect(toastError).not.toHaveBeenCalled();
    expect(fileInputs()).toEqual([]);
  });

  it("shows no notice when files are picked", () => {
    pressOpening(vi.fn()).click();
    pick(lastInput(), [new File(["x"], "a.txt")]);
    expect(toastInfo).not.toHaveBeenCalled();
    expect(toastError).not.toHaveBeenCalled();
  });

  it("removes a dialog's input that never answered when the next one opens", () => {
    const btn = pressOpening(vi.fn());
    btn.click();
    const first = lastInput();
    btn.click();
    expect(first.isConnected).toBe(false);
    expect(fileInputs()).toEqual([lastInput()]);
  });

  it("shows a failure handling the pick as an error toast with its reason", () => {
    const onPick = vi.fn(() => {
      throw new Error("DataTransfer is not a constructor");
    });
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    pressOpening(onPick).click();
    pick(lastInput(), [new File(["x"], "a.txt")]);
    expect(toastError).toHaveBeenCalledWith("Upload failed: DataTransfer is not a constructor");
    consoleError.mockRestore();
  });
});
