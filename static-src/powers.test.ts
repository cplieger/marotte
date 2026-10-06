import { vi, describe, it, expect, beforeEach } from "vitest";
import type * as ApiClient from "./api-client.js";
import type * as Confirm from "./confirm.js";
import type * as PowerActions from "./actions/powers.js";

interface Reply {
  ok: boolean;
  status: number;
  data: unknown;
  error: string;
}

let listReply: Reply;
let serversReply: unknown;
let confirmAnswer = true;
const installed: string[] = [];
const removed: string[] = [];
let listCalls = 0;
let hold: Promise<void> | null = null;

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetTypedOrError: vi.fn(async (_path: string, decode: (v: unknown) => unknown) => {
    listCalls++;
    if (hold !== null) {
      await hold;
    }
    return listReply.ok ? { ...listReply, data: decode(listReply.data) } : listReply;
  }),
  apiGetTyped: vi.fn(async (_path: string, decode: (v: unknown) => unknown) =>
    decode(serversReply),
  ),
}));
vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Confirm>()),
  confirm: vi.fn(async () => confirmAnswer),
}));
vi.mock("./actions/powers.js", async (importOriginal) => ({
  ...(await importOriginal<typeof PowerActions>()),
  installPower: {
    dispatch: vi.fn(async (args: { name: string }) => {
      installed.push(args.name);
      return undefined;
    }),
  },
  uninstallPower: {
    dispatch: vi.fn(async (args: { name: string }) => {
      removed.push(args.name);
      return undefined;
    }),
  },
}));

import {
  renderPowersPanel,
  setPowerCountsListener,
  powerSections,
  serverSentence,
  _resetPowersForTest,
} from "./powers.js";
import type { PowerRow } from "./powers.js";
import { confirm } from "./confirm.js";
import { dispatch } from "./bus.js";

function row(name: string, over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    name,
    display_name: name.toUpperCase(),
    description: `${name} description`,
    category: "Data",
    installed: false,
    in_catalog: true,
    ...over,
  };
}

function ok(data: unknown): Reply {
  return { ok: true, status: 200, data, error: "" };
}

async function settle(): Promise<void> {
  for (let i = 0; i < 6; i++) {
    await Promise.resolve();
    await new Promise((r) => setTimeout(r, 0));
  }
}

let panel: HTMLElement;

async function mount(filter = ""): Promise<void> {
  renderPowersPanel(panel, filter);
  await settle();
}

function sectionLabels(): string[] {
  return [...panel.querySelectorAll(".docs-section > .entry-section-label")].map(
    (n) => n.textContent ?? "",
  );
}

function names(): string[] {
  return [...panel.querySelectorAll("[data-power]")].map((n) => n.getAttribute("data-power") ?? "");
}

function pr(name: string, over: Partial<PowerRow> = {}): PowerRow {
  return {
    name,
    displayName: "",
    description: "",
    category: "",
    publisherTier: "",
    authType: "",
    origin: "",
    mcpServers: [],
    issues: 0,
    installed: false,
    inCatalog: true,
    ...over,
  };
}

beforeEach(() => {
  _resetPowersForTest();
  document.body.replaceChildren();
  panel = document.createElement("div");
  document.body.appendChild(panel);
  installed.length = 0;
  removed.length = 0;
  listCalls = 0;
  hold = null;
  confirmAnswer = true;
  vi.mocked(confirm).mockClear();
  serversReply = { servers: ["postman"], known: true };
  listReply = ok({
    catalog: "ready",
    installed: "ready",
    powers: [
      row("zeta", { category: "Zebra" }),
      row("alpha", { category: "" }),
      row("beta", { category: "Apps" }),
      row("gamma", { installed: true, category: "Apps", origin: "workspace" }),
    ],
  });
});

describe("powerSections", () => {
  it("puts Installed first, categories alphabetically, and Other last", () => {
    const out = powerSections([
      pr("a", { category: "Zebra" }),
      pr("b"),
      pr("c", { category: "Apps" }),
      pr("d", { installed: true, category: "Apps" }),
    ]);
    expect(out.map((s) => s.label)).toEqual(["Installed", "Apps", "Zebra", "Other"]);
    expect(out[0]?.rows.map((r) => r.name)).toEqual(["d"]);
  });
});

describe("serverSentence", () => {
  it("names one server and says it runs unsandboxed", () => {
    expect(serverSentence({ servers: ["postman"], known: true })).toBe(
      "It starts the MCP server postman, which runs unsandboxed, with your access.",
    );
  });

  it("says it could not read the servers when the answer is unknown or missing", () => {
    const unknown =
      "Its MCP servers could not be read ahead of time; any it declares start right away, in open chats too, and run unsandboxed, with your access.";
    expect(serverSentence({ servers: ["x"], known: false })).toBe(unknown);
    expect(serverSentence(null)).toBe(unknown);
  });

  it("says a Power with no servers declares none", () => {
    expect(serverSentence({ servers: [], known: true })).toBe("It declares no MCP servers.");
  });
});

