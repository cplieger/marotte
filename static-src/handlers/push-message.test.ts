// Turning a posted MESSAGE into a push target, `subject ?? ""` included, through the real seam
// with a spy opener; the destination is push-route.test.ts's.

import { vi, describe, it, expect, beforeEach } from "vitest";
import type { Route } from "../route-path.js";
import { registerNotificationOpener } from "../notification-open.js";
import { defaultUsage, setSessions } from "../store.js";

vi.mock("../toast.js", async () => (await import("../__test-helpers__/toast-mock.js")).toastMock());

const toast = await import("../toast.js");
const { initPushMessages, routePushMessage } = await import("./push-message.js");

const opened = vi.fn<(route: Route) => void>();
// Installed ONCE for the file: the listener it adds outlives every test, so a second
// install would fan one posted message out to two callbacks.
const onSubscriptionChanged = vi.fn();
initPushMessages(onSubscriptionChanged);

/** What the worker posts: a MessageEvent on the page's ServiceWorkerContainer. */
function postFromWorker(data: unknown): void {
  navigator.serviceWorker.dispatchEvent(new MessageEvent("message", { data }));
}

beforeEach(() => {
  vi.clearAllMocks();
  registerNotificationOpener(opened);
});

describe("initPushMessages", () => {
  it("re-derives the presence tag on a rotated subscription, and neither routes nor toasts", () => {
    postFromWorker({
      type: "push",
      reason: "subscription_changed",
      chatId: "",
      subject: "",
      title: "",
      body: "",
    });
    expect(onSubscriptionChanged).toHaveBeenCalledTimes(1);
    expect(opened).not.toHaveBeenCalled();
    expect(toast.notice).not.toHaveBeenCalled();
  });

  it("toasts an arrived chat push and leaves the presence tag alone", () => {
    postFromWorker({
      type: "push",
      reason: "arrived",
      chatId: "c1",
      subject: "",
      title: "Marotte",
      body: "Agent finished",
    });
    expect(toast.notice).toHaveBeenCalledWith("Agent finished", "info", undefined);
    expect(onSubscriptionChanged).not.toHaveBeenCalled();
    expect(opened).not.toHaveBeenCalled();
  });

  it("names the chat the push is about", () => {
    setSessions([
      {
        id: "c1",
        name: "Fix the parser",
        model: "",
        acp_session_id: "",
        current_mode_id: "",
        supervised_mode: false,
        usage: defaultUsage(),
        turns: new Map(),
        turn_order: [],
        turn_count: 0,
        has_more: false,
        thinking: false,
        working_label: "Thinking",
      },
    ]);
    postFromWorker({
      type: "push",
      reason: "arrived",
      chatId: "c1",
      subject: "",
      title: "Marotte",
      body: "Agent finished",
    });
    // The chat is retained and has no tab, so the notice offers Open onto it.
    expect(toast.notice).toHaveBeenCalledWith(
      "Fix the parser: Agent finished",
      "info",
      expect.objectContaining({ label: "Open" }),
    );
    setSessions([]);
  });

  it("names a chat the page no longer holds by the name the push carried", () => {
    setSessions([]);
    postFromWorker({
      type: "push",
      reason: "arrived",
      chatId: "c9",
      subject: "",
      chatName: "Fix the parser",
      title: "Marotte",
      body: "Agent finished",
    });
    expect(toast.notice).toHaveBeenCalledWith("Fix the parser: Agent finished", "info", undefined);
  });
});

describe("routePushMessage", () => {
  it("sends a PR subject to the PRs tab, focused on that pull request", () => {
    routePushMessage({
      type: "push",
      reason: "clicked",
      chatId: "",
      subject: "pr:github:github.com:cplieger/marotte#42",
      title: "Marotte",
      body: "cplieger/marotte #42 checks passed",
    });
    expect(opened).toHaveBeenCalledWith({
      kind: "git",
      tab: "prs",
      pr: "github:github.com:cplieger/marotte#42",
    });
  });

  it("sends a chat notification to its chat", () => {
    routePushMessage({
      type: "push",
      reason: "clicked",
      chatId: "c1",
      title: "Marotte",
      body: "Reviewing the poller",
    });
    expect(opened).toHaveBeenCalledWith({ kind: "chat", id: "c1" });
  });

  it("sends a workspace-global notification to the default chat", () => {
    routePushMessage({
      type: "push",
      reason: "clicked",
      chatId: "",
      title: "Marotte",
      body: "something happened",
    });
    expect(opened).toHaveBeenCalledWith({ kind: "chat", id: "" });
  });

  it("prefers the subject over an accidental chat id", () => {
    // Exactly one subject field is set on the wire, so this is a defensive
    // ordering rather than a live case — and the PR branch is the right winner: a
    // PR notification has no chat to open.
    routePushMessage({
      type: "push",
      reason: "clicked",
      chatId: "c1",
      subject: "pr:github:github.com:a/b#1",
      title: "Marotte",
      body: "checks failed",
    });
    expect(opened).toHaveBeenCalledWith({
      kind: "git",
      tab: "prs",
      pr: "github:github.com:a/b#1",
    });
  });
});
