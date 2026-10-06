// The run-without-asking list has to be VISIBLE in the edit form.
//
// `auto_approve` travels the whole marotte pipeline — the store validates and
// round-trips it, the paste-a-README importer carries it in, and it is rendered
// into KAS's own config as `autoApprove` — while the form showed nothing. So a
// publisher's pasted block could put a standing prompt bypass on a server with
// no surface saying so. (The installed engine does not read `autoApprove` yet,
// measured on 2.21.4, so the mark asks today; the panel hint says so and the
// list is what a build that starts reading it would honour.) Every case below
// drives the real modal over the real markup.

import { describe, it, expect, vi, beforeEach } from "vitest";
import indexHtml from "../static/index.html?raw";
import type { Server } from "./mcp-state.js";
import { mcpState } from "./mcp-state.js";

// Node's absence would put an install banner in the npm panel; the real probe
// is a network call.
vi.mock("./actions/tools.js", () => ({
  getToolsStatus: { dispatch: async () => ({ npx: true }) },
}));
vi.mock("./tools.js", () => ({ installToolAndWait: async () => ({ ok: true }) }));

const saved = vi.hoisted(() => ({
  calls: [] as { id: string; body: Partial<Server> }[],
}));

// The auto-approve POSTURE, writable. The real value is a read-only signal filled
// by `GET /api/mcp`, so a case puts the panel on a rung that suspends by moving
// this rather than by driving a fetch — the panel is the subject, not the wire.
const posture = vi.hoisted(() => ({ honoured: true }));

const opened = vi.hoisted(() => ({ calls: [] as [string, string][] }));

// One export replaced in each: the posture the panel reads, and the navigation the
// pointer performs. Everything else is real, so the sections, the chips and the
// save path are the shipped ones.
vi.mock(import("./mcp-state.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    autoApproveHonoured: {
      peek: () => posture.honoured,
      get value(): boolean {
        return posture.honoured;
      },
    },
  } as unknown as typeof actual;
});

// Replaced WHOLE, matching the six sibling suites that mock this module: its real
// body reads `location.search` at module load and pulls in the tab projection, so
// `importOriginal` cannot evaluate it here. `openSetting` is the only name this
// file's graph reaches.
vi.mock("./settings-highlight.js", () => ({
  openSetting: (tab: string, controlID: string): void => {
    opened.calls.push([tab, controlID]);
  },
}));

// One export replaced, the rest real: `submitServer`'s dispatch is the wire, and
// the body it hands over is what decides whether a grant survives a save.
vi.mock(import("./actions/mcp.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    saveServer: {
      dispatch: (args: { id: string; body: Partial<Server> }) => {
        saved.calls.push(args);
        return { outcome: Promise.resolve({ status: "success", value: {} }) };
      },
    },
  } as unknown as typeof actual;
});

function mountModal(): void {
  const start = indexHtml.indexOf('<div id="mcp-modal"');
  const end = indexHtml.indexOf("<!-- Git output popup -->", start);
  expect(start, "#mcp-modal not found").toBeGreaterThan(-1);
  expect(end, "the marker after the MCP modal not found").toBeGreaterThan(start);
  const host = document.createElement("div");
  host.innerHTML = indexHtml.slice(start, end);
  document.body.replaceChildren(...Array.from(host.childNodes));
}

function record(over: Partial<Server>): Server {
  return {
    id: "srv-1",
    name: "example",
    transport: "stdio",
    enabled: true,
    created_at: 1,
    updated_at: 2,
    command: "npx",
    args: ["-y", "@modelcontextprotocol/server-github"],
    ...over,
  };
}

async function openEdit(server: Server): Promise<void> {
  const { setEditing, initModal, editModeFor } = await import("./mcp-panels.js");
  setEditing({ id: server.id });
  initModal({ mode: editModeFor(server), server });
}

function chipLabels(id: string): string[] {
  return Array.from(document.querySelectorAll(`#${id} .chip`)).map((c) =>
    (c.querySelector("code")?.textContent ?? "").trim(),
  );
}

function removeChip(id: string, label: string): void {
  const chip = Array.from(document.querySelectorAll<HTMLElement>(`#${id} .chip`)).find(
    (c) => (c.querySelector("code")?.textContent ?? "").trim() === label,
  );
  expect(chip, `no chip labelled ${label} in #${id}`).toBeDefined();
  chip?.querySelector<HTMLButtonElement>("button")?.click();
}

async function save(server: Server): Promise<Partial<Server>> {
  const { editModeFor } = await import("./mcp-panels.js");
  const mode = editModeFor(server);
  document.getElementById(`mcp-${mode}-save`)?.click();
  await vi.waitFor(() => {
    expect(saved.calls).toHaveLength(1);
  });
  return saved.calls[0]?.body ?? {};
}

