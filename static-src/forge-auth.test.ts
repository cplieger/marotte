import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(() => Promise.resolve(null)),
  apiPost: vi.fn(() => Promise.resolve(null)),
  CancellableSlot: class {
    start() {
      return new AbortController().signal;
    }
    abort() {
      /* mock no-op */
    }
  },
  withTimeout: vi.fn(
    (signal: AbortSignal | undefined, _ms: number) => signal ?? AbortSignal.timeout(30000),
  ),
  API_TIMEOUT_MS: 30000,
}));

vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  confirm: vi.fn(() => Promise.resolve(true)),
}));

// Routed through the mocked client so each case answers the forge list first with mockResolvedValueOnce.
vi.mock("./forge-store.js", async (importOriginal) => {
  const orig = await importOriginal<Record<string, unknown>>();
  const { apiGetTyped } = await import("./api-client.js");
  const forgeRead = (): unknown => apiGetTyped("/api/forges", (v: unknown) => v);
  return {
    ...orig,
    refreshForges: forgeRead,
    ensureForges: forgeRead,
    initForgeStore: vi.fn(),
  };
});

import { renderForgesPanel } from "./forge-auth.js";
import { apiGetTyped, apiPost } from "./api-client.js";
import { confirm as confirmDialog } from "./confirm.js";
import { iconEl } from "./icon-el.js";
import { ICON_CLOSE_UI, ICON_PLUS_UI } from "./icons.js";

const mockedApiGet = vi.mocked(apiGetTyped);
const mockedApiPost = vi.mocked(apiPost);
const mockedConfirm = vi.mocked(confirmDialog);

function setupDOM(): void {
  document.body.innerHTML = `<div id="forges-panel"></div>`;
}

function panel(): HTMLElement {
  return document.getElementById("forges-panel")!;
}

