// The sign-in banner follows the server's sign-in state, read at boot from whoami and pushed as signin_health: one
// app-wide banner while the sign-in fails or ended, with Sign in again, and nothing while it works.
import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as BannerStack from "./banner-stack.js";
import type * as ApiClient from "./api-client.js";
import type { SignInCause, SignInHealth, SignInState, WhoamiResponse } from "./wire/types.gen.js";
import pauseReasonsRaw from "../internal/agent/testdata/pause_reasons.json?raw";

vi.mock("./banner-stack.js", async (importOriginal) => ({
  ...(await importOriginal<typeof BannerStack>()),
  showBanner: vi.fn(),
  clearBannerCodes: vi.fn(),
}));
const { mockLogin, mockGet } = vi.hoisted(() => ({
  mockLogin: vi.fn(),
  mockGet: vi.fn<(path: string) => Promise<unknown>>(),
}));
vi.mock("./modals.js", () => ({ showLoginModal: mockLogin }));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetTyped: mockGet,
}));

const { adoptSignInHealth, initSignInHealth } = await import("./signin-health.js");
const { resolveIdentity } = await import("./identity.js");
const { dispatch, emitBus, BUS_RECONCILE } = await import("./bus.js");
const { showBanner, clearBannerCodes, GLOBAL_BANNER } = await import("./banner-stack.js");

const mockedShow = vi.mocked(showBanner);
const mockedClear = vi.mocked(clearBannerCodes);

const CODE = "signin_health";

/** The run tab's own cause words, which the banner must share: the provider pause reason's clause before `;`. */
const RUN_CAUSE =
  ((JSON.parse(pauseReasonsRaw) as { reasons: string[] }).reasons[1] ?? "").split(";")[0] ?? "";

let seq = 0;

/** A health newer than every one built before it. */
function health(state: SignInState, cause?: SignInCause): SignInHealth {
  seq += 1;
  return {
    state,
    ...(cause === undefined ? {} : { cause, since: "2026-11-15T11:43:55Z" }),
    version: { boot_ms: 1_760_000_000_000, seq },
  };
}

/** The last showBanner call for the sign-in code. */
function lastShown(): Parameters<typeof showBanner> | undefined {
  return mockedShow.mock.calls.filter((c) => c[1] === CODE).at(-1);
}

function clearedSignIn(): boolean {
  return mockedClear.mock.calls.some((c) => c[0] === GLOBAL_BANNER && c[1].includes(CODE));
}

initSignInHealth();

beforeEach(() => {
  vi.clearAllMocks();
  adoptSignInHealth(health("ok"));
  vi.clearAllMocks();
});

describe("the sign-in banner", () => {
  it("raises the app-wide warning while the sign-in is failing, in the run tab's words", () => {
    dispatch({ type: "signin_health", payload: health("failing", "provider_unreachable") });
    const call = lastShown();
    expect(call).toBeDefined();
    const [chatID, , message, level, dismissible, link] = call ?? [];
    expect(chatID).toBe(GLOBAL_BANNER);
    expect(level).toBe("warning");
    expect(dismissible).toBe(false);
    expect(RUN_CAUSE).not.toBe("");
    expect(message).toContain(RUN_CAUSE);
    expect(message).toMatch(/can't renew your Kiro sign-in/);
    expect(link?.label).toBe("Sign in again");
    link?.onClick?.();
    expect(mockLogin).toHaveBeenCalledTimes(1);
  });

  it("says the sign-in ended once it is signed out", () => {
    adoptSignInHealth(health("signed_out", "rejected"));
    const [, , message, level, dismissible, link] = lastShown() ?? [];
    expect(level).toBe("error");
    expect(dismissible).toBe(false);
    expect(message).toMatch(/sign-in ended/);
    expect(message).toContain("Sign in to Kiro");
    expect(link?.label).toBe("Sign in again");
  });

  it.each(["ok", "none"] as const)("clears when the state turns %s", (state) => {
    adoptSignInHealth(health("failing", "provider_unreachable"));
    expect(lastShown()).toBeDefined();
    mockedClear.mockClear();
    adoptSignInHealth(health(state));
    expect(clearedSignIn()).toBe(true);
  });

  it("ignores a health older than the one shown", () => {
    const older = health("ok");
    adoptSignInHealth(health("failing", "provider_unreachable"));
    mockedClear.mockClear();
    adoptSignInHealth(older);
    expect(clearedSignIn()).toBe(false);
  });

  it("re-reads the health from whoami on a reconcile", async () => {
    const answer: WhoamiResponse = {
      state: "signed_in",
      email: "a@example.com",
      health: health("failing", "provider_unreachable"),
    };
    mockGet.mockResolvedValue(answer);
    emitBus(BUS_RECONCILE, { cause: "test", signal: new AbortController().signal });
    await vi.waitFor(() => {
      expect(lastShown()).toBeDefined();
    });
    expect(mockGet).toHaveBeenCalledWith("/api/whoami", expect.anything(), expect.anything());
  });
});

describe("the boot read", () => {
  it("carries whoami's health on the identity verdict", async () => {
    const h = health("failing", "provider_unreachable");
    mockGet.mockResolvedValue({ state: "signed_in", email: "a@example.com", health: h });
    const v = await resolveIdentity();
    expect(v.health).toEqual(h);
  });

  it("carries none when whoami names no health", async () => {
    mockGet.mockResolvedValue({ state: "signed_out" });
    const v = await resolveIdentity();
    expect(v.health).toBeNull();
  });
});
