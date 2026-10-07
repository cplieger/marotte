import { describe, it, expect, vi, afterEach, beforeEach } from "vitest";
import type * as ForgeActions from "./actions/forge.js";
import type * as ApiClient from "./api-client.js";
import type { DeviceFlowResponse } from "./wire/types.gen.js";

const mocks = vi.hoisted(() => ({ dispatch: vi.fn(), apiPostTyped: vi.fn() }));
vi.mock("./actions/forge.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ForgeActions>()),
  startDeviceFlow: { dispatch: mocks.dispatch },
}));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiPostTyped: mocks.apiPostTyped,
}));

const { resetActionFramework } = await import("@cplieger/actions/testing");
const { renderDevicePrompt, renderDeviceSignIn, abortPoll, endGrantsIn } =
  await import("./forge-auth-oauth.js");

function start(over: Partial<DeviceFlowResponse> = {}): DeviceFlowResponse {
  return {
    user_code: "WXYZ-1234",
    verification_uri: "https://github.com/login/device",
    grant_id: "0123456789abcdef0123456789abcdef",
    interval: 5,
    expires_in: 900,
    ...over,
  };
}

/** A dispatch handle as the actions framework returns one: awaitable, with
 *  the typed outcome beside it. */
function started(value: DeviceFlowResponse): Promise<DeviceFlowResponse> & {
  outcome: Promise<{ status: "success"; value: DeviceFlowResponse }>;
} {
  return Object.assign(Promise.resolve(value), {
    outcome: Promise.resolve({ status: "success" as const, value }),
  });
}

