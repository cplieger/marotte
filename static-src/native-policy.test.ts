import { describe, it, expect, beforeEach, vi } from "vitest";
import type { PolicyView } from "./types.js";

type SSEFn = (chatID: string, payload?: unknown) => void;

const mocks = vi.hoisted(() => ({
  apiGet: vi.fn<(path: string, signal?: AbortSignal) => Promise<PolicyView | null>>(),
  editDispatch:
    vi.fn<(args: Record<string, unknown>) => Promise<{ ok?: boolean; error?: string } | null>>(),
  explainDispatch: vi.fn<(args: Record<string, unknown>) => Promise<unknown>>(),
  confirm: vi.fn<(msg: string, label?: string) => Promise<boolean>>(),
  sseHandlers: new Map<string, SSEFn>(),
  busHandlers: new Map<string, () => void>(),
}));

vi.mock("./api-client.js", () => ({ apiGet: mocks.apiGet }));
vi.mock("./persist.js", () => ({ patchSettings: vi.fn(), settingsAsShown: vi.fn(() => null) }));
vi.mock("./confirm.js", () => ({ confirm: mocks.confirm }));
vi.mock("./bus.js", () => ({
  onSSE: (type: string, fn: SSEFn) => {
    mocks.sseHandlers.set(type, fn);
    return () => mocks.sseHandlers.delete(type);
  },
  onBus: (type: string, fn: () => void) => {
    mocks.busHandlers.set(type, fn);
    return () => mocks.busHandlers.delete(type);
  },
  BUS_ENGINE_READY: "engine:ready",
  // Present-but-inert so real-ESM linking succeeds; no case calls them.
  apiGetTyped: vi.fn(),
}));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
  bindLoadingState: vi.fn(() => vi.fn()),
}));
vi.mock("./actions/permissions.js", () => ({
  editNativeRule: { name: "permissions.edit_native_rule", dispatch: mocks.editDispatch },
  explainPolicy: { name: "permissions.explain", dispatch: mocks.explainDispatch },
  setSecurityProfile: { name: "permissions.set_profile", dispatch: vi.fn() },
}));

import { initNativePolicyUI, loadNativePolicy } from "./permissions-ui.js";
import { byId } from "./dom.js";
import { shippedAddForm } from "./__test-helpers__/add-forms.js";

function initAndLoad(): void {
  initNativePolicyUI();
  loadNativePolicy();
}

function el<T extends HTMLElement>(tag: string, id: string): T {
  const e = document.createElement(tag) as T;
  e.id = id;
  return e;
}
async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

const sampleView: PolicyView = {
  available: true,
  writable_scopes: ["user", "workspace"],
  capabilities: ["fs_read", "fs_write", "shell", "web_fetch"],
  // Empty: this fixture has no relaxation checkbox (permissions-relax.test.ts owns it).
  profiles: [{ id: "guarded", presets: ["read-workspace"] }],
  profile: "guarded",
  load_errors: [],
  rules: [
    {
      capability: "fs_write",
      effect: "deny",
      match: ["**/.git/**"],
      scope: "kiro",
      source: "kiro-scope",
      editable: false,
    },
    {
      capability: "shell",
      effect: "allow",
      scope: "user",
      source: "/home/u/.kiro/settings/permissions.yaml",
      editable: true,
    },
    {
      capability: "fs_read",
      effect: "ask",
      match: ["src/**"],
      scope: "workspace",
      source: "/home/u/.kiro/workspace-roots/ab/permissions.yaml",
      editable: true,
    },
  ],
};

function policyRows(): HTMLElement[] {
  return Array.from(
    byId<HTMLDivElement>("native-policy-list").querySelectorAll<HTMLElement>(".native-rule"),
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.sseHandlers.clear();
  mocks.busHandlers.clear();
  document.body.replaceChildren();
  document.body.appendChild(el("p", "native-policy-status"));
  document.body.appendChild(el("div", "native-policy-list"));
  document.body.appendChild(el("p", "native-policy-empty-hint"));
  document.body.appendChild(shippedAddForm("native-rule-capability"));
  document.body.appendChild(shippedAddForm("native-explain-capability"));
  document.body.appendChild(el("p", "native-explain-result"));

  mocks.apiGet.mockResolvedValue(sampleView);
  mocks.editDispatch.mockResolvedValue({ ok: true });
  mocks.confirm.mockResolvedValue(true);
});

