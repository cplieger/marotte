import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { setupWorker } from "msw/browser";

import { closeModal, initLoginModal, loginPollVerdict, openModal } from "./modals.js";
import type { WhoamiResponse } from "./wire/types.gen.js";

const worker = setupWorker();
let whoami: WhoamiResponse[] = [];
let whoamiReads = 0;
let host: HTMLDivElement;
let onLoggedIn: ReturnType<typeof vi.fn<() => void>>;

/** The old account stays signed in while the new attempt polls: the switch the poll must not mistake for success. */
const OLD_ACCOUNT_PENDING: WhoamiResponse = {
  state: "signed_in",
  auth: "Logged in with IAM Identity Center",
  loginAttempt: 2,
  loginState: "pending",
};

beforeAll(async () => {
  await worker.start({ quiet: true });
  // One page and one init: initLoginModal binds its listeners once, as app.ts does.
  host = document.createElement("div");
  host.innerHTML =
    '<div id="login-modal" class="hidden" data-modal>' +
    '<button type="button" id="modal-login-free" class="modal-btn">Sign in</button>' +
    '<button type="button" id="modal-login-sso" class="modal-btn">Sign in with SSO</button>' +
    '<div id="modal-sso-form" class="hidden">' +
    '<input id="modal-provider"><input id="modal-region">' +
    '<button type="button" id="modal-sso-submit" class="modal-btn">Connect</button></div>' +
    '<div id="modal-status"></div></div>';
  document.body.append(host);
  onLoggedIn = vi.fn<() => void>();
  initLoginModal(() => {
    onLoggedIn();
  });
});

afterAll(() => {
  worker.stop();
  host.remove();
});

beforeEach(() => {
  whoami = [];
  whoamiReads = 0;
  onLoggedIn.mockClear();
  status().textContent = "";
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  worker.use(
    http.post("/api/login", () =>
      HttpResponse.json({ url: "https://device.example/verify", code: "ABCD-EFGH", attempt: 2 }),
    ),
    http.get("/api/whoami", () => {
      const answer = whoami[Math.min(whoamiReads, whoami.length - 1)];
      whoamiReads++;
      return HttpResponse.json(answer);
    }),
  );
});

afterEach(() => {
  vi.useRealTimers();
  worker.resetHandlers();
});

function status(): HTMLDivElement {
  return document.getElementById("modal-status") as HTMLDivElement;
}

function doors(): HTMLButtonElement[] {
  return [...document.querySelectorAll<HTMLButtonElement>("#login-modal .modal-btn")];
}

/** Starts a Builder ID sign-in and waits until the device code is on screen. */
async function startSignIn(): Promise<void> {
  (document.getElementById("modal-login-free") as HTMLButtonElement).click();
  await vi.waitFor(() => {
    expect(status().textContent).toContain("Code: ABCD-EFGH");
  });
}

/** Runs one 3 s poll interval and waits for that whoami read to be answered and judged. */
async function poll(): Promise<void> {
  const before = whoamiReads;
  await vi.advanceTimersByTimeAsync(3000);
  await vi.waitFor(() => {
    expect(whoamiReads).toBe(before + 1);
  });
  await vi.advanceTimersByTimeAsync(0);
}

describe("the login poll", () => {
  it("keeps polling while the old account stays signed in, and signs in when this attempt commits", async () => {
    whoami = [
      OLD_ACCOUNT_PENDING,
      OLD_ACCOUNT_PENDING,
      { ...OLD_ACCOUNT_PENDING, auth: "Logged in with Builder ID", loginState: "signed_in" },
    ];
    await startSignIn();
    expect(doors().every((b) => b.disabled)).toBe(true);
    await poll();
    await poll();
    expect(onLoggedIn).not.toHaveBeenCalled();
    await poll();
    await vi.waitFor(() => {
      expect(onLoggedIn).toHaveBeenCalledTimes(1);
    });
  });

  it("stops on this attempt's loginError while the old account stays signed in, and shows its text", async () => {
    whoami = [
      OLD_ACCOUNT_PENDING,
      {
        ...OLD_ACCOUNT_PENDING,
        loginState: "failed",
        loginError: "This account has no Kiro profile marotte can use",
      },
    ];
    await startSignIn();
    await poll();
    await poll();
    await vi.waitFor(() => {
      expect(status().textContent).toBe("This account has no Kiro profile marotte can use");
    });
    expect(onLoggedIn).not.toHaveBeenCalled();
    expect(doors().every((b) => !b.disabled)).toBe(true);
    // Ended: no further read is made.
    const reads = whoamiReads;
    await vi.advanceTimersByTimeAsync(9000);
    expect(whoamiReads).toBe(reads);
  });

  it("keeps polling on a plain signed_out, and stops on signed_in", async () => {
    const waiting: WhoamiResponse = { state: "signed_out", loginAttempt: 2, loginState: "pending" };
    whoami = [waiting, waiting, { state: "signed_in", loginAttempt: 2, loginState: "signed_in" }];
    await startSignIn();
    await poll();
    await poll();
    expect(onLoggedIn).not.toHaveBeenCalled();
    expect(status().textContent).toContain("Code: ABCD-EFGH");
    await poll();
    await vi.waitFor(() => {
      expect(onLoggedIn).toHaveBeenCalledTimes(1);
    });
  });

  it("gives up only after the vault's own 16-minute bound has passed", async () => {
    whoami = [OLD_ACCOUNT_PENDING];
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    const start = Date.now();
    await startSignIn();
    vi.setSystemTime(start + 16 * 60_000 + 30_000);
    await poll();
    expect(status().textContent).toContain("Code: ABCD-EFGH");
    vi.setSystemTime(start + 17 * 60_000);
    await vi.advanceTimersByTimeAsync(3000);
    await vi.waitFor(() => {
      expect(status().textContent).toBe("The sign-in did not finish in time. Try again.");
    });
    expect(doors().every((b) => !b.disabled)).toBe(true);
  });

  it("shows the server's refusal of the start and reopens the doors", async () => {
    worker.use(
      http.post("/api/login", () =>
        HttpResponse.json({ error: "login in progress" }, { status: 409 }),
      ),
    );
    (document.getElementById("modal-login-free") as HTMLButtonElement).click();
    await vi.waitFor(() => {
      expect(status().textContent).toBe("login in progress");
    });
    expect(doors().every((b) => !b.disabled)).toBe(true);
    expect(whoamiReads).toBe(0);
  });
});