beforeEach(() => {
  saved.calls.length = 0;
  opened.calls.length = 0;
  // Back to the honouring rung. Browser Mode isolates per FILE, so a case that
  // suspends would otherwise decide every case declared after it.
  posture.honoured = true;
  mountModal();
  // A connected server's advertised tools are what the suggestion row offers.
  mcpState.setDiscovery("example", [], [], []);
});

describe("the run-without-asking section", () => {
  it("shows the grant a pasted block put on the record", async () => {
    await openEdit(record({ auto_approve: ["search_repos"] }));

    expect(document.getElementById("mcp-auto-approve")?.classList.contains("hidden")).toBe(false);
    expect(chipLabels("mcp-auto-approve-chips")).toEqual(["search_repos"]);
  });

  it("stays hidden while adding a server, which has no record to grant on", async () => {
    const { setEditing, initModal } = await import("./mcp-panels.js");
    setEditing({ id: "" });
    initModal({ mode: "npm", server: null });

    expect(document.getElementById("mcp-auto-approve")?.classList.contains("hidden")).toBe(true);
  });

  it("sends the list on save so the grant survives an edit", async () => {
    const server = record({ auto_approve: ["search_repos"] });
    await openEdit(server);
    expect(await save(server)).toMatchObject({ auto_approve: ["search_repos"] });
  });

  it("sends an EMPTY list when the last grant is removed, which is what clears it", async () => {
    // The store reads an omitted list as unchanged, so a save that withholds
    // the field silently re-grants the bypass the user just revoked.
    const server = record({ auto_approve: ["search_repos"] });
    await openEdit(server);
    removeChip("mcp-auto-approve-chips", "search_repos");
    expect(chipLabels("mcp-auto-approve-chips")).toEqual([]);

    const body = await save(server);
    expect(body.auto_approve).toEqual([]);
  });

  it("adds a typed name and takes it out of the suggestion row", async () => {
    mcpState.setDiscovery("example", ["search_repos", "create_issue"], [], []);
    const server = record({});
    await openEdit(server);

    const input = document.getElementById("mcp-auto-approve-input") as HTMLInputElement;
    input.value = "search_repos";
    document.getElementById("mcp-auto-approve-add")?.click();

    expect(chipLabels("mcp-auto-approve-chips")).toEqual(["search_repos"]);
    const suggested = Array.from(
      document.querySelectorAll("#mcp-auto-approve .mcp-tool-suggestions button"),
    ).map((b) => b.textContent);
    expect(suggested).toEqual(["create_issue"]);
    expect(await save(server)).toMatchObject({ auto_approve: ["search_repos"] });
  });

  it("leaves it out of the raw JSON box, because the chips own it now", async () => {
    await openEdit(
      record({ command: "docker", args: ["run", "--rm", "-i", "x"], auto_approve: ["a"] }),
    );

    const box = document.getElementById("mcp-raw-input") as HTMLTextAreaElement;
    expect(Object.keys(JSON.parse(box.value) as Record<string, unknown>)).not.toContain(
      "auto_approve",
    );
  });
});

describe("the two tool lists", () => {
  it("edit their own field, so one grant cannot answer for the other", async () => {
    // Both sections are one control over one vocabulary, so a config pointed at
    // the wrong field is the failure this pins: removing a block must not
    // revoke a bypass, and neither list may be seeded from the other.
    const server = record({ auto_approve: ["search_repos"], disabled_tools: ["delete_repo"] });
    await openEdit(server);

    expect(chipLabels("mcp-auto-approve-chips")).toEqual(["search_repos"]);
    expect(chipLabels("mcp-disabled-chips")).toEqual(["delete_repo"]);

    removeChip("mcp-disabled-chips", "delete_repo");
    expect(chipLabels("mcp-auto-approve-chips")).toEqual(["search_repos"]);

    expect(await save(server)).toMatchObject({
      auto_approve: ["search_repos"],
      disabled_tools: [],
    });
  });

  it("offers each list its own suggestion row over the same tool names", async () => {
    mcpState.setDiscovery("example", ["search_repos", "create_issue"], [], []);
    await openEdit(record({ auto_approve: ["search_repos"] }));

    const offered = (sectionID: string): string[] =>
      Array.from(document.querySelectorAll(`#${sectionID} .mcp-tool-suggestions button`)).map(
        (b) => b.textContent ?? "",
      );
    // The granted tool leaves its OWN row and stays in the deny row: the two
    // sections filter against different lists.
    expect(offered("mcp-auto-approve")).toEqual(["create_issue"]);
    expect(offered("mcp-disabled-tools")).toEqual(["search_repos", "create_issue"]);
  });
});

