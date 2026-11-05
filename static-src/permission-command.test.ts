import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { setupWorker } from "msw/browser";
import type * as SettingsHighlight from "./settings-highlight.js";
import type * as Navigate from "./navigate.js";
import type { EntryToolCall, PermissionNeededPayload } from "./types.js";
import { makeSession, makeToolCall, makeTurn } from "./__test-helpers__/model.js";

vi.mock("./settings-highlight.js", async (importOriginal) => ({
  ...(await importOriginal<typeof SettingsHighlight>()),
  openSetting: vi.fn(),
}));
vi.mock("./navigate.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Navigate>()),
  openChange: vi.fn(),
}));

import { buildPermissionCard } from "./permission.js";
import { setSessions } from "./store.js";

function ask(over: Partial<PermissionNeededPayload>): PermissionNeededPayload {
  return {
    request_id: 1,
    title: "Run Command",
    kind: "execute",
    options: [{ option_id: "a", name: "Allow", kind: "allow_once" }],
    ...over,
  };
}

/** Seeds the store with one tool call `tc-1` whose input is `input`, in chat `c-1`. */
function seedToolInput(input: unknown, over: Partial<EntryToolCall> = {}): void {
  setSessions([
    makeSession({
      turns: [
        makeTurn({
          body: [
            {
              kind: "tool_call",
              id: "tc-1",
              payload: makeToolCall({ id: "tc-1", input, ...over }),
            },
          ],
        }),
      ],
    }),
  ]);
}

const worker = setupWorker();
/** Paths the card asked the server for. */
let requested: string[] = [];

beforeAll(async () => {
  await worker.start({ quiet: true });
});

afterAll(() => {
  worker.stop();
});

afterEach(() => {
  worker.resetHandlers();
  requested = [];
  setSessions([]);
});

/** Answer the bulk read of `tc-1` in chat `c-1` with `input`, held until `release` is called. */
function serveWholeInput(input: unknown): { release: () => void } {
  const gate = Promise.withResolvers<undefined>();
  worker.use(
    http.get("/api/*", async ({ request }) => {
      requested.push(new URL(request.url).pathname);
      await gate.promise;
      return HttpResponse.json({ id: "tc-1", input });
    }),
  );
  return { release: () => gate.resolve(undefined) };
}

const GAP =
  "Part of this tool's input is too large to show. Deny it unless you can vouch for all of it.";

describe("a command ask's card", () => {
  it("shows a long command whole, its suffix included", () => {
    const text = "echo " + "x".repeat(2000) + "; rm -rf ~";
    const card = buildPermissionCard("c-1", ask({ title: text, command: { text } }), vi.fn());

    const box = card.querySelector(".approval-input");
    expect(box?.textContent).toBe(text);
    expect(card.textContent).not.toContain("more characters");
  });

  it("puts what a long command leaves out between its head and tail", () => {
    const card = buildPermissionCard(
      "c-1",
      ask({ command: { text: "head", tail: "; rm -rf ~", omitted_bytes: 12345 } }),
      vi.fn(),
    );

    const box = card.querySelector(".approval-input");
    const parts = [...(box?.childNodes ?? [])].map((n) => n.textContent);
    expect(parts).toEqual([
      "head",
      `… ${(12345).toLocaleString()} bytes not shown …`,
      "; rm -rf ~",
    ]);
    expect(box?.querySelector(".approval-omitted")?.textContent).toBe(parts[1]);
    expect(card.querySelector(".approval-hint")?.textContent).toBe(
      "The middle of this command is too long to show. Deny it unless you can vouch for all of it.",
    );
  });

  it("says why a command the connect snapshot dropped is not shown", () => {
    const card = buildPermissionCard(
      "c-1",
      ask({ command: { text: "", omitted_bytes: 8292 } }),
      vi.fn(),
    );

    expect(card.querySelector(".approval-input")?.textContent).toBe(
      `… ${(8292).toLocaleString()} bytes not shown …`,
    );
    expect(card.querySelector(".approval-hint")?.textContent).toBe(
      "This command is not shown because too many approvals are waiting. Deny it unless you can vouch for it.",
    );
  });

  it("shows no omission line for a command that arrived whole", () => {
    const card = buildPermissionCard("c-1", ask({ command: { text: "ls" } }), vi.fn());

    expect(card.querySelector(".approval-omitted")).toBeNull();
    expect(card.querySelector(".approval-hint")).toBeNull();
  });

  it("states a command once", () => {
    const text = "git push --force origin main";
    const card = buildPermissionCard("c-1", ask({ title: text, command: { text } }), vi.fn());

    expect(card.querySelector(".approval-body > strong")?.textContent).toBe("Run this command");
    expect(card.textContent.split(text)).toHaveLength(2);
  });

  it("names the directory a command runs in", () => {
    seedToolInput({ command: "rm -rf *", cwd: "/srv/app" });
    const card = buildPermissionCard(
      "c-1",
      ask({ tool_call_id: "tc-1", command: { text: "rm -rf *" } }),
      vi.fn(),
    );

    expect(card.querySelector(".approval-cwd")?.textContent).toBe("in /srv/app");
    expect(card.querySelector(".approval-input")?.textContent).toBe("rm -rf *");
  });

  it("shows another tool's input whole", () => {
    const input = { path: "a.md", text: "y".repeat(3000) };
    seedToolInput(input);
    const card = buildPermissionCard(
      "c-1",
      ask({ title: "Write a.md", kind: "edit", tool_call_id: "tc-1" }),
      vi.fn(),
    );

    expect(card.querySelector(".approval-body > strong")?.textContent).toBe("Write a.md");
    expect(card.querySelector(".approval-input")?.textContent).toBe(JSON.stringify(input, null, 2));
    expect(card.querySelector(".approval-cwd")).toBeNull();
  });
});

