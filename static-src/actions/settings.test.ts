import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import type * as SettingsActions from "./settings.js";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../api-client.js", () => ({
  apiGetOrError: vi.fn(),
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,

  apiGet: vi.fn(),
  apiPost: vi.fn(),
  // Inert: present only so real-ESM linking succeeds.
  apiGetTyped: vi.fn(),
}));
import * as toast from "../toast.js";
import { signedIn } from "../__test-helpers__/identity.js";
import { settingsPayload } from "../__test-helpers__/settings.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

describe("saveSteering", () => {
  it("PUTs to /api/steering with content body, carrying the validator as If-Match", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const { saveSteering } = await import("./settings.js");
    await saveSteering.dispatch({ content: "# My steering", etag: 'W/"12-34"' });
    expect(mockFetch).toHaveBeenCalledTimes(1);
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/steering");
    expect(opts.method).toBe("PUT");
    expect(JSON.parse(opts.body as string)).toEqual({ content: "# My steering" });
    expect(new Headers(opts.headers as HeadersInit).get("If-Match")).toBe('W/"12-34"');
  });

  // On a server that REQUIRES If-Match, an invented value is a permanent 428.
  it("sends no If-Match when the read answered no validator", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const { saveSteering } = await import("./settings.js");
    await saveSteering.dispatch({ content: "x", etag: "" });
    const [, opts] = mockFetch.mock.calls[0]!;
    expect(new Headers(opts.headers as HeadersInit).has("If-Match")).toBe(false);
  });

  it("toasts error on failure", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "disk full" }), { status: 500 }),
    );
    const { saveSteering } = await import("./settings.js");
    const r = await saveSteering.dispatch({ content: "x", etag: "" });
    expect(r).toBeNull();
    expect(toast.error).toHaveBeenCalledWith(
      expect.stringContaining("Could not save steering"),
      undefined,
    );
  });

  // `settings-steering.ts` branches on the status: 409 the file moved, 428 no validator sent.
  it("carries the refusal's HTTP status on the normalized error", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "custom.md changed" }), { status: 409 }),
    );
    const { saveSteering } = await import("./settings.js");
    const out = await saveSteering.dispatch({ content: "x", etag: 'W/"1-1"' }).outcome;
    expect(out.status).toBe("error");
    expect(out.status === "error" ? out.error.status : 0).toBe(409);
  });

  // The validator rides the BODY: `decode` cannot reach a response header.
  it("answers the validator the 200 body carried", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ ok: true, etag: 'W/"9-9"' }), { status: 200 }),
    );
    const { saveSteering } = await import("./settings.js");
    expect(await saveSteering.dispatch({ content: "x", etag: 'W/"1-1"' })).toBe('W/"9-9"');
  });

  // "" (the server could not stat its write) and an absent field both answer "no token".
  it("answers no token when the body carries none", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const { saveSteering } = await import("./settings.js");
    expect(await saveSteering.dispatch({ content: "x", etag: 'W/"1-1"' })).toBe("");
  });
});

