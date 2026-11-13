// The listening face pulses with the streaming face's beat, and holds still under reduced motion (rule 3).

import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { emulateA11yMedia, resetA11yMedia } from "./__test-helpers__/a11y-media.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let sheet: HTMLStyleElement;
let row: HTMLDivElement;

beforeAll(() => {
  sheet = mountAppCSS();
  row = document.createElement("div");
  row.className = "prompt-pills";
  document.body.append(row);
});

afterAll(() => {
  sheet.remove();
  row.remove();
});

function button(cls: string): HTMLButtonElement {
  const b = document.createElement("button");
  b.type = "button";
  b.className = cls;
  row.append(b);
  return b;
}

const overlay = (el: Element): CSSStyleDeclaration => getComputedStyle(el, "::before");

describe("the listening face", () => {
  it("beats on the shared dot beat", () => {
    const b = button("send-btn dictating");
    try {
      expect(overlay(b).content).not.toBe("none");
      expect(overlay(b).animationName).toBe("vk-dot-beat");
      expect(overlay(b).animationIterationCount).toBe("infinite");
    } finally {
      b.remove();
    }
  });

  it("does not beat on the resting microphone or on Send", () => {
    const b = button("send-btn");
    try {
      expect(overlay(b).animationName).toBe("none");
    } finally {
      b.remove();
    }
  });
});

describe("the listening face under reduced motion", () => {
  beforeAll(async () => {
    await emulateA11yMedia({ reducedMotion: "reduce" });
  });

  afterAll(async () => {
    await resetA11yMedia();
  });

  it("holds still at its resting opacity", async () => {
    const b = button("send-btn dictating");
    try {
      expect(overlay(b).animationIterationCount).toBe("1");
      await vi.waitFor(() => {
        expect(overlay(b).opacity).toBe("0");
      });
    } finally {
      b.remove();
    }
  });

  it("still looks different from the resting microphone", () => {
    const resting = button("send-btn");
    const listening = button("send-btn dictating");
    try {
      expect(getComputedStyle(listening).backgroundColor).not.toBe(
        getComputedStyle(resting).backgroundColor,
      );
    } finally {
      resting.remove();
      listening.remove();
    }
  });
});