describe("native policy view", () => {
  it("does not fetch at init — the initial load is lazy (B2)", async () => {
    initNativePolicyUI();
    await flush();
    expect(mocks.apiGet).not.toHaveBeenCalled();
  });

  it("renders rules grouped by scope with remove buttons only on writable scopes", async () => {
    initAndLoad();
    await flush();

    const rows = policyRows();
    expect(rows).toHaveLength(3);
    const kiroRow = rows.find(
      (r) => r.querySelector(".native-rule-cap")?.textContent === "fs_write",
    );
    expect(kiroRow?.querySelector(".native-rule-remove")).toBeNull();
    const userRow = rows.find((r) => r.querySelector(".native-rule-cap")?.textContent === "shell");
    expect(userRow?.querySelector(".native-rule-remove")).not.toBeNull();
    expect(byId<HTMLSelectElement>("native-rule-capability").options.length).toBe(4);
  });

  it("populates the effect selector defaulting to Ask (conservative)", () => {
    initNativePolicyUI();
    expect(byId<HTMLSelectElement>("native-rule-effect").value).toBe("ask");
  });

  // A read the server refused while no engine was ready is asked again when the engine is back.
  for (const [what, refused] of [
    ["kiro-cli did not answer", { ...sampleView, available: false }],
    ["whose read failed", null],
  ] as const) {
    it(`re-reads a policy ${what} when the engine is back, and not one that answered`, async () => {
      mocks.apiGet.mockResolvedValue(refused);
      initAndLoad();
      await flush();
      mocks.apiGet.mockClear();
      mocks.apiGet.mockResolvedValue(sampleView);

      mocks.busHandlers.get("engine:ready")?.();
      await flush();
      expect(mocks.apiGet).toHaveBeenCalledWith("/api/permissions", expect.anything());
      expect(policyRows()).toHaveLength(3);

      mocks.apiGet.mockClear();
      mocks.busHandlers.get("engine:ready")?.();
      await flush();
      expect(mocks.apiGet, "a policy that answered").not.toHaveBeenCalled();
    });
  }

  it("refetches when a permissions_changed SSE arrives", async () => {
    initAndLoad();
    await flush();
    expect(mocks.apiGet).toHaveBeenCalledTimes(1);
    mocks.sseHandlers.get("permissions_changed")?.("");
    await flush();
    expect(mocks.apiGet).toHaveBeenCalledTimes(2);
  });
});