describe("the sign-in start", () => {
  function modal(): HTMLDivElement {
    return document.getElementById("login-modal") as HTMLDivElement;
  }

  /** POST /api/login's first answer waits for `release`; later ones answer at once with this test's attempt 2. */
  function holdFirstStart(): { release: () => void; posted: () => number } {
    let posts = 0;
    let release!: () => void;
    const held = new Promise<void>((r) => (release = r));
    worker.use(
      http.post("/api/login", async () => {
        posts++;
        if (posts === 1) {
          await held;
          return HttpResponse.json({
            url: "https://device.example/verify",
            code: "LATE-LATE",
            attempt: 1,
          });
        }
        return HttpResponse.json({
          url: "https://device.example/verify",
          code: "ABCD-EFGH",
          attempt: 2,
        });
      }),
    );
    return { release, posted: () => posts };
  }

  afterEach(() => {
    closeModal(modal());
  });

  it("paints nothing and polls nothing for a start answered after the modal closed", async () => {
    whoami = [{ state: "signed_in", loginAttempt: 1, loginState: "signed_in" }];
    const start = holdFirstStart();
    openModal(modal());
    (document.getElementById("modal-login-free") as HTMLButtonElement).click();
    await vi.waitFor(() => {
      expect(start.posted()).toBe(1);
    });
    closeModal(modal());
    await vi.waitFor(() => {
      expect(doors().every((b) => !b.disabled)).toBe(true);
    });
    start.release();
    await vi.advanceTimersByTimeAsync(9000);
    expect(status().textContent).not.toContain("LATE-LATE");
    expect(whoamiReads, "a closed modal starts no poll").toBe(0);
    expect(onLoggedIn).not.toHaveBeenCalled();
  });

  it("keeps the reopened modal's sign-in when the closed one's start answers last", async () => {
    whoami = [{ state: "signed_in", loginAttempt: 2, loginState: "signed_in" }];
    const start = holdFirstStart();
    openModal(modal());
    (document.getElementById("modal-login-free") as HTMLButtonElement).click();
    await vi.waitFor(() => {
      expect(start.posted()).toBe(1);
    });
    closeModal(modal());
    await vi.waitFor(() => {
      expect(doors().every((b) => !b.disabled)).toBe(true);
    });
    openModal(modal());
    await startSignIn();
    start.release();
    await vi.advanceTimersByTimeAsync(0);
    expect(status().textContent).toContain("Code: ABCD-EFGH");
    await poll();
    await vi.waitFor(() => {
      expect(onLoggedIn).toHaveBeenCalledTimes(1);
    });
    expect(status().textContent).not.toContain("LATE-LATE");
  });
});

describe("loginPollVerdict", () => {
  const attempt = 2;
  it.each<[string, WhoamiResponse, ReturnType<typeof loginPollVerdict>]>([
    ["the attempt still pending", OLD_ACCOUNT_PENDING, { kind: "waiting" }],
    [
      "the attempt signed in",
      { state: "signed_in", loginAttempt: 2, loginState: "signed_in" },
      { kind: "signed_in" },
    ],
    [
      "the attempt signed in, then the account signed out",
      { state: "signed_out", loginAttempt: 2, loginState: "signed_in" },
      {
        kind: "ended",
        text: "The sign-in finished, then the account was signed out. Sign in again.",
      },
    ],
    [
      "a failure with no text",
      { state: "signed_out", loginAttempt: 2, loginState: "failed" },
      { kind: "ended", text: "The sign-in did not complete. Try again." },
    ],
    [
      "a sign-out cancelling the attempt",
      { state: "signed_out", loginAttempt: 2, loginState: "cancelled" },
      { kind: "ended", text: "The sign-in was cancelled. Try again." },
    ],
    [
      "a later attempt",
      { state: "signed_in", loginAttempt: 3, loginState: "pending" },
      {
        kind: "ended",
        text: "Another sign-in started, so this one stopped. Check the account menu.",
      },
    ],
    [
      "no attempt since marotte started",
      { state: "signed_in" },
      { kind: "ended", text: "marotte restarted before the sign-in finished. Try again." },
    ],
  ])("%s", (_name, wd, want) => {
    expect(loginPollVerdict(wd, attempt)).toEqual(want);
  });
});
