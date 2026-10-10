// Turning a posted MESSAGE into a push target, `subject ?? ""` included, through the real seam
// with a spy opener; the destination is push-route.test.ts's. An arrival on a focused page is
// delivered as the page delivers its own `notification` frame, through the real notify.ts.

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import type { Route } from "../route-path.js";
import { registerNotificationOpener } from "../notification-open.js";
import { chatTarget, runTarget, type PushTarget } from "../push-subject.js";

vi.mock("../persist.js", () => ({
  patchSettings: async (): Promise<Record<string, unknown>> => ({ ok: true }),
}));

vi.mock("../actions/notify.js", () => ({
  registerPush: { dispatch: async (): Promise<null> => null, cancel: vi.fn() },
  unsubscribePush: { dispatch: async (): Promise<null> => null },
}));

const notify = await import("../notify.js");
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

let shown: { title: string; body: string | undefined; tag: string | undefined }[] = [];
let onScreen: PushTarget | null = null;

function shadowNotification(): void {
  const fake = function fakeNotification(title: string, o?: NotificationOptions): void {
    shown.push({ title, body: o?.body, tag: o?.tag });
  } as unknown as {
    (title: string, o?: NotificationOptions): void;
    permission: string;
    prototype: { addEventListener: (t: string, f: () => void) => void };
  };
  fake.permission = "granted";
  fake.prototype = { addEventListener: vi.fn() };
  vi.stubGlobal("Notification", fake);
}

beforeEach(() => {
  vi.clearAllMocks();
  registerNotificationOpener(opened);
  shown = [];
  onScreen = null;
  shadowNotification();
  notify.setNotificationsEnabled(true);
  for (const kind of ["agent_finished", "run_outcome", "pr_status"]) {
    notify.setKindEnabled(kind, true);
  }
  notify.setOnScreen((t) => JSON.stringify(t) === JSON.stringify(onScreen));
});

afterEach(() => {
  notify.setOnScreen(() => false);
});

describe("initPushMessages", () => {
  it("re-derives the presence tag on a rotated subscription, and neither routes nor notifies", () => {
    postFromWorker({
      type: "push",
      reason: "subscription_changed",
      chatId: "",
      subject: "",
      kind: "",
      title: "",
      body: "",
    });
    expect(onSubscriptionChanged).toHaveBeenCalledTimes(1);
    expect(opened).not.toHaveBeenCalled();
    expect(shown).toEqual([]);
  });

  it("notifies an arrived push about a background chat under that chat's tag", () => {
    onScreen = chatTarget("c2");
    postFromWorker({
      type: "push",
      reason: "arrived",
      chatId: "c1",
      subject: "",
      kind: "permission",
      title: "Fix the parser",
      body: "Permission required · git push",
    });
    expect(shown).toEqual([
      { title: "Fix the parser", body: "Permission required · git push", tag: "marotte:c1" },
    ]);
    expect(onSubscriptionChanged).not.toHaveBeenCalled();
    expect(opened).not.toHaveBeenCalled();
  });

  it("says nothing about an arrived push for the chat on screen", () => {
    onScreen = chatTarget("c1");
    postFromWorker({
      type: "push",
      reason: "arrived",
      chatId: "c1",
      subject: "",
      kind: "permission",
      title: "Fix the parser",
      body: "Permission required · git push",
    });
    expect(shown).toEqual([]);
  });

  it("notifies an arrived run outcome while another tab is on screen", () => {
    onScreen = chatTarget("c1");
    postFromWorker({
      type: "push",
      reason: "arrived",
      chatId: "",
      subject: "run:wf1",
      kind: "run_outcome",
      title: "deploy",
      body: "Workflow failed · step timed out",
    });
    expect(shown).toEqual([
      { title: "deploy", body: "Workflow failed · step timed out", tag: "marotte:run:wf1" },
    ]);
  });

  it("says nothing about an arrived run outcome while that run is on screen", () => {
    onScreen = runTarget("wf1");
    postFromWorker({
      type: "push",
      reason: "arrived",
      chatId: "",
      subject: "run:wf1",
      kind: "run_outcome",
      title: "deploy",
      body: "Workflow completed",
    });
    expect(shown).toEqual([]);
  });

  it("drops an arrival whose kind the server does not send", () => {
    postFromWorker({
      type: "push",
      reason: "arrived",
      chatId: "c1",
      subject: "",
      kind: "exploded",
      title: "Fix the parser",
      body: "Response complete",
    });
    expect(shown).toEqual([]);
  });
});

describe("routePushMessage", () => {
  it("sends a PR subject to the PRs tab, focused on that pull request", () => {
    routePushMessage({
      type: "push",
      reason: "clicked",
      chatId: "",
      subject: "pr:github:github.com:cplieger/marotte#42",
      title: "cplieger/marotte #42",
      body: "Checks passed",
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