// The argument is `{ render, prev }`: `renderIdentity` is the one writer of the auth row and its
// separator, and carrying the whole VERDICT (not the address) makes all three arms restorable.
// Asserted through a `vi.fn()` render callback, which IS the contract.
describe("logout", () => {
  it("POSTs to /api/logout and renders the signed-out verdict optimistically", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const render = vi.fn();

    const { logout } = await import("./settings.js");
    await logout.dispatch({ render, prev: signedIn("user@test.com") });

    expect(render).toHaveBeenCalledWith({ state: "signed_out" });
    expect(mockFetch).toHaveBeenCalledWith(
      "/api/logout",
      expect.objectContaining({ method: "POST" }),
    );
  });

  it("rolls back to the verdict it replaced on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "nope" }), { status: 500 }));
    const render = vi.fn();
    const prev = signedIn("user@test.com");

    const { logout } = await import("./settings.js");
    await logout.dispatch({ render, prev });

    expect(render).toHaveBeenNthCalledWith(1, { state: "signed_out" });
    expect(render).toHaveBeenLastCalledWith(prev);
  });

  it("says when a refused sign-out leaves the account signed in, in the server's words", async () => {
    const kept =
      "marotte could not remove the saved sign-in, so you are still signed in. The server log has the details.";
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: kept }), { status: 500 }));
    const { logout } = await import("./settings.js");
    await logout.dispatch({ render: vi.fn(), prev: signedIn("a@test.com") });
    expect(toast.error).toHaveBeenCalledWith(kept, undefined);
  });

  it("says chats stop using the account within about four minutes, and how to stop them at once", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ output: "Signed out" }), { status: 200 }),
    );
    const { logout } = await import("./settings.js");
    await logout.dispatch({ render: vi.fn(), prev: signedIn("a@test.com") });
    expect(toast.success).toHaveBeenCalledWith(
      "Signed out. Chats stop using this account within about four minutes. To stop at once, restart the agent engine from Settings.",
    );
  });

  it("restores an UNAVAILABLE verdict rather than signed_out", async () => {
    // `unavailable` and `signed_out` both render an EMPTY address, so only the carried verdict can
    // restore `unavailable` after a refused logout.
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "nope" }), { status: 500 }));
    const render = vi.fn();
    const prev = { state: "unavailable", reason: "whoami unreachable" } as const;

    const { logout } = await import("./settings.js");
    await logout.dispatch({ render, prev });

    expect(render).toHaveBeenLastCalledWith(prev);
    expect(render).not.toHaveBeenLastCalledWith({ state: "signed_out" });
  });
});

describe("setKiroSetting", () => {
  it("PUTs to /api/kiro-settings and rolls back checkbox on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "bad" }), { status: 400 }));
    const input = document.createElement("input");
    input.type = "checkbox";
    input.checked = true; // user just toggled ON

    const { setKiroSetting } = await import("./settings.js");
    await setKiroSetting.dispatch({ key: "debug", value: "true", input });

    expect(input.checked).toBe(false);
  });

  it("PUTs to /api/kiro-settings with key/value body", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const input = document.createElement("input");
    input.type = "checkbox";
    input.checked = true;

    const { setKiroSetting } = await import("./settings.js");
    await setKiroSetting.dispatch({
      key: "telemetry.enabled",
      value: "true",
      input,
    });

    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/kiro-settings");
    expect(JSON.parse(opts.body as string)).toEqual({ key: "telemetry.enabled", value: "true" });
  });
});

describe("patchAppSettings", () => {
  it("PATCHes to /api/settings and puts back the switches it is told to on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    const input = document.createElement("input");
    input.type = "checkbox";
    input.checked = true;

    const { patchAppSettings } = await import("./settings.js");
    await patchAppSettings.dispatch({
      body: { debug_logs: true },
      restore: () => [{ el: input, checked: false }],
    });

    expect(input.checked).toBe(false);
  });
});

