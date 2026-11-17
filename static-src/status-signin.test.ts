// The account button carries the Kiro sign-in's state beside the connection's: a still amber mark while it fails, a
// still red one once it ended, and the description says which. A lost connection outranks both.
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { setSignInMark, setStatus } from "./status.js";

function mountButton(): { btn: HTMLButtonElement; dot: HTMLElement } {
  document.body.innerHTML = `
    <button type="button" id="account-btn">
      <span id="status-dot" class="status-dot"></span><span id="user-email"></span>
    </button>
    <span id="status-card">
      <span id="st-ws">-</span><span id="st-kiro">-</span><span id="st-auth">-</span>
    </span>`;
  return {
    btn: document.getElementById("account-btn") as HTMLButtonElement,
    dot: document.getElementById("status-dot") as HTMLElement,
  };
}

let btn: HTMLButtonElement;
let dot: HTMLElement;

beforeEach(() => {
  ({ btn, dot } = mountButton());
  setSignInMark("ok");
  setStatus("connected");
});

afterEach(() => {
  document.body.replaceChildren();
});

describe("the account button's sign-in mark", () => {
  it("is absent while the sign-in works", () => {
    expect(dot.classList.contains("signin-warn")).toBe(false);
    expect(dot.classList.contains("signin-ended")).toBe(false);
    expect(btn.dataset["tooltip"]).toBe("Connected");
  });

  it("warns while the sign-in is failing", () => {
    setSignInMark("failing");
    expect(dot.classList.contains("signin-warn")).toBe(true);
    expect(dot.classList.contains("signin-ended")).toBe(false);
    expect(btn.dataset["tooltip"]).toBe("Connected. Kiro sign-in failing");
  });

  it("marks the sign-in ended once it is signed out", () => {
    setSignInMark("signed_out");
    expect(dot.classList.contains("signin-ended")).toBe(true);
    expect(dot.classList.contains("signin-warn")).toBe(false);
    expect(btn.dataset["tooltip"]).toBe("Connected. Kiro sign-in ended");
  });

  it("clears when the state returns to ok", () => {
    setSignInMark("failing");
    setSignInMark("ok");
    expect(dot.classList.contains("signin-warn")).toBe(false);
    expect(btn.dataset["tooltip"]).toBe("Connected");
  });

  it("gives way to a lost connection, and comes back with it", () => {
    setSignInMark("failing");
    setStatus("disconnected");
    expect(dot.classList.contains("error")).toBe(true);
    expect(dot.classList.contains("signin-warn")).toBe(false);
    expect(btn.dataset["tooltip"]).toBe("Disconnected");
    setStatus("connected");
    expect(dot.classList.contains("signin-warn")).toBe(true);
    expect(btn.dataset["tooltip"]).toBe("Connected. Kiro sign-in failing");
  });

  it("reads none as nothing to mark", () => {
    setSignInMark("none");
    expect(dot.classList.contains("signin-warn")).toBe(false);
    expect(dot.classList.contains("signin-ended")).toBe(false);
    expect(btn.dataset["tooltip"]).toBe("Connected");
  });
});