describe("native policy editor", () => {
  it("adds a rule with the selected scope/capability/effect and splits comma globs", async () => {
    initAndLoad();
    await flush();

    byId<HTMLSelectElement>("native-rule-capability").value = "fs_write";
    byId<HTMLInputElement>("native-rule-match").value = "src/** , dist/**";
    byId<HTMLInputElement>("native-rule-exclude").value = "**/secrets/** , **/.git/**";
    byId<HTMLButtonElement>("native-rule-add").click();
    await flush();

    expect(mocks.editDispatch).toHaveBeenCalledTimes(1);
    const args = mocks.editDispatch.mock.calls[0]?.[0];
    expect(args).toMatchObject({
      op: "add",
      scope: "workspace",
      capability: "fs_write",
      effect: "ask",
      match: ["src/**", "dist/**"],
      exclude: ["**/secrets/**", "**/.git/**"],
    });
    expect(byId<HTMLInputElement>("native-rule-match").value).toBe("");
    expect(byId<HTMLInputElement>("native-rule-exclude").value).toBe("");
    expect(document.activeElement).toBe(byId<HTMLInputElement>("native-rule-match"));
    expect(mocks.apiGet).toHaveBeenCalledTimes(2);
  });

  it("renders an effect select on writable rows and a static badge on read-only rows", async () => {
    initAndLoad();
    await flush();

    const rows = policyRows();
    const kiroRow = rows.find(
      (r) => r.querySelector(".native-rule-cap")?.textContent === "fs_write",
    );
    expect(kiroRow?.querySelector("select.native-rule-effect")).toBeNull();
    expect(kiroRow?.querySelector("span.native-rule-effect")?.textContent).toBe("deny");

    const userRow = rows.find((r) => r.querySelector(".native-rule-cap")?.textContent === "shell");
    const sel = userRow?.querySelector<HTMLSelectElement>("select.native-rule-effect");
    expect(sel).not.toBeNull();
    expect(sel?.value).toBe("allow");
  });

  it("dispatches op=update on a narrowing effect change (no confirm)", async () => {
    initAndLoad();
    await flush();

    const row = policyRows().find(
      (r) => r.querySelector(".native-rule-cap")?.textContent === "fs_read",
    );
    const sel = row?.querySelector<HTMLSelectElement>("select.native-rule-effect");
    expect(sel).not.toBeNull();
    sel!.value = "deny";
    sel!.dispatchEvent(new Event("change"));
    await flush();

    expect(mocks.confirm).not.toHaveBeenCalled();
    expect(mocks.editDispatch).toHaveBeenCalledTimes(1);
    expect(mocks.editDispatch.mock.calls[0]?.[0]).toMatchObject({
      op: "update",
      scope: "workspace",
      capability: "fs_read",
      effect: "ask",
      new_effect: "deny",
      match: ["src/**"],
      confirm: false,
    });
    expect(mocks.apiGet).toHaveBeenCalledTimes(2);
  });

  it("requires confirmation for a widening effect change and reverts on cancel", async () => {
    mocks.confirm.mockResolvedValue(false);
    initAndLoad();
    await flush();

    const row = policyRows().find(
      (r) => r.querySelector(".native-rule-cap")?.textContent === "fs_read",
    );
    const sel = row?.querySelector<HTMLSelectElement>("select.native-rule-effect");
    sel!.value = "allow";
    sel!.dispatchEvent(new Event("change"));
    await flush();

    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    expect(mocks.editDispatch).not.toHaveBeenCalled();
    expect(sel!.value).toBe("ask");

    mocks.confirm.mockResolvedValue(true);
    sel!.value = "allow";
    sel!.dispatchEvent(new Event("change"));
    await flush();
    expect(mocks.editDispatch).toHaveBeenCalledTimes(1);
    expect(mocks.editDispatch.mock.calls[0]?.[0]).toMatchObject({
      op: "update",
      new_effect: "allow",
      confirm: true,
    });
  });

  it("pre-flights a shell explain without a resource instead of dispatching", async () => {
    initAndLoad();
    await flush();

    const capSel = byId<HTMLSelectElement>("native-explain-capability");
    const opt = document.createElement("option");
    opt.value = "shell";
    opt.textContent = "shell";
    capSel.appendChild(opt);
    capSel.value = "shell";
    byId<HTMLInputElement>("native-explain-resource").value = "   ";
    byId<HTMLButtonElement>("native-explain-run").click();
    await flush();

    expect(mocks.explainDispatch).not.toHaveBeenCalled();
    expect(byId("native-explain-result").textContent).toBe("");
    expect(byId("native-explain-error").textContent).toContain("Enter a command");
    expect(byId("native-explain-resource").getAttribute("aria-invalid")).toBe("true");
  });

  it("removes a non-deny rule directly (no confirm)", async () => {
    initAndLoad();
    await flush();
    const userRow = policyRows().find(
      (r) => r.querySelector(".native-rule-cap")?.textContent === "shell",
    );
    userRow?.querySelector<HTMLButtonElement>(".native-rule-remove")?.click();
    await flush();

    expect(mocks.confirm).not.toHaveBeenCalled();
    expect(mocks.editDispatch).toHaveBeenCalledTimes(1);
    expect(mocks.editDispatch.mock.calls[0]?.[0]).toMatchObject({
      op: "remove",
      scope: "user",
      capability: "shell",
      effect: "allow",
      confirm: false,
    });
  });

  it("requires confirmation to remove a deny rule and sends confirm=true", async () => {
    mocks.apiGet.mockResolvedValue({
      ...sampleView,
      rules: [
        {
          capability: "shell",
          effect: "deny",
          match: ["sudo *"],
          scope: "workspace",
          source: "/w/permissions.yaml",
          editable: true,
        },
      ],
    });
    initAndLoad();
    await flush();

    const denyRow = policyRows()[0];
    denyRow?.querySelector<HTMLButtonElement>(".native-rule-remove")?.click();
    await flush();

    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    expect(mocks.editDispatch).toHaveBeenCalledTimes(1);
    expect(mocks.editDispatch.mock.calls[0]?.[0]).toMatchObject({
      op: "remove",
      effect: "deny",
      confirm: true,
    });
  });

  it("does NOT remove a deny rule when confirmation is cancelled", async () => {
    mocks.confirm.mockResolvedValue(false);
    mocks.apiGet.mockResolvedValue({
      ...sampleView,
      rules: [
        {
          capability: "shell",
          effect: "deny",
          match: ["sudo *"],
          scope: "workspace",
          source: "/w/permissions.yaml",
          editable: true,
        },
      ],
    });
    initAndLoad();
    await flush();

    policyRows()[0]?.querySelector<HTMLButtonElement>(".native-rule-remove")?.click();
    await flush();

    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    expect(mocks.editDispatch).not.toHaveBeenCalled();
  });

  it("runs an explain simulation and renders the effect", async () => {
    mocks.explainDispatch.mockResolvedValue({
      effect: "ask",
      scope: "workspace",
      source: "/w/permissions.yaml",
    });
    initAndLoad();
    await flush();

    byId<HTMLSelectElement>("native-explain-capability").value = "fs_write";
    byId<HTMLInputElement>("native-explain-resource").value = "/etc/hosts";
    byId<HTMLButtonElement>("native-explain-run").click();
    await flush();

    expect(mocks.explainDispatch).toHaveBeenCalledWith({
      capability: "fs_write",
      resource: "/etc/hosts",
    });
    expect(byId("native-explain-result").textContent).toContain("Effect: ask");
  });
});

