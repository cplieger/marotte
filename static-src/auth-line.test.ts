// The card's auth row and its separator are ONE fact, and a refused logout restores the
// VERDICT. The row is an ERROR row: `signed_in` renders empty and hides, and the two
// remaining arms explain the blank address. `setAuthLine` writes row and separator
// together. `signed_out` and `unavailable` both blank the address, so only the whole
// verdict is restorable. `settings.ts` is real here; its heavy graph is mocked.
import { vi, describe, it, expect, beforeEach } from "vitest";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";

// Everything `settings.ts` reaches that touches network, shell, file browser or chat view.
vi.mock("./modals.js", () => ({ initAllModals: vi.fn(), showLoginModal: vi.fn() }));
// The COMPLETE tabs mock: Browser Mode links ESM for real, so a partial factory fails once
// the graph widens (see __test-helpers__/tabs-mock.ts).
vi.mock("./tabs.js", async () => ({
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
}));
vi.mock("./git-badge.js", () => ({ initGitBadge: vi.fn() }));
vi.mock("./git-tabs.js", () => ({ getGitTab: vi.fn(() => "changes") }));
vi.mock("./files.js", () => ({ noteDefaultBrowsePath: vi.fn() }));
vi.mock("./shell.js", () => ({ restoreShell: vi.fn() }));
vi.mock("./tools.js", () => ({ initTools: vi.fn(), loadToolsList: vi.fn() }));
vi.mock("./notify.js", () => ({ restoreNotifications: vi.fn() }));
vi.mock("./permissions-ui.js", () => ({
  initPermissionsUI: vi.fn(),
  initNativePolicyUI: vi.fn(),
  loadNativePolicy: vi.fn(),
}));
vi.mock("./mcp-ui.js", () => ({ initMCP: vi.fn() }));
vi.mock("./knowledge.js", () => ({ initKnowledge: vi.fn(), loadKnowledge: vi.fn() }));
vi.mock("./settings-steering.js", () => ({
  initSteeringEditor: vi.fn(),
  loadSteeringDoc: vi.fn(),
}));
vi.mock("./settings-notifications.js", () => ({ initNotificationToggles: vi.fn() }));
vi.mock("./api-client.js", () => ({ apiGet: vi.fn(async () => null), apiPost: vi.fn() }));
vi.mock("./governance.js", () => ({
  paintSettingLocks: vi.fn(),
  writeSwitch: (input: HTMLInputElement, value: boolean) => {
    input.checked = value;
  },
}));
vi.mock("./actions/tools.js", () => ({ runDiagnostics: vi.fn() }));

vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));

const mockFetch = vi.fn();

const { renderIdentity, currentIdentity } = await import("./settings.js");
const { logout } = await import("./actions/settings.js");

/**
 * The card's three rows as `static/index.html` authors them, including `#st-auth-sep`,
 * which only `setAuthLine` in `settings.ts` reaches.
 */
function seedCard(): { row: HTMLElement; sep: HTMLElement; addr: HTMLElement } {
  document.body.innerHTML = `
    <button type="button" id="account-btn">
      <span id="status-dot"></span><span id="user-email"></span>
    </button>
    <span id="status-card">
      <span class="pill-detail" id="st-ws">-</span>
      <span class="pill-sep"></span>
      <span class="pill-detail" id="st-kiro">-</span>
      <span class="pill-sep" id="st-auth-sep" hidden></span>
      <span class="pill-detail" id="st-auth" hidden></span>
    </span>`;
  return {
    row: document.getElementById("st-auth") as HTMLElement,
    sep: document.getElementById("st-auth-sep") as HTMLElement,
    addr: document.getElementById("user-email") as HTMLElement,
  };
}

beforeEach(() => {
  // The action framework holds module state, so it is reset per case.
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
  seedCard();
});

