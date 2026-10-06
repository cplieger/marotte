// The Active-policy table's own surfaces in permissions-ui.ts: the per-row hints, the capability
// pickers, and the "Test a decision (why?)" box. The profile picker's tests live beside them in
// permissions-profile.test.ts.
import { describe, it, expect, beforeEach, vi } from "vitest";
import type { PolicyView, PolicyRule, PolicyExplainResult } from "./types.js";

type SSEFn = (chatID: string, payload?: unknown) => void;

const mocks = vi.hoisted(() => ({
  apiGet: vi.fn<(path: string, signal?: AbortSignal) => Promise<PolicyView | null>>(),
  editDispatch:
    vi.fn<(args: Record<string, unknown>) => Promise<{ ok?: boolean; error?: string } | null>>(),
  explainDispatch: vi.fn<(args: Record<string, unknown>) => Promise<PolicyExplainResult | null>>(),
  profileDispatch:
    vi.fn<(args: Record<string, unknown>) => Promise<{ ok?: boolean; error?: string } | null>>(),
  confirm: vi.fn<(msg: string, label?: string, variant?: string) => Promise<boolean>>(),
  sseHandlers: new Map<string, SSEFn>(),
}));

vi.mock("./api-client.js", () => ({ apiGet: mocks.apiGet }));
vi.mock("./persist.js", () => ({ patchSettings: vi.fn() }));
vi.mock("./confirm.js", () => ({ confirm: mocks.confirm }));
vi.mock("./bus.js", () => ({
  onSSE: (type: string, fn: SSEFn) => {
    mocks.sseHandlers.set(type, fn);
    return () => mocks.sseHandlers.delete(type);
  },
  // Present-but-inert so real-ESM linking succeeds: this graph reaches names no case here calls.
  apiGetTyped: vi.fn(),
}));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
  bindLoadingState: vi.fn(() => vi.fn()),
}));
vi.mock("./actions/permissions.js", () => ({
  editNativeRule: { name: "permissions.edit_native_rule", dispatch: mocks.editDispatch },
  explainPolicy: { name: "permissions.explain", dispatch: mocks.explainDispatch },
  setSecurityProfile: { name: "permissions.set_profile", dispatch: mocks.profileDispatch },
}));

import { initNativePolicyUI, loadNativePolicy } from "./permissions-ui.js";
import { byId } from "./dom.js";

const SOURCE = "/home/u/.kiro/settings/permissions.yaml";

function userRule(capability: string, over: Partial<PolicyRule> = {}): PolicyRule {
  return { capability, effect: "allow", scope: "user", source: SOURCE, ...over };
}

function view(rules: PolicyRule[], caps = ["fs_read", "fs_write", "shell"]): PolicyView {
  return {
    available: true,
    writable_scopes: ["user", "workspace"],
    capabilities: caps,
    // Custom, so the table is editable and its rows carry their controls.
    profiles: [
      { id: "guarded", presets: ["read-workspace"] },
      { id: "unrestricted", presets: ["allow-all"] },
      { id: "custom", presets: [] },
    ],
    profile: "custom",
    rules,
  };
}

async function flush(): Promise<void> {
  for (let i = 0; i < 8; i++) {
    await Promise.resolve();
  }
}

function tag<T extends HTMLElement>(name: string, id: string): T {
  const e = document.createElement(name) as T;
  e.id = id;
  return e;
}

/** init + the lazy first load, as wired in production. */
async function mount(v: PolicyView): Promise<void> {
  mocks.apiGet.mockResolvedValue(v);
  initNativePolicyUI();
  loadNativePolicy();
  await flush();
}

/** A second load, as a `permissions_changed` frame produces. */
async function reload(v: PolicyView): Promise<void> {
  mocks.apiGet.mockResolvedValue(v);
  loadNativePolicy();
  await flush();
}

/** The rendered row for one capability. Read out of the DOM, because what a reader gets is the
 *  rendered row rather than the builder's return. */
function rowFor(capability: string): HTMLElement {
  const rows = [...byId("native-policy-list").querySelectorAll<HTMLElement>(".native-rule")];
  const found = rows.find((r) => r.querySelector(".native-rule-cap")?.textContent === capability);
  if (!found) {
    throw new Error(`no rule row for ${capability}`);
  }
  return found;
}

