import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
let host: HTMLElement;
let entry: { readonly width: number; readonly height: number } | null = null;

beforeAll(() => {
  style = mountAppCSS();
  entry = { width: window.innerWidth, height: window.innerHeight };
});

afterAll(async () => {
  style.remove();
  if (entry !== null) {
    await page.viewport(entry.width, entry.height);
  }
});

afterEach(() => {
  host.remove();
  document.documentElement.removeAttribute("data-pointer");
});

function mountRow(): HTMLElement {
  host = document.createElement("div");
  host.id = "tab-list";
  host.innerHTML = `<div class="tab" role="tab" data-kind="chat">
    <span class="tab-status-dot" aria-hidden="true"></span><span class="tab-name">Release notes</span>
    <span class="tab-close" aria-hidden="true"><svg class="ic-inline" viewBox="0 0 24 24"></svg></span>
  </div>`;
  document.body.appendChild(host);
  return host.querySelector<HTMLElement>(".tab")!;
}

function beginRename(row: HTMLElement): HTMLInputElement {
  const name = row.querySelector<HTMLElement>(".tab-name")!;
  const input = document.createElement("input");
  input.type = "text";
  input.className = "tab-name-input";
  input.value = name.textContent;
  name.hidden = true;
  name.after(input);
  return input;
}

describe("renaming a tab", () => {
  it.each([
    [1180, 820, "fine"],
    [1180, 820, "coarse"],
    [390, 844, "fine"],
    [390, 844, "coarse"],
  ] as const)("keeps the row's height at %ix%i, %s", async (width, height, tier) => {
    await page.viewport(width, height);
    document.documentElement.dataset["pointer"] = tier;
    const row = mountRow();
    const before = row.getBoundingClientRect().height;

    const input = beginRename(row);
    const field = input.getBoundingClientRect().height;
    const text = parseFloat(getComputedStyle(input).fontSize);

    expect(row.getBoundingClientRect().height, "the row does not grow").toBe(before);
    expect(field, "the field holds its line").toBeGreaterThan(text);
    expect(field, "and is only a hair taller than it").toBeLessThanOrEqual(text * 1.75);
  });
});