describe("the row and its separator are one fact", () => {
  it("hides BOTH for signed_in, because the address already says it", () => {
    const { row, sep, addr } = seedCard();
    renderIdentity({ state: "signed_in", email: "someone@example.invalid" });
    expect(addr.textContent, "the address carries the statement").toBe("someone@example.invalid");
    expect(row.textContent, "the row says nothing").toBe("");
    expect(row.hidden, "so the row hides").toBe(true);
    expect(sep.hidden, "and its separator hides with it").toBe(true);
  });

  it.each([
    ["signed_out", { state: "signed_out" } as const, "not signed in"],
    ["unavailable", { state: "unavailable", reason: "whoami unreachable" } as const, "unknown"],
  ])("shows BOTH for %s, which is what explains the blank address", (_name, verdict, wording) => {
    const { row, sep, addr } = seedCard();
    renderIdentity(verdict);
    expect(addr.textContent, "these are exactly the arms that name nobody").toBe("");
    expect(row.textContent).toBe(wording);
    expect(row.hidden).toBe(false);
    expect(sep.hidden, "the separator appears with the row").toBe(false);
  });

  it("never leaves one visible without the other, across every transition", () => {
    // Every ordered pair of arms: a second writer would forget one element on ONE path.
    const { row, sep } = seedCard();
    const arms = [
      { state: "signed_in", email: "a@b.invalid" },
      { state: "signed_out" },
      { state: "unavailable", reason: "x" },
    ] as const;
    for (const from of arms) {
      for (const to of arms) {
        renderIdentity(from);
        renderIdentity(to);
        expect(
          sep.hidden,
          `${from.state} -> ${to.state}: the separator disagreed with the row`,
        ).toBe(row.hidden);
      }
    }
  });
});

describe("currentIdentity", () => {
  it("answers what renderIdentity last rendered", () => {
    // The export exists for the logout rollback, which receives the VALUE; nothing else
    // retains the verdict.
    seedCard();
    const v = { state: "signed_in", email: "someone@example.invalid" } as const;
    renderIdentity(v);
    expect(currentIdentity()).toEqual(v);
    const u = { state: "unavailable", reason: "whoami unreachable" } as const;
    renderIdentity(u);
    expect(currentIdentity()).toEqual(u);
  });
});

describe("logout through the REAL action", () => {
  it("leaves the row and its separator visible on success", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { row, sep, addr } = seedCard();
    renderIdentity({ state: "signed_in", email: "someone@example.invalid" });

    await logout.dispatch({ render: renderIdentity, prev: currentIdentity() });

    expect(addr.textContent, "the address clears").toBe("");
    expect(row.textContent).toBe("not signed in");
    expect(row.hidden, "and the row is what explains the blank").toBe(false);
    expect(sep.hidden).toBe(false);
  });

  it("restores UNKNOWN, not 'not signed in', when a logout from unavailable is refused", async () => {
    // `signed_out` and `unavailable` both render an empty address, so restoring an address
    // could not tell which to restore.
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "nope" }), { status: 500 }));
    const { row, sep } = seedCard();
    renderIdentity({ state: "unavailable", reason: "whoami unreachable" });
    expect(row.textContent, "the premise: the row reads unknown").toBe("unknown");

    await logout.dispatch({ render: renderIdentity, prev: currentIdentity() });

    expect(row.textContent, "the refusal restores the verdict it replaced").toBe("unknown");
    expect(row.hidden).toBe(false);
    expect(sep.hidden).toBe(false);
    expect(currentIdentity().state).toBe("unavailable");
  });

  it("restores the SIGNED-IN verdict, address and all, on a refusal", async () => {
    // The third arm, so all three are covered and the case above cannot pass by
    // rollback simply doing nothing.
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "nope" }), { status: 500 }));
    const { row, sep, addr } = seedCard();
    renderIdentity({ state: "signed_in", email: "someone@example.invalid" });

    await logout.dispatch({ render: renderIdentity, prev: currentIdentity() });

    expect(addr.textContent).toBe("someone@example.invalid");
    expect(row.hidden, "signed_in hides the row again").toBe(true);
    expect(sep.hidden).toBe(true);
  });
});
