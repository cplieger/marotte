import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import type { NotificationRegistration } from "./notify.js";
import { chatTarget, runTarget } from "./push-subject.js";
import { noticeFor } from "./__test-helpers__/notice.js";

vi.mock("./persist.js", () => ({
  patchSettings: (): Promise<{ ok: boolean }> => Promise.resolve({ ok: true }),
}));
vi.mock("./actions/notify.js", () => ({
  registerPush: { dispatch: (): Promise<null> => Promise.resolve(null) },
  unsubscribePush: { dispatch: (): Promise<null> => Promise.resolve(null) },
}));

const notify = await import("./notify.js");

/** A minted notification; close() fires "close", as the real one does. */
class FakeNotification extends EventTarget {
  static permission = "granted";
  static instances: FakeNotification[] = [];
  readonly tag: string;
  closed = false;
  constructor(_title: string, opts?: { tag?: string }) {
    super();
    this.tag = opts?.tag ?? "";
    FakeNotification.instances.push(this);
  }
  close(): void {
    this.closed = true;
    this.dispatchEvent(new Event("close"));
  }
}

/** A registration whose notifications are the tags handed in; close() records. */
function fakeRegistration(tags: string[]): {
  reg: NotificationRegistration;
  closed: string[];
  asked: string[];
} {
  const closed: string[] = [];
  const asked: string[] = [];
  const reg: NotificationRegistration = {
    getNotifications: (filter?: GetNotificationOptions) => {
      const want = filter?.tag;
      asked.push(want ?? "");
      return Promise.resolve(
        tags
          .filter((t) => want === undefined || t === want)
          .map((t) => ({ tag: t, close: () => closed.push(t) }) as unknown as Notification),
      );
    },
  };
  return { reg, closed, asked };
}

beforeEach(() => {
  FakeNotification.instances = [];
  vi.stubGlobal("Notification", FakeNotification);
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
  notify.setNotificationsEnabled(true);
});

afterEach(() => {
  notify._setRegistrationForTest(null);
  notify.setNotificationsEnabled(false);
});

describe("notifyOffScreen's tag", () => {
  it("is the target's tag, so a chat's banner and the workspace cue take different slots", () => {
    expect(notify.notifyOffScreen(noticeFor(chatTarget("c1"), "ask"))).toBe(true);
    expect(notify.notifyOffScreen(noticeFor(chatTarget(""), "done"))).toBe(true);
    expect(FakeNotification.instances.map((n) => n.tag)).toEqual(["marotte:c1", "marotte"]);
  });
});

describe("closeNotificationsFor", () => {
  it("closes the page notification carrying the target's tag and no other", async () => {
    notify._setRegistrationForTest(() => Promise.resolve(null));
    notify.notifyOffScreen(noticeFor(chatTarget("c1"), "ask on c1"));
    notify.notifyOffScreen(noticeFor(chatTarget("c2"), "ask on c2"));

    await notify.closeNotificationsFor(chatTarget("c1"));

    expect(FakeNotification.instances.map((n) => [n.tag, n.closed])).toEqual([
      ["marotte:c1", true],
      ["marotte:c2", false],
    ]);
  });

  it("closes exactly the registration's notifications with that tag", async () => {
    const { reg, closed, asked } = fakeRegistration(["marotte:c1", "marotte:c2", "marotte"]);
    notify._setRegistrationForTest(() => Promise.resolve(reg));

    await notify.closeNotificationsFor(chatTarget("c1"));

    expect(asked).toEqual(["marotte:c1"]);
    expect(closed).toEqual(["marotte:c1"]);
  });

  it("closes a run-keyed banner by the run target", async () => {
    const { reg, closed } = fakeRegistration(["marotte:run:wf_1", "marotte:c1"]);
    notify._setRegistrationForTest(() => Promise.resolve(reg));

    await notify.closeNotificationsFor(runTarget("wf_1"));

    expect(closed).toEqual(["marotte:run:wf_1"]);
  });

  it("is a no-op with nothing shown and no registration", async () => {
    notify._setRegistrationForTest(() => Promise.resolve(null));
    await expect(notify.closeNotificationsFor(chatTarget("c9"))).resolves.toBeUndefined();
  });

  it("forgets a page notification the reader closed, so a later retraction does not close it twice", async () => {
    notify._setRegistrationForTest(() => Promise.resolve(null));
    notify.notifyOffScreen(noticeFor(chatTarget("c1"), "ask"));
    const first = FakeNotification.instances[0];
    first?.close();
    const closeSpy = vi.spyOn(first as FakeNotification, "close");

    await notify.closeNotificationsFor(chatTarget("c1"));

    expect(closeSpy).not.toHaveBeenCalled();
  });
});

describe("closeNotificationsExcept", () => {
  it("closes every chat and run banner the live set does not name, in one registration read", async () => {
    const { reg, closed, asked } = fakeRegistration([
      "marotte:c1",
      "marotte:c2",
      "marotte:run:wf_1",
      "marotte:run:wf_2",
    ]);
    notify._setRegistrationForTest(() => Promise.resolve(reg));

    await notify.closeNotificationsExcept(new Set(["marotte:c2", "marotte:run:wf_2"]));

    expect(asked, "one unfiltered read").toEqual([""]);
    expect(closed).toEqual(["marotte:c1", "marotte:run:wf_1"]);
  });

  it("leaves a pull request's banner and the constant-tag cue alone", async () => {
    const { reg, closed } = fakeRegistration(["marotte:pr:github:x#1", "marotte", "marotte:c1"]);
    notify._setRegistrationForTest(() => Promise.resolve(reg));

    await notify.closeNotificationsExcept(new Set());

    expect(closed).toEqual(["marotte:c1"]);
  });

  it("closes the page's own chat banner the set does not name and keeps the one it does", async () => {
    notify._setRegistrationForTest(() => Promise.resolve(null));
    notify.notifyOffScreen(noticeFor(chatTarget("c1"), "ask on c1"));
    notify.notifyOffScreen(noticeFor(chatTarget("c2"), "ask on c2"));
    notify.notifyOffScreen(noticeFor(chatTarget(""), "done"));

    await notify.closeNotificationsExcept(new Set(["marotte:c2"]));

    expect(FakeNotification.instances.map((n) => [n.tag, n.closed])).toEqual([
      ["marotte:c1", true],
      ["marotte:c2", false],
      ["marotte", false],
    ]);
  });
});

describe("the default registration", () => {
  it("is the current registration or none, never a promise that waits for one", async () => {
    const getRegistration = vi.fn(() => Promise.resolve(undefined));
    vi.stubGlobal("navigator", {
      serviceWorker: { getRegistration, ready: new Promise(() => undefined) },
    });
    notify._setRegistrationForTest(null);
    notify.notifyOffScreen(noticeFor(chatTarget("c1"), "ask on c1"));

    await expect(notify.closeNotificationsFor(chatTarget("c1"))).resolves.toBeUndefined();

    expect(getRegistration).toHaveBeenCalledTimes(1);
    expect(FakeNotification.instances.map((n) => n.closed)).toEqual([true]);
  });
});