function capabilityOptions(id: string): string[] {
  return [...byId<HTMLSelectElement>(id).options].map((o) => o.value);
}

async function runExplain(capability: string, resource = ""): Promise<string> {
  byId<HTMLSelectElement>("native-explain-capability").value = capability;
  byId<HTMLInputElement>("native-explain-resource").value = resource;
  byId<HTMLButtonElement>("native-explain-run").click();
  await flush();
  return byId("native-explain-result").textContent ?? "";
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.sseHandlers.clear();
  document.body.replaceChildren();

  const section = tag("div", "native-policy-section");
  section.appendChild(tag("p", "native-policy-status"));
  section.appendChild(tag("div", "native-policy-list"));
  section.appendChild(tag("p", "native-policy-empty-hint"));
  const adder = document.createElement("div");
  adder.setAttribute("data-rule-form", "add");
  adder.appendChild(tag<HTMLSelectElement>("select", "native-rule-capability"));
  adder.appendChild(tag<HTMLButtonElement>("button", "native-rule-add"));
  section.appendChild(adder);
  section.appendChild(tag<HTMLSelectElement>("select", "native-explain-capability"));
  section.appendChild(tag<HTMLInputElement>("input", "native-explain-resource"));
  section.appendChild(tag<HTMLButtonElement>("button", "native-explain-run"));
  section.appendChild(tag("p", "native-explain-result"));
  document.body.appendChild(section);

  document.body.appendChild(tag("div", "security-profile-list"));
  document.body.appendChild(tag("button", "security-profile-customize"));
  document.body.appendChild(tag("p", "security-profile-status"));

  mocks.editDispatch.mockResolvedValue({ ok: true });
  mocks.profileDispatch.mockResolvedValue({ ok: true });
  mocks.confirm.mockResolvedValue(true);
});

// A native `title` is the browser's own tooltip: it misses the styled treatment every other hover
// in the app uses, and it publishes no accessible description, so it reaches mouse users only.
describe("a rule row's hints", () => {
  it("carries the provenance path on the app's tooltip, not a native title", async () => {
    await mount(view([userRule("shell")]));
    const src = rowFor("shell").querySelector<HTMLElement>(".native-rule-src");

    expect(src?.getAttribute("data-tooltip")).toBe(SOURCE);
    expect(src?.hasAttribute("title")).toBe(false);
  });

  // `shortSource` elides the path on screen, so without a label the whole of it reaches nobody who
  // is not hovering.
  it("names the provenance path for a reader who cannot hover", async () => {
    await mount(view([userRule("shell")]));
    const src = rowFor("shell").querySelector<HTMLElement>(".native-rule-src");

    expect(src?.textContent).not.toContain("/home/u");
    expect(src?.getAttribute("aria-label")).toContain(SOURCE);
  });

  it("says nothing about a rule with no source", async () => {
    await mount(view([userRule("shell", { source: "" })]));
    const src = rowFor("shell").querySelector<HTMLElement>(".native-rule-src");

    expect(src?.hasAttribute("data-tooltip")).toBe(false);
    expect(src?.hasAttribute("aria-label")).toBe(false);
  });

  it("puts the remove and effect hints on the same channel", async () => {
    await mount(view([userRule("shell")]));
    const row = rowFor("shell");
    const rm = row.querySelector<HTMLButtonElement>(".native-rule-remove");
    const sel = row.querySelector<HTMLSelectElement>("select");

    expect(rm?.getAttribute("data-tooltip")).toBe("Remove rule");
    expect(rm?.hasAttribute("title")).toBe(false);
    expect(sel?.getAttribute("data-tooltip")).toBe("Change this rule's effect");
    expect(sel?.hasAttribute("title")).toBe(false);
  });
});