describe("the suspension the security profile can put on that list", () => {
  it("names the rungs where the list is suspended and says nothing about annotations", () => {
    // The panel's copy, pinned on the surface a reader actually meets. It states
    // the exception as the COMPLEMENT — three rungs honour the list, two suspend
    // it, and naming the two cannot go stale when a permissive rung is added. The
    // annotations half is a REFUSAL: marotte does not read an MCP tool's own
    // annotations as a permission source, so the panel must not imply it does.
    const hint = document.querySelector("#mcp-auto-approve .section-hint")?.textContent ?? "";
    expect(hint).toContain("suspended on the Guarded and Read-only");
    expect(hint.toLowerCase()).not.toContain("annotation");
  });

  it("keeps the names VISIBLE and marks them not-in-force", async () => {
    // The whole defect being fixed is a grant nobody could see, so a suspension
    // that HID the list would reproduce it one state over. The chips stay; the
    // section carries the mark its stylesheet strikes them through on.
    posture.honoured = false;
    await openEdit(record({ auto_approve: ["search_repos"] }));

    expect(chipLabels("mcp-auto-approve-chips")).toEqual(["search_repos"]);
    expect(document.getElementById("mcp-auto-approve")?.hasAttribute("data-suspended")).toBe(true);
    const notice = document.getElementById("mcp-auto-approve-suspended");
    expect(notice?.classList.contains("hidden")).toBe(false);
    expect(notice?.textContent).toContain("still asks");
  });

  it("says nothing on a profile that honours the list", async () => {
    posture.honoured = true;
    await openEdit(record({ auto_approve: ["search_repos"] }));

    expect(document.getElementById("mcp-auto-approve")?.hasAttribute("data-suspended")).toBe(false);
    expect(
      document.getElementById("mcp-auto-approve-suspended")?.classList.contains("hidden"),
    ).toBe(true);
  });

  it("drops the mark when the same modal is reopened on a honouring profile", async () => {
    // The attribute and the notice are written on every open, not only when the
    // posture is false — a set-only writer leaves the previous open's suspension
    // standing over a list that is now in force, which is the lie in the other
    // direction.
    posture.honoured = false;
    await openEdit(record({ auto_approve: ["search_repos"] }));
    expect(document.getElementById("mcp-auto-approve")?.hasAttribute("data-suspended")).toBe(true);

    posture.honoured = true;
    await openEdit(record({ auto_approve: ["search_repos"] }));
    expect(document.getElementById("mcp-auto-approve")?.hasAttribute("data-suspended")).toBe(false);
  });

  it("NAVIGATES to the profile picker and offers no way to change it here", async () => {
    // The constraint permission.ts's buildPolicyPointer established: a surface
    // that benefits from a looser profile must never itself be a path that
    // loosens one. So the notice's only control is the link, and the link opens
    // Settings rather than writing anything.
    posture.honoured = false;
    await openEdit(record({ auto_approve: ["search_repos"] }));

    const notice = document.getElementById("mcp-auto-approve-suspended");
    // Precondition, not decoration: the notice is in the markup either way, so
    // without this the case passes over a suspension that never rendered and
    // stops describing a control a reader can reach.
    expect(notice?.classList.contains("hidden")).toBe(false);
    const buttons = Array.from(notice?.querySelectorAll("button") ?? []);
    expect(buttons).toHaveLength(1);

    buttons[0]?.click();
    expect(opened.calls).toEqual([["permissions", "security-profile-list"]]);
  });

  it("keeps the list EDITABLE while suspended, because only the effect is off", async () => {
    // The record is the user's intent and it applies again on a profile that
    // honours it, so the adder and the remove control stay live. Disabling them
    // would make a suspension look like a deletion.
    posture.honoured = false;
    const server = record({ auto_approve: ["search_repos"] });
    await openEdit(server);

    // Precondition: the claim is about a SUSPENDED list, so without this the case
    // passes over a section that was never suspended and asserts nothing.
    expect(document.getElementById("mcp-auto-approve")?.hasAttribute("data-suspended")).toBe(true);

    const input = document.getElementById("mcp-auto-approve-input") as HTMLInputElement;
    expect(input.disabled).toBe(false);
    input.value = "create_issue";
    document.getElementById("mcp-auto-approve-add")?.click();
    expect(chipLabels("mcp-auto-approve-chips")).toEqual(["search_repos", "create_issue"]);

    expect(await save(server)).toMatchObject({
      auto_approve: ["search_repos", "create_issue"],
    });
  });
});
