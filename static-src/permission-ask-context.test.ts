import { describe, expect, it, vi } from "vitest";
import type * as SettingsHighlight from "./settings-highlight.js";
import type * as Navigate from "./navigate.js";
import type { PermissionNeededPayload } from "./types.js";

vi.mock("./settings-highlight.js", async (importOriginal) => ({
  ...(await importOriginal<typeof SettingsHighlight>()),
  openSetting: vi.fn(),
}));
vi.mock("./navigate.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Navigate>()),
  openChange: vi.fn(),
}));

import { buildPermissionCard } from "./permission.js";

function ask(over: Partial<PermissionNeededPayload>): PermissionNeededPayload {
  return {
    request_id: 1,
    title: "Write File",
    kind: "edit",
    options: [{ option_id: "a", name: "Allow", kind: "allow_once" }],
    ...over,
  } as PermissionNeededPayload;
}

function texts(card: HTMLElement, selector: string): string[] {
  return [...card.querySelectorAll(selector)].map((n) => n.textContent);
}

describe("a tool ask's context lines", () => {
  it("lists each path the call touches as its own row", () => {
    const card = buildPermissionCard(
      "chat-1",
      ask({ locations: ["src/a.ts", "docs/b.md"] }),
      vi.fn(),
    );

    expect(texts(card, ".approval-locations .dock-file-path")).toEqual(["src/a.ts", "docs/b.md"]);
  });

  it("names the workflow watch that raised the ask", () => {
    const card = buildPermissionCard(
      "chat-1",
      ask({ watch: { workflow_id: "wf_1", node_id: "poll" } }),
      vi.fn(),
    );

    expect(card.querySelector(".approval-watch")?.textContent).toBe("workflow watch: wf_1 / poll");
  });

  it("says a repeat ask for one tool call is another round, and counts it", () => {
    const card = buildPermissionCard("chat-1", ask({ consent_round: 2 }), vi.fn());

    expect(card.querySelector(".approval-round")?.textContent).toBe(
      "Another approval for this same tool call (2)",
    );
  });

  it("shows none of the three on a first ask with no paths and no watch", () => {
    const card = buildPermissionCard("chat-1", ask({ consent_round: 1 }), vi.fn());

    expect(card.querySelector(".approval-locations")).toBeNull();
    expect(card.querySelector(".approval-watch")).toBeNull();
    expect(card.querySelector(".approval-round")).toBeNull();
  });
});