// The server's capability list is a UNION of marotte's suggested set and every capability the
// returned rules already use, so it exists in order to GROW: a cold no-bridge view names only what
// the two writable files hold, and the live view is wider.
describe("the capability pickers", () => {
  it("follows a capability set that grew", async () => {
    await mount(view([], ["fs_read", "shell"]));
    expect(capabilityOptions("native-rule-capability")).toEqual(["fs_read", "shell"]);

    await reload(view([], ["fs_read", "shell", "mcp"]));

    expect(capabilityOptions("native-rule-capability")).toEqual(["fs_read", "shell", "mcp"]);
    expect(capabilityOptions("native-explain-capability")).toEqual(["fs_read", "shell", "mcp"]);
  });

  it("keeps a selection the reader had made", async () => {
    await mount(view([], ["fs_read", "shell"]));
    const sel = byId<HTMLSelectElement>("native-rule-capability");
    sel.value = "shell";

    await reload(view([], ["fs_read", "shell", "mcp"]));

    expect(sel.value).toBe("shell");
  });

  // A frame that changes nothing about the set must not rebuild it: an open `<select>` closes and
  // the keyboard loses its place for no information at all. Object identity is the assertion,
  // because a rebuilt option is indistinguishable from a kept one by value.
  it("rebuilds nothing when the set did not move", async () => {
    await mount(view([], ["fs_read", "shell"]));
    const before = [...byId<HTMLSelectElement>("native-rule-capability").options];

    await reload(view([], ["fs_read", "shell"]));

    const after = [...byId<HTMLSelectElement>("native-rule-capability").options];
    expect(after).toHaveLength(before.length);
    for (const [i, opt] of before.entries()) {
      expect(after[i], `option ${String(i)} was replaced`).toBe(opt);
    }
  });

  // An empty answer says the view could not name a capability, not that there are none, so
  // replacing a populated picker with nothing would take the form's only input away on a transient
  // view.
  it("keeps what it has when the view names no capability", async () => {
    await mount(view([], ["fs_read", "shell"]));

    await reload(view([], []));

    expect(capabilityOptions("native-rule-capability")).toEqual(["fs_read", "shell"]);
  });
});

// The control is labelled "Test a decision (why?)". Effect plus scope answers which LAYER decided,
// and the kiro layer holds dozens of match globs, so the reader was left to find the deciding rule
// by eye. Both fields are decoded server-side already.
describe("Test a decision", () => {
  it("names the rule that decided, with its globs", async () => {
    await mount(view([]));
    mocks.explainDispatch.mockResolvedValue({
      capability: "fs_write",
      effect: "ask",
      is_explicit_ask: true,
      matched_rule: {
        capability: "fs_write",
        effect: "ask",
        match: ["**/.git/**"],
        exclude: ["**/tmp/**"],
      },
      scope: "kiro",
      source: "kiro-scope",
    });

    const out = await runExplain("fs_write", ".git/config");

    expect(out).toContain("rule: fs_write ask");
    expect(out).toContain("+**/.git/**");
    expect(out).toContain("\u2212**/tmp/**");
    expect(out).toContain("scope: kiro");
  });

  // The distinction `guardAllowRule` refuses an allow against: an ask a rule STATES, as opposed to
  // the implicit ask that means nothing matched.
  it("distinguishes an explicit ask from an implicit one", async () => {
    await mount(view([]));
    mocks.explainDispatch.mockResolvedValue({
      capability: "fs_write",
      effect: "ask",
      is_explicit_ask: true,
    });
    expect(await runExplain("fs_write", "src/x.go")).toContain("explicit ask");

    mocks.explainDispatch.mockResolvedValue({
      capability: "fs_write",
      effect: "ask",
      is_explicit_ask: false,
    });
    const implicit = await runExplain("fs_write", "src/x.go");
    expect(implicit).not.toContain("explicit");
    expect(implicit).not.toContain("rule:");
  });

  it("still answers when the reply names no rule", async () => {
    await mount(view([]));
    mocks.explainDispatch.mockResolvedValue({
      capability: "fs_read",
      effect: "allow",
      is_explicit_ask: false,
      scope: "session",
      source: "preset:read-workspace",
    });

    const out = await runExplain("fs_read", "src/x.go");

    expect(out).toContain("Effect: allow");
    expect(out).not.toContain("rule:");
  });
});