// Every switch the lock map can pin, saved through the action that saves it in
// production, with a real governance module painting the locks.
describe("a lockable switch crossing a lock", () => {
  type Actions = typeof SettingsActions;
  const save = {
    kiro: (a: Actions, key: string, input: HTMLInputElement) =>
      a.setKiroSetting.dispatch({ key, value: String(input.checked), input }),
    marotte: (a: Actions, key: string, input: HTMLInputElement) => {
      const before = !input.checked;
      return a.patchAppSettings.dispatch({
        body: { [key]: input.checked },
        restore: () => [{ el: input, checked: before }],
      });
    },
  };
  const lockable = [
    ["flag-telemetry", "telemetry.enabled", save.kiro],
    ["flag-content-collection", "content_collection_enabled", save.marotte],
    ["flag-workflows", "workflows_enabled", save.marotte],
  ] as const;

  const govState = (lockKey?: string) => ({
    known: true,
    is_enterprise: true,
    features: {
      mcp_enabled: true,
      web_tools_enabled: true,
      usage_analytics: false,
      content_collection: false,
      prompt_logging: false,
      code_reference_tracker: false,
      autonomous_agents: true,
    },
    ...(lockKey === undefined
      ? {}
      : { locks: { [lockKey]: { source: "organization", reason: "Set by org", value: true } } }),
  });

  let initialized = false;
  async function setup(inputID: string) {
    document.body.insertAdjacentHTML(
      "beforeend",
      `<div class="section-option" id="lock-row"><label class="toggle">` +
        `<input type="checkbox" id="${inputID}"></label>` +
        `<div><span class="section-option-label">Switch</span></div></div>`,
    );
    const gov = await import("../governance.js");
    const bus = await import("../bus.js");
    if (!initialized) {
      const api = await import("../api-client.js");
      vi.mocked(api.apiGetTyped).mockResolvedValueOnce(null);
      gov.initGovernance();
      initialized = true;
    }
    const frame = (lockKey?: string) => {
      bus.dispatch({ type: "governance_state", chat_id: "", payload: govState(lockKey) });
    };
    const input = document.getElementById(inputID) as HTMLInputElement;
    return { input, frame, actions: await import("./settings.js") };
  }

  afterEach(() => {
    document.getElementById("lock-row")?.remove();
  });

  function heldResponse(): (r: Response) => void {
    let answer: (r: Response) => void = () => undefined;
    mockFetch.mockReturnValueOnce(
      new Promise<Response>((r) => {
        answer = r;
      }),
    );
    return (r) => {
      answer(r);
    };
  }

  describe.each(lockable)("%s", (inputID, key, saveSwitch) => {
    it("shows the lock while a refused save crosses it, then the stored value", async () => {
      const { input, frame, actions } = await setup(inputID);
      const answer = heldResponse();
      input.checked = true;
      const done = saveSwitch(actions, key, input);
      await vi.waitFor(() => {
        expect(mockFetch).toHaveBeenCalled();
      });
      frame(key);
      answer(new Response(JSON.stringify({ error: "refused" }), { status: 409 }));
      await done;
      expect(input.checked).toBe(true);
      expect(input.disabled).toBe(true);
      frame();
      expect(input.checked).toBe(false);
      expect(input.disabled).toBe(false);
    });

    it("keeps a write made after the unlock across the next lock and unlock", async () => {
      const { input, frame, actions } = await setup(inputID);
      frame(key);
      frame();
      expect(input.checked).toBe(false);
      // Both endpoints answer 200; the settings PATCH's body is the committed settings.
      mockFetch.mockResolvedValueOnce(Response.json(settingsPayload()));
      input.checked = true;
      await saveSwitch(actions, key, input);
      expect(input.checked).toBe(true);
      frame(key);
      frame();
      expect(input.checked).toBe(true);
    });

    it("restores the value a load wrote under the lock when it lifts", async () => {
      const { input, frame } = await setup(inputID);
      const gov = await import("../governance.js");
      input.checked = true;
      frame(key);
      gov.writeSwitch(input, false);
      gov.paintSettingLocks();
      expect(input.checked).toBe(true);
      frame();
      expect(input.checked).toBe(false);
      gov.writeSwitch(input, true);
      expect(input.checked).toBe(true);
    });
  });
});

describe("restartEngine", () => {
  it("POSTs /api/engine/restart and takes the 202", async () => {
    mockFetch.mockResolvedValue(new Response(null, { status: 202 }));
    const { restartEngine } = await import("./settings.js");
    const r = await restartEngine.dispatch(undefined);
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/engine/restart");
    expect(opts.method).toBe("POST");
    expect(r).toBe(true);
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("says a restart is already under way on a 409", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "The agent engine is already restarting." }), {
        status: 409,
      }),
    );
    const { restartEngine } = await import("./settings.js");
    expect(await restartEngine.dispatch(undefined)).toBeNull();
    expect(toast.error).toHaveBeenCalledWith("The agent engine is already restarting.", undefined);
  });
});

describe("setEngineHeap", () => {
  it("PATCHes engine_heap_mib alone and shows the server's refusal", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "engine_heap_mib must be 0, or 1024 to 3584" }), {
        status: 400,
      }),
    );
    const { setEngineHeap } = await import("./settings.js");
    expect(await setEngineHeap.dispatch({ mib: 4096 })).toBeNull();
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/settings");
    expect(opts.method).toBe("PATCH");
    expect(JSON.parse(opts.body as string)).toEqual({ engine_heap_mib: 4096 });
    expect(toast.error).toHaveBeenCalledWith(
      "engine_heap_mib must be 0, or 1024 to 3584",
      undefined,
    );
  });
});
