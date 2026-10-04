// The network double the press-feedback suites share: every request is held until
// the case answers it, so a control's busy state can be read while it runs.

import { expect, vi } from "vitest";
/** One request the page made, answered when the case says so. */
interface Held {
  url: string;
  body: unknown;
  answer: (r: Response) => void;
}

/** Hold every request the page makes until the case answers it. */
export function heldFetch(): Held[] {
  const calls: Held[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(
      (input: RequestInfo | URL, init?: RequestInit) =>
        new Promise<Response>((resolve) => {
          const raw = init?.body;
          calls.push({
            url: typeof input === "string" ? input : input instanceof URL ? input.href : input.url,
            body: typeof raw === "string" ? JSON.parse(raw) : undefined,
            answer: resolve,
          });
        }),
    ),
  );
  return calls;
}

export function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/** The clone route's answer: one final NDJSON line. */
export function cloneAnswer(line: Record<string, unknown>): Response {
  return new Response(`${JSON.stringify(line)}\n`, { status: 200 });
}

/** Every value `data-async-status` takes on `btn` from now on: the outcome the
 *  feedback helper paints, which a cancelled confirm must never produce. */
export function outcomesOf(btn: HTMLElement): string[] {
  const seen: string[] = [];
  new MutationObserver(() => {
    const s = btn.dataset["asyncStatus"];
    if (s !== undefined) {
      seen.push(s);
    }
  }).observe(btn, { attributes: true, attributeFilter: ["data-async-status"] });
  return seen;
}

/** Let every queued microtask and one task run. */
export async function settle(): Promise<void> {
  await new Promise((r) => setTimeout(r, 0));
}

export function expectBusy(btn: HTMLButtonElement | undefined): void {
  expect(btn?.disabled).toBe(true);
  expect(btn?.getAttribute("aria-busy")).toBe("true");
}

export function expectIdle(btn: HTMLButtonElement | undefined): void {
  expect(btn?.disabled).toBe(false);
  expect(btn?.getAttribute("aria-busy")).toBeNull();
}
