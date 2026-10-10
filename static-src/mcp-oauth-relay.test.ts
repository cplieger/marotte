// The OAuth loopback relay's row affordance: KAS's redirect listener is on the container's localhost, so a remote
// browser's sign-in dies; the user pastes the dead address and the server replays it inward. This pins the client's
// half; validation is the server's (internal/hub/mcp_oauth_relay_test.go).
import { describe, it, expect, vi, beforeEach } from "vitest";

import type * as ApiClient from "./api-client.js";
import type * as McpActions from "./actions/mcp.js";
import type * as McpStateMod from "./mcp-state.js";

vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));

// Keeps the real mcp-state controller off the page's fetch, or teardown prints an AbortError over a green run.
vi.mock("./api-client.js", async (importOriginal) => {
  const actual = await importOriginal<typeof ApiClient>();
  return {
    ...actual,
    apiGet: async (): Promise<null> => null,
    apiGetTyped: async (): Promise<null> => null,
    apiPost: async (): Promise<null> => null,
  };
});

// vi.hoisted: a vi.mock factory is hoisted above top-level consts.
const { relayDispatch, refetchStatus } = vi.hoisted(() => ({
  relayDispatch: vi.fn(),
  refetchStatus: vi.fn(),
}));

vi.mock("./actions/mcp.js", async (importOriginal) => {
  const actual = await importOriginal<typeof McpActions>();
  return { ...actual, relayOAuthCallback: { dispatch: relayDispatch } };
});
// Partial: actions/mcp.ts needs the real apiAction; only the process-wide cleanup registry is replaced.
vi.mock(import("./actions/index.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    registerCleanup:
      (_fn: () => void): (() => void) =>
      () => {
        /* noop: the singleton cleanup registry is process-wide */
      },
  };
});
vi.mock("./mcp-state.js", async (importOriginal) => {
  const actual = await importOriginal<typeof McpStateMod>();
  return { ...actual, mcpState: { ...actual.mcpState, refetchStatus } };
});

import { renderOAuthRelay } from "./mcp-ui.js";

function resolveWith(hook: "onSuccess" | "onError", arg: unknown): void {
  relayDispatch.mockImplementation((_args: unknown, opts: Record<string, (v: unknown) => void>) => {
    opts[hook]?.(arg);
    return Promise.resolve(null);
  });
}

interface Parts {
  box: HTMLDetailsElement;
  input: HTMLInputElement;
  submit: HTMLButtonElement;
  note: () => string;
}

function mount(server = "linear"): Parts {
  const box = renderOAuthRelay(server);
  document.body.replaceChildren(box);
  return {
    box,
    input: box.querySelector<HTMLInputElement>(".mcp-relay-input") as HTMLInputElement,
    submit: box.querySelector<HTMLButtonElement>("button") as HTMLButtonElement,
    note: () => box.querySelector(".mcp-relay-note")?.textContent ?? "",
  };
}

const flush = (): Promise<void> => new Promise((r) => setTimeout(r, 0));

beforeEach(() => {
  relayDispatch.mockReset();
  refetchStatus.mockReset();
  document.body.replaceChildren();
});

describe("renderOAuthRelay", () => {
  // A sign-in from a browser on the container works normally and needs none of this.
  it("starts collapsed so a working sign-in is not asked to read a form", () => {
    const { box } = mount();
    expect(box.open).toBe(false);
    expect(box.querySelector("summary")?.textContent).toContain("did not load");
  });

  it("sends the pasted address verbatim under the server it belongs to", async () => {
    resolveWith("onSuccess", { status: 200 });
    const { input, submit } = mount("linear");
    // Percent-encoding and a long code, as a real callback carries; re-encoding would break the exchange.
    const pasted = "http://localhost:41234/oauth/callback?code=A%2Fb%2Bc-9&state=st-abc";
    input.value = pasted;

    submit.click();
    await flush();

    expect(relayDispatch).toHaveBeenCalledTimes(1);
    expect(relayDispatch.mock.calls[0]?.[0]).toEqual({
      server: "linear",
      redirect_url: pasted,
    });
  });

  it("trims the clipboard's whitespace rather than refusing the paste", async () => {
    resolveWith("onSuccess", { status: 200 });
    const { input, submit } = mount();
    input.value = "  http://localhost:41234/oauth/callback?code=abc&state=st  \n";

    submit.click();
    await flush();

    expect(relayDispatch.mock.calls[0]?.[0]).toMatchObject({
      redirect_url: "http://localhost:41234/oauth/callback?code=abc&state=st",
    });
  });

  it("asks for an address instead of posting an empty one", async () => {
    const { submit, note } = mount();

    submit.click();
    await flush();

    expect(relayDispatch).not.toHaveBeenCalled();
    expect(note()).toContain("Paste the address");
  });

  // Delivered, not connected: the code only starts the token exchange, and the connected state is KAS's to report.
  it("reports a delivered callback as delivered, not as a finished sign-in", async () => {
    resolveWith("onSuccess", { status: 200 });
    const { input, submit, note } = mount();
    input.value = "http://localhost:41234/oauth/callback?code=abc&state=st";

    submit.click();
    await flush();

    expect(note()).toContain("Delivered");
    expect(note()).not.toContain("Connected");
    expect(refetchStatus).toHaveBeenCalled();
    // The code is spent.
    expect(input.value).toBe("");
  });

  // Only the server can name what was wrong (it checks against the stored authorization URL), so it is shown verbatim.
  it("shows the server's own refusal, verbatim", async () => {
    resolveWith("onError", new Error("that address belongs to a different sign-in"));
    const { input, submit, note, box } = mount();
    input.value = "http://localhost:41234/oauth/callback?code=abc&state=wrong";

    submit.click();
    await flush();

    expect(note()).toBe("that address belongs to a different sign-in");
    expect(box.querySelector(".mcp-relay-note")?.classList.contains("mcp-relay-err")).toBe(true);
    // A refused relay did not spend the code, so a corrected paste is an edit.
    expect(input.value).not.toBe("");
  });

  it("re-enables the button after a refusal so a corrected address can be sent", async () => {
    resolveWith("onError", new Error("nope"));
    const { input, submit } = mount();
    input.value = "http://localhost:41234/oauth/callback?code=abc&state=wrong";

    submit.click();
    await flush();

    expect(submit.disabled).toBe(false);
  });
});
