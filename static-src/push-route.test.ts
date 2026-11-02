// REQUIRED TEST 1: the worker's destination and the page's opener resolve to the SAME route.

import { vi, describe, it, expect, beforeEach } from "vitest";
import { buildPath, type Route } from "./route-path.js";
import { join } from "@cplieger/keyenc";
import { parsePushTarget, pushTargetRoute } from "./push-subject.js";
import { _resetRunAsksForTest, applyRunAsks, runAskPosition } from "./run-asks.js";
import type { RunAsk } from "./wire/types.gen.js";
import {
  registerNotificationOpener,
  _resetNotificationOpenerForTest,
} from "./notification-open.js";

vi.mock("./toast.js", async () => (await import("./__test-helpers__/toast-mock.js")).toastMock());

const { routePushMessage } = await import("./handlers/push-message.js");
const { info } = await import("./toast.js");

const WAIT = join("wf_42", join("wf_42", "review"), "sess-1");

interface Row {
  readonly name: string;
  readonly chatId: string;
  readonly subject: string;
  readonly route: Route;
  readonly url: string;
}

const rows: readonly Row[] = [
  {
    name: "a chat",
    chatId: "c-7f3a",
    subject: "",
    route: { kind: "chat", id: "c-7f3a" },
    url: "/chat/c-7f3a",
  },
  {
    name: "a pull request",
    chatId: "",
    subject: "pr:github:github.com:cplieger/marotte#42",
    route: { kind: "git", tab: "prs", pr: "github:github.com:cplieger/marotte#42" },
    // The identity rides as a `#pr=` fragment, so a cold click from the tray carries the pull
    // request into `openWindow` rather than only the tab. The `#` inside it percent-encodes to
    // `%23`, which is why the fragment is one encoded value.
    url: "/git/prs#pr=github%3Agithub.com%3Acplieger%2Fmarotte%2342",
  },
  {
    name: "a workflow run",
    chatId: "",
    subject: "run:wf_42",
    route: { kind: "run", id: "wf_42" },
    url: "/run/wf_42",
  },
  {
    name: "a step's question",
    chatId: "",
    subject: `runask:${WAIT}`,
    route: { kind: "run", id: "wf_42" },
    url: "/run/wf_42",
  },
  {
    name: "the workspace",
    chatId: "",
    subject: "",
    route: { kind: "chat", id: "" },
    url: "/",
  },
];

const opened = vi.fn<(route: Route) => void>();

describe("one subject, one destination", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    registerNotificationOpener(opened);
  });

  for (const row of rows) {
    it(`${row.name} resolves to one route for both halves`, () => {
      const expected = pushTargetRoute(
        parsePushTarget({ chatId: row.chatId, subject: row.subject }),
      );
      expect(expected).toEqual(row.route);

      // The WORKER's destination: what `openWindow` receives when no page is open.
      expect(buildPath(expected)).toBe(row.url);

      // The PAGE's destination, through the real seam.
      routePushMessage({
        type: "push",
        reason: "clicked",
        chatId: row.chatId,
        subject: row.subject,
        title: "Marotte",
        body: "",
      });
      expect(opened).toHaveBeenCalledTimes(1);
      expect(opened).toHaveBeenCalledWith(expected);
    });
  }
});

describe("an unregistered opener", () => {
  // Its own describe with the reset, because the opener is ONE module-level slot and Vitest links a
  // module once per file: the table above has already registered a spy, so without this the
  // unregistered state is unreachable.
  beforeEach(() => {
    _resetNotificationOpenerForTest();
  });

  it("throws rather than losing the navigation", () => {
    expect(() => {
      routePushMessage({
        type: "push",
        reason: "clicked",
        chatId: "c1",
        subject: "",
        title: "Marotte",
        body: "",
      });
    }).toThrow(/no opener registered/);
  });
});

// A banner can outlive its question (another device answered, the step moved on): the tap still lands
// on the run, and says the step no longer waits rather than leaving the reader to look for a card.
describe("a tap on a step question's banner", () => {
  const ask: RunAsk = {
    ask_id: "ask-1",
    node_id: "review",
    node_path: ["wf_42", "review"],
    step_session_id: "sess-1",
    agent_name: "",
    text: "Ship it?",
    text_source: "asked",
  };

  function tap(): void {
    routePushMessage({
      type: "push",
      reason: "clicked",
      chatId: "",
      subject: `runask:${WAIT}`,
      title: "Marotte",
      body: "",
    });
  }

  function hold(asks: RunAsk[], reachable = true): void {
    applyRunAsks({
      workflow_id: "wf_42",
      chat_id: "run:wf_42",
      epoch: "e1",
      seq: 1,
      reachable,
      asks,
    });
  }

  beforeEach(() => {
    vi.clearAllMocks();
    _resetRunAsksForTest();
    registerNotificationOpener(opened);
  });

  it("opens the run quietly while the step still waits", () => {
    expect(runAskPosition("wf_42", ask)).toBe(WAIT);
    hold([ask]);
    tap();
    expect(opened).toHaveBeenCalledWith({ kind: "run", id: "wf_42" });
    expect(info).not.toHaveBeenCalled();
  });

  it("says the step no longer waits once the run's snapshot drops it", () => {
    hold([{ ...ask, step_session_id: "sess-other", node_path: ["wf_42", "other"] }]);
    tap();
    expect(opened).toHaveBeenCalledWith({ kind: "run", id: "wf_42" });
    expect(info).toHaveBeenCalledWith("This step is no longer waiting for an answer.");
  });

  it("says so for a run this page holds no questions for", () => {
    tap();
    expect(info).toHaveBeenCalledWith("This step is no longer waiting for an answer.");
  });

  it("claims nothing for a run marotte could not read", () => {
    hold([], false);
    tap();
    expect(info).not.toHaveBeenCalled();
  });
});