describe("forge-auth: race condition guards", () => {
  beforeEach(() => {
    setupDOM();
    vi.clearAllMocks();
  });

  it("concurrent renderForgesPanel calls: only the latest render paints", async () => {
    // The first call resolves after the second; the panel must show the second's answer, not the stale one.
    let resolveFirst!: (v: unknown) => void;
    const firstPromise = new Promise((r) => {
      resolveFirst = r;
    });
    mockedApiGet.mockReturnValueOnce(firstPromise);

    mockedApiGet.mockResolvedValueOnce({
      forges: [
        {
          id: "gitlab:gitlab.com",
          kind: "gitlab",
          host: "gitlab.com",
          username: "fast",
          connected: true,
          reconnect_required: false,
        },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });

    const p1 = renderForgesPanel({ revalidate: false });
    const p2 = renderForgesPanel({ revalidate: false });

    await p2;

    resolveFirst({
      forges: [
        {
          id: "github:github.com",
          kind: "github",
          host: "github.com",
          username: "stale",
          connected: true,
          reconnect_required: false,
        },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await p1;

    const rows = panel().querySelectorAll(".forge-account-row");
    expect(rows.length).toBe(1);
    expect(panel().querySelector(".forge-account-primary")?.textContent).toBe("fast");
  });
});

describe("forge-auth: 4-section layout", () => {
  beforeEach(() => {
    setupDOM();
    vi.clearAllMocks();
  });

  it("renders one section per supported forge kind, then Another server", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const sections = panel().querySelectorAll<HTMLElement>(".forge-kind-section");
    const kinds = [...sections].map((s) => s.dataset["kind"]);
    expect(kinds).toEqual(["github", "gitlab", "codeberg", "gitea", "other"]);
    expect(sections[4]!.querySelector(".forge-kind-title")?.textContent).toBe("Another server");
  });

  it("each section renders an Add account button (no empty-state filler)", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    for (const s of panel().querySelectorAll<HTMLElement>(".forge-kind-section")) {
      // An empty section is just its header and the + button: no "no accounts" filler.
      expect(s.querySelector(".forge-account-empty")).toBeNull();
      expect(s.querySelector("[data-forge-add]")).not.toBeNull();
    }
  });

  it("the add pane opens above the account list, next to the + that opened it", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        {
          id: "github:github.com",
          kind: "github",
          host: "github.com",
          username: "alice",
          email: "a@x.io",
          connected: true,
          reconnect_required: false,
        },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel({ revalidate: false });
    const section = panel().querySelector<HTMLElement>(".forge-kind-section[data-kind='github']")!;
    section.querySelector<HTMLButtonElement>("[data-forge-add]")!.click();

    const kids = [...section.children];
    const header = kids.indexOf(section.querySelector(".forge-kind-header")!);
    const slot = kids.indexOf(section.querySelector("[data-forge-slot]")!);
    const list = kids.indexOf(section.querySelector(".forge-account-list")!);
    expect(section.querySelector("form.forge-pat-form")).not.toBeNull();
    expect(header).toBeLessThan(slot);
    expect(slot).toBeLessThan(list);
  });

  it("clicking the + button opens a unified add pane (no separate Add-a-PAT button)", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    for (const k of ["github", "gitlab", "codeberg", "gitea"]) {
      const pat = panel().querySelector(
        `.forge-kind-section[data-kind='${k}'] [data-forge-add-pat]`,
      );
      expect(pat, `${k} should not have a separate Add-a-PAT button`).toBeNull();
    }
  });

  it("renders one slim row per connected account, prefers email over username", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        {
          id: "github:github.com",
          kind: "github",
          host: "github.com",
          username: "alice",
          email: "alice@example.com",
          connected: true,
          reconnect_required: false,
        },
        {
          id: "gitlab:gitlab.com",
          kind: "gitlab",
          host: "gitlab.com",
          username: "bob",
          connected: true,
          reconnect_required: false,
        },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();

    const ghRow = panel().querySelector<HTMLElement>(
      ".forge-kind-section[data-kind='github'] .forge-account-row",
    );
    expect(ghRow).not.toBeNull();
    expect(ghRow!.querySelector(".forge-account-primary")?.textContent).toBe("alice@example.com");
    expect(ghRow!.querySelector(".forge-account-meta")?.textContent).toContain("@alice");
    expect(ghRow!.querySelector(".forge-account-meta")?.textContent).toContain("github.com");

    const glRow = panel().querySelector<HTMLElement>(
      ".forge-kind-section[data-kind='gitlab'] .forge-account-row",
    );
    expect(glRow!.querySelector(".forge-account-primary")?.textContent).toBe("bob");
  });

  it("each account row has a Manage link and a Sign out button", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        {
          id: "github:github.com",
          kind: "github",
          host: "github.com",
          username: "alice",
          email: "a@x.io",
          connected: true,
          reconnect_required: false,
        },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const row = panel().querySelector<HTMLElement>(".forge-account-row")!;
    const manage = row.querySelector<HTMLAnchorElement>(".forge-account-manage");
    const signOut = [...row.querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent === "Sign out",
    );
    expect(manage).not.toBeNull();
    expect(manage!.href).toContain("github.com");
    expect(manage!.href).toContain("settings/profile");
    expect(signOut).toBeDefined();
  });

  it("shows error styling and last_error text on a disconnected account", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        {
          id: "github:github.com",
          kind: "github",
          host: "github.com",
          username: "alice",
          connected: false,
          reconnect_required: false,
          last_error: "token expired",
        },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const row = panel().querySelector<HTMLElement>(".forge-account-row")!;
    expect(row.classList.contains("forge-account-row-error")).toBe(true);
    expect(row.querySelector(".forge-account-error")?.textContent).toBe("token expired");
  });

  it("renders a list error message when /api/forges fails", async () => {
    mockedApiGet.mockResolvedValueOnce(null);
    await renderForgesPanel();
    expect(panel().querySelector(".forge-error")).not.toBeNull();
  });

  it("renders an SVG icon (not a letter) in each kind badge", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    for (const k of ["github", "gitlab", "codeberg", "gitea"]) {
      const badge = panel().querySelector<HTMLElement>(
        `.forge-kind-section[data-kind='${k}'] .forge-kind-badge`,
      );
      expect(badge).not.toBeNull();
      const svg = badge!.querySelector("svg");
      expect(svg, `${k} badge should contain an svg`).not.toBeNull();
      expect(svg!.getAttribute("viewBox")).toBe("0 0 24 24");
      expect(badge!.textContent?.trim() ?? "").toBe("");
    }
  });

  it("the host is fixed for codeberg and editable elsewhere, defaulting to the public instance", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    for (const k of ["github", "gitlab", "codeberg", "gitea"]) {
      const add = panel().querySelector<HTMLButtonElement>(
        `.forge-kind-section[data-kind='${k}'] [data-forge-add]`,
      );
      add!.click();
    }
    const inputs: Record<string, HTMLInputElement | null> = {};
    for (const k of ["github", "gitlab", "codeberg", "gitea"]) {
      inputs[k] = panel().querySelector<HTMLInputElement>(
        `.forge-kind-section[data-kind='${k}'] [data-forge-host]`,
      );
      expect(inputs[k], `${k} host field`).not.toBeNull();
    }
    expect(inputs["codeberg"]!.type).toBe("hidden");
    expect(inputs["codeberg"]!.value).toBe("codeberg.org");
    expect(inputs["github"]!.type).toBe("text");
    expect(inputs["github"]!.value).toBe("github.com");
    expect(inputs["gitlab"]!.type).toBe("text");
    expect(inputs["gitlab"]!.value).toBe("gitlab.com");
    expect(inputs["gitea"]!.type).toBe("text");
    expect(inputs["gitea"]!.value).toBe("");
    expect(inputs["gitea"]!.placeholder).toBe("your-host.example.com");
  });

  it("the gitea section is labeled 'Gitea / Forgejo'", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const title = panel().querySelector(
      ".forge-kind-section[data-kind='gitea'] .forge-kind-title",
    )?.textContent;
    expect(title).toBe("Gitea / Forgejo");
  });

  it("does not duplicate @username in meta when the primary line is already the username", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        // No email: the primary line is the username, and the meta line adds no @username.
        {
          id: "github:github.com",
          kind: "github",
          host: "github.com",
          username: "cplieger",
          connected: true,
          reconnect_required: false,
        },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const row = panel().querySelector<HTMLElement>(".forge-account-row")!;
    expect(row.querySelector(".forge-account-primary")?.textContent).toBe("cplieger");
    const meta = row.querySelector(".forge-account-meta")?.textContent ?? "";
    expect(meta).not.toContain("@cplieger");
    expect(meta).toBe("github.com");
  });

  it("clicking 'Add account' twice toggles the form open then closed", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const section = panel().querySelector<HTMLElement>(".forge-kind-section[data-kind='gitlab']")!;
    const btn = section.querySelector<HTMLButtonElement>("[data-forge-add]")!;
    const slot = section.querySelector<HTMLElement>("[data-forge-slot]")!;
    btn.click();
    expect(slot.querySelector("form.forge-pat-form"), "first click opens").not.toBeNull();
    expect(slot.dataset["mode"]).toBe("add");
    btn.click();
    expect(slot.querySelector("form.forge-pat-form"), "second click closes").toBeNull();
    expect(slot.dataset["mode"]).toBeUndefined();
  });

  it("turns the + into a close mark while its pane is open, and back once it closes", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const section = panel().querySelector<HTMLElement>(".forge-kind-section[data-kind='gitlab']")!;
    const btn = section.querySelector<HTMLButtonElement>("[data-forge-add]")!;
    const glyph = (svg: string): string => iconEl(svg).outerHTML;
    expect(btn.getAttribute("aria-expanded")).toBe("false");
    expect(btn.innerHTML).toBe(glyph(ICON_PLUS_UI));

    btn.click();
    expect(btn.getAttribute("aria-expanded")).toBe("true");
    expect(btn.getAttribute("data-tooltip")).toBe("Close");
    expect(btn.getAttribute("aria-label")).toBe("Close");
    expect(btn.innerHTML).toBe(glyph(ICON_CLOSE_UI));

    btn.click();
    expect(btn.getAttribute("aria-expanded")).toBe("false");
    expect(btn.getAttribute("data-tooltip")).toBe("Add an account");
    expect(btn.getAttribute("aria-label")).toBe("Add an account");
    expect(btn.innerHTML).toBe(glyph(ICON_PLUS_UI));
  });

  it("re-probes connected accounts in the background on page open", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        {
          id: "github:github.com",
          kind: "github",
          host: "github.com",
          username: "alice",
          connected: true,
          reconnect_required: false,
        },
        {
          id: "codeberg:codeberg.org",
          kind: "codeberg",
          host: "codeberg.org",
          username: "bob",
          connected: true,
          reconnect_required: false,
        },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    mockedApiGet.mockResolvedValueOnce({ repos: [] });
    await renderForgesPanel();
    // Flushes the void revalidateInBackground microtasks.
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    const probeCalls = mockedApiPost.mock.calls.filter(
      ([path]) => typeof path === "string" && path.includes("/probe"),
    );
    expect(probeCalls.length).toBe(2);
    expect(probeCalls.some(([p]) => p.includes("github%3Agithub.com"))).toBe(true);
    expect(probeCalls.some(([p]) => p.includes("codeberg%3Acodeberg.org"))).toBe(true);
  });

  it("does not probe disconnected accounts on page open", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        {
          id: "github:github.com",
          kind: "github",
          host: "github.com",
          username: "alice",
          connected: false,
          reconnect_required: false,
          last_error: "expired",
        },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    await Promise.resolve();
    await Promise.resolve();

    const probeCalls = mockedApiPost.mock.calls.filter(
      ([path]) => typeof path === "string" && path.includes("/probe"),
    );
    expect(probeCalls.length).toBe(0);
  });

  it("sign-out uses the custom styled confirm dialog (not the native popup)", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        {
          id: "github:github.com",
          kind: "github",
          host: "github.com",
          username: "alice",
          email: "a@x.io",
          connected: true,
          reconnect_required: false,
        },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    mockedApiGet.mockImplementation(((url: string) =>
      Promise.resolve(
        url === "/api/forges"
          ? { forges: [], kinds: ["github", "gitlab", "codeberg", "gitea"] }
          : null,
      )) as typeof apiGetTyped);
    mockedConfirm.mockResolvedValueOnce(true);
    const fetchSpy = vi.fn(() => Promise.resolve(new Response(null, { status: 204 })));
    vi.stubGlobal("fetch", fetchSpy);

    await renderForgesPanel();
    const signOutBtn = [
      ...panel().querySelectorAll<HTMLButtonElement>(".forge-account-row button"),
    ].find((b) => b.textContent === "Sign out")!;
    signOutBtn.click();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(mockedConfirm).toHaveBeenCalled();
    const [msg, label, variant] = mockedConfirm.mock.calls[0]!;
    expect(msg).toContain("Sign out of a@x.io");
    expect(label).toBe("Sign out");
    expect(variant).toBe("destructive");
    expect(fetchSpy).toHaveBeenCalledWith(
      "/api/forges/github%3Agithub.com",
      expect.objectContaining({ method: "DELETE" }),
    );
  });

  it("sign-out cancellation does not delete", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        {
          id: "github:github.com",
          kind: "github",
          host: "github.com",
          username: "alice",
          connected: true,
          reconnect_required: false,
        },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    mockedConfirm.mockResolvedValueOnce(false);
    const fetchSpy = vi.fn(() => Promise.resolve(new Response(null, { status: 204 })));
    vi.stubGlobal("fetch", fetchSpy);

    await renderForgesPanel();
    fetchSpy.mockClear();
    const signOutBtn = [
      ...panel().querySelectorAll<HTMLButtonElement>(".forge-account-row button"),
    ].find((b) => b.textContent === "Sign out")!;
    signOutBtn.click();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(mockedConfirm).toHaveBeenCalled();
    expect(fetchSpy).not.toHaveBeenCalled();
  });
});