describe("native policy rows the writer cannot edit", () => {
  const userFile = "/home/u/.kiro/settings/permissions.yaml";

  function loadRules(rules: PolicyView["rules"]): void {
    mocks.apiGet.mockResolvedValue({ ...sampleView, rules });
    initAndLoad();
  }

  it("renders a preset the user file references as a badge with no remove button", async () => {
    loadRules([
      {
        capability: "shell",
        effect: "allow",
        scope: "user",
        source: `preset:dev-shell in ${userFile}`,
        editable: false,
      },
    ]);
    await flush();

    const row = policyRows()[0];
    expect(row?.querySelector("select.native-rule-effect")).toBeNull();
    expect(row?.querySelector("span.native-rule-effect")?.textContent).toBe("allow");
    expect(row?.querySelector(".native-rule-remove")).toBeNull();
  });

  it("names the preset in the row's source and shortens only the path", async () => {
    loadRules([
      {
        capability: "shell",
        effect: "allow",
        scope: "user",
        source: `preset:dev-shell in ${userFile}`,
        editable: false,
      },
    ]);
    await flush();

    expect(policyRows()[0]?.querySelector(".native-rule-src")?.textContent).toBe(
      "preset:dev-shell in .../settings/permissions.yaml",
    );
  });

  it("renders an evaluate rule as evaluate, never as allow", async () => {
    loadRules([
      { capability: "all", effect: "evaluate", scope: "user", source: userFile, editable: false },
    ]);
    await flush();

    const row = policyRows()[0];
    expect(row?.querySelector("select.native-rule-effect")).toBeNull();
    expect(row?.querySelector("span.native-rule-effect")?.textContent).toBe("evaluate");
  });
});

describe("native policy status line", () => {
  it("shows the rules kiro-cli did not load", async () => {
    mocks.apiGet.mockResolvedValue({
      ...sampleView,
      load_errors: [{ scope: "user", message: "unknown capability 'teleport'" }],
    });
    initAndLoad();
    await flush();

    const status = byId("native-policy-status");
    expect(status.textContent).toContain("user: unknown capability 'teleport'");
    expect(status.classList.contains("hidden")).toBe(false);
    expect(status.classList.contains("native-policy-status-error")).toBe(false);
  });

  it("styles the status as an error when a load error is fatal", async () => {
    mocks.apiGet.mockResolvedValue({
      ...sampleView,
      load_errors: [{ scope: "workspace", message: "parse error", fatal: true }],
    });
    initAndLoad();
    await flush();

    expect(byId("native-policy-status").classList.contains("native-policy-status-error")).toBe(
      true,
    );
  });

  it("names kiro-cli, not a chat session, when the live policy is unavailable", async () => {
    mocks.apiGet.mockResolvedValue({ ...sampleView, available: false });
    initAndLoad();
    await flush();

    const text = byId("native-policy-status").textContent;
    expect(text).toContain("kiro-cli did not answer");
    expect(text).not.toContain("session");
  });

  it("names kiro-cli when an explain gets no answer", async () => {
    mocks.explainDispatch.mockResolvedValue(null);
    initAndLoad();
    await flush();

    byId<HTMLSelectElement>("native-explain-capability").value = "fs_write";
    byId<HTMLButtonElement>("native-explain-run").click();
    await flush();

    expect(byId("native-explain-error").textContent).toBe(
      "Could not evaluate: kiro-cli did not answer.",
    );
  });
});

describe("native policy permissions_changed refetch", () => {
  it("fetches nothing before the tab's first activation", async () => {
    initNativePolicyUI();
    mocks.sseHandlers.get("permissions_changed")?.("");
    await flush();

    expect(mocks.apiGet).not.toHaveBeenCalled();
  });

  it("coalesces a burst of frames during a pending fetch into one refetch", async () => {
    let release: (v: PolicyView) => void = () => undefined;
    mocks.apiGet.mockReturnValueOnce(
      new Promise<PolicyView>((resolve) => {
        release = resolve;
      }),
    );
    initAndLoad();
    for (let i = 0; i < 20; i++) {
      mocks.sseHandlers.get("permissions_changed")?.("");
    }
    await flush();
    expect(mocks.apiGet).toHaveBeenCalledTimes(1);

    release(sampleView);
    await flush();
    await flush();
    expect(mocks.apiGet).toHaveBeenCalledTimes(2);
  });
});