describe("the list", () => {
  it("renders the sections and marks an installed row with its origin", async () => {
    await mount();
    expect(sectionLabels()).toEqual(["Installed", "Apps", "Zebra", "Other"]);
    const g = panel.querySelector('[data-power="gamma"]');
    expect(g?.textContent).toContain("Installed");
    expect(g?.textContent).toContain("workspace");
    expect(g?.querySelector(".powers-install")).toBeNull();
  });

  it("reports counts and filters without refetching", async () => {
    const seen: { total: number; shown: number }[] = [];
    setPowerCountsListener((c) => seen.push(c));
    await mount();
    const before = listCalls;
    await mount("zeta");
    expect(listCalls).toBe(before);
    expect(names()).toEqual(["zeta"]);
    expect(seen.at(-1)).toEqual({ total: 4, shown: 1 });
  });

  it("keeps one initial request in flight while the filter is typed", async () => {
    let release = (): void => undefined;
    hold = new Promise<void>((r) => {
      release = r;
    });
    renderPowersPanel(panel, "");
    renderPowersPanel(panel, "z");
    renderPowersPanel(panel, "ze");
    renderPowersPanel(panel, "zeta");
    await settle();
    expect(listCalls).toBe(1);
    release();
    await settle();
    expect(listCalls).toBe(1);
    expect(names()).toEqual(["zeta"]);
  });

  it("notes a missing catalogue and a missing installed list", async () => {
    listReply = ok({ catalog: "unavailable", installed: "unavailable", powers: [] });
    await mount();
    const notes = [...panel.querySelectorAll(".powers-note")].map((n) => n.textContent ?? "");
    expect(notes).toEqual([
      "Couldn't load the Powers catalogue, so only installed Powers are listed.",
      "Couldn't read which Powers are installed, so every Power shows Install.",
    ]);
  });

  it("names each installed Power KAS could not load, with its reason", async () => {
    listReply = ok({
      catalog: "ready",
      installed: "partial",
      powers: [row("gamma", { installed: true })],
      load_errors: [
        { name: "bad", message: "the power could not be read" },
        { name: "worse", message: "missing POWER.md." },
      ],
    });
    await mount();
    const notes = [...panel.querySelectorAll(".powers-note")].map((n) => n.textContent ?? "");
    expect(notes).toEqual([
      "Couldn't load 2 installed Powers. bad: the power could not be read; worse: missing POWER.md.",
    ]);
    expect(names()).toEqual(["gamma"]);
  });

  it("offers no Install while an administrator blocks Powers, and keeps Uninstall", async () => {
    listReply = ok({
      catalog: "ready",
      installed: "ready",
      blocked: "Powers are blocked by your organization",
      powers: [row("zeta"), row("gamma", { installed: true })],
    });
    await mount();
    expect(panel.querySelector(".powers-install")).toBeNull();
    expect(panel.querySelector('[aria-label="Uninstall GAMMA"]')).not.toBeNull();
    const notes = [...panel.querySelectorAll(".powers-note")].map((n) => n.textContent ?? "");
    expect(notes).toEqual([
      "Powers are blocked by your organization. Installed Powers can still be removed.",
    ]);
  });

  it("disables Install until the organization's settings load", async () => {
    listReply = ok({
      catalog: "ready",
      installed: "ready",
      policy_pending: true,
      powers: [row("zeta")],
    });
    await mount();
    const install = panel.querySelector<HTMLButtonElement>('[aria-label="Install ZETA"]');
    expect(install?.disabled).toBe(true);
    const notes = [...panel.querySelectorAll(".powers-note")].map((n) => n.textContent ?? "");
    expect(notes).toEqual([
      "Your organization's settings have not loaded yet, so Powers cannot be installed.",
    ]);

    listReply = ok({ catalog: "ready", installed: "ready", powers: [row("zeta")] });
    dispatch({ type: "powers_changed", chat_id: "" });
    await settle();
    expect(panel.querySelector<HTMLButtonElement>('[aria-label="Install ZETA"]')?.disabled).toBe(
      false,
    );
  });

  it("offers Retry on a failed load", async () => {
    listReply = { ok: false, status: 502, data: null, error: "powers request failed" };
    await mount();
    expect(panel.textContent).toContain("Couldn't load Powers.");
    listReply = ok({ catalog: "ready", installed: "ready", powers: [row("back")] });
    panel.querySelector<HTMLButtonElement>(".powers-failed button")?.click();
    await settle();
    expect(names()).toEqual(["back"]);
  });

  it("refetches on powers_changed while showing", async () => {
    await mount();
    listReply = ok({ catalog: "ready", installed: "ready", powers: [row("fresh")] });
    dispatch({ type: "powers_changed", chat_id: "" });
    await settle();
    expect(names()).toEqual(["fresh"]);
  });
});

describe("install and uninstall", () => {
  it("names the server in the confirm and installs on yes", async () => {
    await mount();
    panel.querySelector<HTMLButtonElement>('[aria-label="Install ZETA"]')?.click();
    await settle();
    expect(vi.mocked(confirm).mock.calls[0]?.[0]).toBe(
      "Install the ZETA Power? It starts the MCP server postman, which runs unsandboxed, with your access.",
    );
    expect(installed).toEqual(["zeta"]);
  });

  it("installs nothing when the confirm is refused", async () => {
    confirmAnswer = false;
    await mount();
    panel.querySelector<HTMLButtonElement>('[aria-label="Install ZETA"]')?.click();
    await settle();
    expect(installed).toEqual([]);
  });

  it("uninstalls behind a destructive confirm", async () => {
    await mount();
    panel.querySelector<HTMLButtonElement>('[aria-label="Uninstall GAMMA"]')?.click();
    await settle();
    expect(vi.mocked(confirm).mock.calls[0]?.[2]).toBe("destructive");
    expect(removed).toEqual(["gamma"]);
  });
});