// The ask carries no tool input, so the store's untreated copy is defused on the card itself.
describe("a deceptive tool input on the card", () => {
  it("shows a bidi override in an argument as a space", () => {
    seedToolInput({ path: "notes\u202ehs.txt\u202c" });
    const card = buildPermissionCard(
      "c-1",
      ask({ title: "Write notes", kind: "edit", tool_call_id: "tc-1" }),
      vi.fn(),
    );

    expect(card.querySelector(".approval-input")?.textContent).toBe(
      '{\n  "path": "notes hs.txt "\n}',
    );
  });

  it("keeps the line breaks of a raw string input and folds every other control", () => {
    seedToolInput("cat a\r\nrm\tb\u202ec\u2028d\u0085");
    const card = buildPermissionCard(
      "c-1",
      ask({ title: "Raw", kind: "other", tool_call_id: "tc-1" }),
      vi.fn(),
    );

    expect(card.querySelector(".approval-input")?.textContent).toBe("cat a\nrm b c d ");
  });

  it("defuses the directory a command runs in", () => {
    seedToolInput({ command: "rm -rf *", cwd: "/srv/\u202eppa" });
    const card = buildPermissionCard(
      "c-1",
      ask({ tool_call_id: "tc-1", command: { text: "rm -rf *" } }),
      vi.fn(),
    );

    expect(card.querySelector(".approval-cwd")?.textContent).toBe("in /srv/ ppa");
  });
});

describe("a tool input the store bounded", () => {
  function writeAsk(): PermissionNeededPayload {
    return ask({ title: "Write a.md", kind: "edit", tool_call_id: "tc-1" });
  }

  it("says part of the input is not shown when the record dropped a member", () => {
    serveWholeInput({}).release();
    seedToolInput({ path: "a.md" }, { truncated: { input_bytes: 20000 } });
    const card = buildPermissionCard("c-1", writeAsk(), vi.fn());

    expect(card.querySelector(".approval-input")?.textContent).toBe('{\n  "path": "a.md"\n}');
    expect(card.querySelector(".approval-input-gap")?.textContent).toBe(GAP);
    expect(requested).toEqual([]);
  });

  it("states no gap for an input that arrived whole", () => {
    seedToolInput({ path: "a.md" });
    const card = buildPermissionCard("c-1", writeAsk(), vi.fn());

    expect(card.querySelector(".approval-input-gap")).toBeNull();
  });

  it("states no gap beside a command, which the ask itself carries", () => {
    seedToolInput({ command: "ls", cwd: "/srv" }, { truncated: { input_bytes: 20000 } });
    const card = buildPermissionCard(
      "c-1",
      ask({ tool_call_id: "tc-1", command: { text: "ls" } }),
      vi.fn(),
    );

    expect(card.querySelector(".approval-input-gap")).toBeNull();
    expect(card.querySelector(".approval-cwd")?.textContent).toBe("in /srv");
  });

  it("replaces a preview's cut input with the whole one when it lands", async () => {
    const whole = { path: "a.md", content: "z".repeat(6000) };
    const server = serveWholeInput(whole);
    seedToolInput({ path: "a.md" }, { has_full: true });
    const card = buildPermissionCard("c-1", writeAsk(), vi.fn());
    const slot = card.querySelector(".approval-tool-input");

    expect(card.querySelector(".approval-input")?.textContent).toBe('{\n  "path": "a.md"\n}');
    expect(card.querySelector(".approval-input-gap")?.textContent).toBe(GAP);

    server.release();
    await vi.waitFor(() => {
      expect(card.querySelector(".approval-input")?.textContent).toBe(
        JSON.stringify(whole, null, 2),
      );
    });
    expect(card.querySelector(".approval-input-gap")).toBeNull();
    expect(card.querySelector(".approval-tool-input")).toBe(slot);
    expect(requested).toEqual(["/api/chats/c-1/tools/tc-1"]);
  });
});