describe("renderDeviceSignIn", () => {
  let body: HTMLElement;
  let hostInput: HTMLInputElement;
  let deps: { connected: ReturnType<typeof vi.fn<(id: string) => void>> };

  beforeEach(() => {
    vi.useFakeTimers();
    resetActionFramework();
    mocks.dispatch.mockReset();
    mocks.apiPostTyped.mockReset();
    body = document.createElement("div");
    hostInput = document.createElement("input");
    hostInput.value = "github.com";
    document.body.append(hostInput, body);
    deps = { connected: vi.fn() };
  });

  afterEach(() => {
    abortPoll();
    body.remove();
    hostInput.remove();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  async function signIn(kind: "github" | "gitlab" = "github"): Promise<void> {
    renderDeviceSignIn(body, { kind, hostInput, options: () => ({}) }, deps);
    body.querySelector<HTMLButtonElement>("[data-forge-device-start]")!.click();
    await vi.advanceTimersByTimeAsync(0);
  }

  it("polls the grant by the id the start answered, never by a device code", async () => {
    mocks.dispatch.mockReturnValue(started(start()));
    mocks.apiPostTyped.mockResolvedValue({ status: "complete" });

    await signIn();
    await vi.advanceTimersByTimeAsync(5000);

    expect(mocks.apiPostTyped).toHaveBeenCalledWith(
      "/api/forges/oauth/github/poll",
      { grant_id: "0123456789abcdef0123456789abcdef" },
      expect.any(Function),
      expect.any(AbortSignal),
    );
    expect(deps.connected).toHaveBeenCalledExactlyOnceWith("github:github.com");
  });

  it("closing the pane of a grant that connected cancels nothing on the server", async () => {
    // The pane closes from inside `connected`, which ends every grant left in it.
    mocks.dispatch.mockReturnValue(started(start()));
    mocks.apiPostTyped.mockResolvedValue({ status: "complete" });
    const fetchSpy = vi.fn<typeof fetch>(() =>
      Promise.resolve(new Response(null, { status: 204 })),
    );
    vi.stubGlobal("fetch", fetchSpy);
    deps.connected.mockImplementation(() => {
      endGrantsIn(document.body);
    });

    await signIn();
    await vi.advanceTimersByTimeAsync(5000);

    expect(deps.connected).toHaveBeenCalledOnce();
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("says the sign-in ended when the grant is denied", async () => {
    mocks.dispatch.mockReturnValue(started(start()));
    mocks.apiPostTyped.mockResolvedValue({
      status: "denied",
      error: "the user declined the grant",
    });

    await signIn();
    await vi.advanceTimersByTimeAsync(5000);

    expect(body.querySelector(".forge-device-status")?.textContent).toBe(
      "Error: the user declined the grant",
    );
  });

  it("polls GitLab's route for a GitLab grant and expands that connection on completion", async () => {
    hostInput.value = "gitlab.example.com";
    mocks.dispatch.mockReturnValue(started(start()));
    mocks.apiPostTyped.mockResolvedValue({ status: "complete" });
    renderDeviceSignIn(body, { kind: "gitlab", hostInput, options: () => ({}) }, deps);
    body.querySelector<HTMLInputElement>("[data-forge-client-id] input")!.value = "app-id";

    body.querySelector<HTMLButtonElement>("[data-forge-device-start]")!.click();
    await vi.advanceTimersByTimeAsync(5000);

    expect(mocks.dispatch).toHaveBeenCalledWith({
      kind: "gitlab",
      host: "gitlab.example.com",
      clientId: "app-id",
      options: {},
    });
    expect(mocks.apiPostTyped).toHaveBeenCalledWith(
      "/api/forges/oauth/gitlab/poll",
      { grant_id: "0123456789abcdef0123456789abcdef" },
      expect.any(Function),
      expect.any(AbortSignal),
    );
    expect(deps.connected).toHaveBeenCalledExactlyOnceWith("gitlab:gitlab.example.com");
  });

  it("names on a self-managed GitLab every setting its OAuth application needs", () => {
    hostInput.value = "gitlab.example.com";
    renderDeviceSignIn(body, { kind: "gitlab", hostInput, options: () => ({}) }, deps);

    const hint = body.querySelector("[data-forge-client-id] .tool-form-hint")?.textContent ?? "";
    for (const setting of [
      "Device authorization grant ticked",
      "Confidential unticked",
      "scope api",
      "any HTTPS redirect URI",
    ]) {
      expect(hint).toContain(setting);
    }
  });

  it("names no GitLab setting on a GitHub Enterprise host", () => {
    hostInput.value = "ghe.example.com";
    renderDeviceSignIn(body, { kind: "github", hostInput, options: () => ({}) }, deps);

    const hint = body.querySelector("[data-forge-client-id] .tool-form-hint")?.textContent ?? "";
    expect(hint).toContain("client ID");
    expect(hint).not.toContain("Device authorization grant");
  });

  it("another host with no client id names what is missing and starts nothing", async () => {
    hostInput.value = "ghe.example.com";

    await signIn();

    expect(mocks.dispatch).not.toHaveBeenCalled();
    expect(body.querySelector(".forge-card-status")?.textContent).toBe(
      "Enter the OAuth client ID for this server.",
    );
  });

  it("an empty host names what is missing and starts nothing", async () => {
    hostInput.value = "  ";

    await signIn();

    expect(mocks.dispatch).not.toHaveBeenCalled();
    expect(body.querySelector(".forge-card-status")?.textContent).toBe("Enter the server's host.");
  });

  it("a start answered after the pane closed polls nothing", async () => {
    let land!: (v: DeviceFlowResponse) => void;
    const outcome = new Promise<{ status: "success"; value: DeviceFlowResponse }>((r) => {
      land = (value) => {
        r({ status: "success", value });
      };
    });
    mocks.dispatch.mockReturnValue(
      Object.assign(
        outcome.then((o) => o.value),
        { outcome },
      ),
    );
    mocks.apiPostTyped.mockResolvedValue({ status: "pending" });

    await signIn();
    body.remove();
    land(start());
    await vi.advanceTimersByTimeAsync(10_000);

    expect(mocks.apiPostTyped).not.toHaveBeenCalled();
  });

  it("Cancel posts the grant id, stops polling and restores the start state", async () => {
    mocks.dispatch.mockReturnValue(started(start()));
    mocks.apiPostTyped.mockResolvedValue({ status: "pending" });
    let answer!: (r: Response) => void;
    const fetchSpy = vi.fn<typeof fetch>(
      () =>
        new Promise<Response>((r) => {
          answer = r;
        }),
    );
    vi.stubGlobal("fetch", fetchSpy);
    await signIn();
    const cancel = body.querySelector<HTMLButtonElement>("[data-forge-device-cancel]")!;

    cancel.click();
    expect(cancel.disabled).toBe(true);
    expect(cancel.getAttribute("aria-busy")).toBe("true");
    expect(fetchSpy).toHaveBeenCalledOnce();
    expect(fetchSpy.mock.calls[0]![0]).toBe("/api/forges/oauth/github/cancel");
    expect(JSON.parse(fetchSpy.mock.calls[0]![1]!.body as string)).toEqual({
      grant_id: "0123456789abcdef0123456789abcdef",
    });

    answer(new Response(null, { status: 204 }));
    await vi.advanceTimersByTimeAsync(60_000);

    expect(mocks.apiPostTyped).not.toHaveBeenCalled();
    expect(body.querySelector(".forge-device-prompt")).toBeNull();
    expect(body.querySelector("[data-forge-device-start]")).not.toBeNull();
  });

  it("closing the pane that holds the prompt stops the poll and cancels the grant once", async () => {
    mocks.dispatch.mockReturnValue(started(start()));
    mocks.apiPostTyped.mockResolvedValue({ status: "pending" });
    const fetchSpy = vi.fn<typeof fetch>(() =>
      Promise.resolve(new Response(null, { status: 204 })),
    );
    vi.stubGlobal("fetch", fetchSpy);
    await signIn();

    endGrantsIn(document.body);
    endGrantsIn(document.body);
    body.replaceChildren();
    await vi.advanceTimersByTimeAsync(60_000);

    expect(mocks.apiPostTyped).not.toHaveBeenCalled();
    expect(fetchSpy).toHaveBeenCalledOnce();
    expect(fetchSpy.mock.calls[0]![0]).toBe("/api/forges/oauth/github/cancel");
    expect(JSON.parse(fetchSpy.mock.calls[0]![1]!.body as string)).toEqual({
      grant_id: "0123456789abcdef0123456789abcdef",
    });
  });

  it("a failed Cancel names the failure in the prompt's status line", async () => {
    mocks.dispatch.mockReturnValue(started(start()));
    mocks.apiPostTyped.mockResolvedValue({ status: "pending" });
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(() =>
        Promise.resolve(
          new Response(JSON.stringify({ error: "missing grant_id" }), { status: 400 }),
        ),
      ),
    );
    await signIn();

    body.querySelector<HTMLButtonElement>("[data-forge-device-cancel]")!.click();
    await vi.advanceTimersByTimeAsync(0);

    expect(body.querySelector(".forge-device-status")?.textContent).toBe(
      "Could not cancel the sign-in. missing grant_id",
    );
  });
});

describe("renderDevicePrompt", () => {
  it("renders the user_code in a code element and an http(s) anchor for a valid uri", () => {
    const host = document.createElement("div");
    renderDevicePrompt(host, start());

    const code = host.querySelector(".forge-device-code");
    expect(code?.tagName).toBe("CODE");
    expect(code?.textContent).toBe("WXYZ-1234");

    const link = host.querySelector<HTMLAnchorElement>("a.forge-device-link");
    expect(link).not.toBeNull();
    expect(link?.getAttribute("href")).toBe("https://github.com/login/device");
    expect(link?.textContent).toBe("https://github.com/login/device");
  });

  it("renders inert text for a verification_uri containing a markup-injection payload", () => {
    const evil = `"><img src=x onerror=alert(1)>`;
    const host = document.createElement("div");
    renderDevicePrompt(host, start({ verification_uri: evil }));

    expect(host.querySelector("a")).toBeNull();
    // The payload is never parsed as HTML: no injected element leaks in.
    expect(host.querySelector("img")).toBeNull();
    const p = host.querySelector("p");
    expect(p?.textContent).toContain(evil);
  });

  it("emits no anchor when the uri is non-http(s) (e.g. javascript:)", () => {
    const host = document.createElement("div");
    renderDevicePrompt(host, start({ verification_uri: "javascript:alert(1)" }));

    expect(host.querySelector("a")).toBeNull();
    expect(host.querySelector("p")?.textContent).toContain("javascript:alert(1)");
  });

  it("copy button writes the user_code to the clipboard and shows transient feedback", () => {
    const writeText = vi.fn(() => Promise.resolve());
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    vi.useFakeTimers();

    const host = document.createElement("div");
    renderDevicePrompt(host, start({ user_code: "ABCD-9999" }));

    const btn = host.querySelector<HTMLButtonElement>(".forge-copy-btn");
    expect(btn).not.toBeNull();
    btn?.click();

    expect(writeText).toHaveBeenCalledWith("ABCD-9999");
    expect(btn?.textContent).toBe("Copied");
    vi.advanceTimersByTime(2000);
    expect(btn?.textContent).toBe("Copy");

    vi.useRealTimers();
    vi.unstubAllGlobals();
  });
});
