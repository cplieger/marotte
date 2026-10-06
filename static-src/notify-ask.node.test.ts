import { describe, it, expect, beforeEach } from "vitest";
import { createNotifyAsk, type NotifyAsk, type NotifyAskEnv } from "./notify-ask.js";

interface Harness {
  ask: NotifyAsk;
  env: NotifyAskEnv;
  requests: () => number;
  grants: () => number;
  spent: () => boolean;
  supported: (v: boolean) => void;
  permission: (v: string) => void;
  /** What the next prompt resolves to; `null` makes it throw. */
  answer: (v: string | null) => void;
}

function harness(): Harness {
  let supported = true;
  let permission = "default";
  let spent = false;
  let answer: string | null = "granted";
  let requests = 0;
  let grants = 0;

  const env: NotifyAskEnv = {
    supported: () => supported,
    permission: () => permission,
    request: async () => {
      requests += 1;
      if (answer === null) {
        throw new Error("this browser refuses to be asked");
      }
      permission = answer;
      return answer;
    },
    spent: () => spent,
    markSpent: () => {
      spent = true;
    },
    granted: () => {
      grants += 1;
    },
  };

  return {
    ask: createNotifyAsk(env),
    env,
    requests: () => requests,
    grants: () => grants,
    spent: () => spent,
    supported: (v) => {
      supported = v;
    },
    permission: (v) => {
      permission = v;
    },
    answer: (v) => {
      answer = v;
    },
  };
}

/** The request is fire-and-forget inside `gesture()`, so a case reading `grants()`
 *  has to let its promise chain settle first. */
async function settle(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

describe("the ask needs both an arm and a gesture", () => {
  let h: Harness;
  beforeEach(() => {
    h = harness();
  });

  it("raises nothing on a gesture with nothing armed", () => {
    expect.assertions(2);
    h.ask.gesture();
    h.ask.gesture();
    expect(h.requests()).toBe(0);
    expect(h.spent()).toBe(false);
  });

  it("raises nothing on an arm alone, because the prompt needs user activation", () => {
    expect.assertions(1);
    h.ask.arm();
    expect(h.requests()).toBe(0);
  });

  it("raises the prompt on the first gesture after an arm", () => {
    expect.assertions(1);
    h.ask.arm();
    h.ask.gesture();
    expect(h.requests()).toBe(1);
  });

  it("raises it once per page whatever else happens", () => {
    expect.assertions(1);
    h.ask.arm();
    h.ask.gesture();
    h.ask.arm();
    h.ask.gesture();
    h.ask.gesture();
    expect(h.requests()).toBe(1);
  });
});

describe("what makes an ask worth raising", () => {
  let h: Harness;
  beforeEach(() => {
    h = harness();
  });

  it("declines a browser with no Notification API at all", () => {
    expect.assertions(1);
    h.supported(false);
    h.ask.arm();
    h.ask.gesture();
    expect(h.requests()).toBe(0);
  });

  it("declines an already-granted permission — there is nothing to ask", () => {
    expect.assertions(1);
    h.permission("granted");
    h.ask.arm();
    h.ask.gesture();
    expect(h.requests()).toBe(0);
  });

  it("declines a denied permission, which a prompt cannot reopen", () => {
    expect.assertions(1);
    h.permission("denied");
    h.ask.arm();
    h.ask.gesture();
    expect(h.requests()).toBe(0);
  });

  it("declines a permission value it does not recognise", () => {
    expect.assertions(1);
    h.permission("something-newer");
    h.ask.arm();
    h.ask.gesture();
    expect(h.requests()).toBe(0);
  });

  it("re-reads the gates at the gesture, so a sibling tab's answer wins", () => {
    expect.assertions(1);
    h.ask.arm();
    h.permission("granted");
    h.ask.gesture();
    expect(h.requests()).toBe(0);
  });
});

describe("one ask per device", () => {
  it("declines a FRESH page on a device whose ask is already spent", () => {
    expect.assertions(1);
    const h = harness();
    h.ask.spend();
    // A fresh instance: asking the same one proves nothing, `spend` sets a per-page flag too.
    const reloaded = createNotifyAsk(h.env);
    reloaded.arm();
    reloaded.gesture();
    expect(h.requests()).toBe(0);
  });

  it("spends the device's ask on a DISMISSAL, which is the case a page flag misses", () => {
    expect.assertions(3);
    const h = harness();
    h.answer("default");
    h.ask.arm();
    h.ask.gesture();
    expect(h.spent()).toBe(true);
    expect(h.requests()).toBe(1);
    const reloaded = createNotifyAsk(h.env);
    reloaded.arm();
    reloaded.gesture();
    expect(h.requests()).toBe(1);
  });

  it("spends it on a refusal recorded through no prompt at all", () => {
    expect.assertions(2);
    const h = harness();
    h.ask.spend();
    expect(h.spent()).toBe(true);
    expect(h.requests()).toBe(0);
  });

  it("spends it even when the browser throws rather than be asked", async () => {
    expect.assertions(3);
    const h = harness();
    h.answer(null);
    h.ask.arm();
    h.ask.gesture();
    await settle();
    expect(h.requests()).toBe(1);
    expect(h.grants()).toBe(0);
    expect(h.spent()).toBe(true);
  });
});

describe("adopting the answer", () => {
  it("adopts a grant", async () => {
    expect.assertions(1);
    const h = harness();
    h.answer("granted");
    h.ask.arm();
    h.ask.gesture();
    await settle();
    expect(h.grants()).toBe(1);
  });

  it("adopts nothing on a denial", async () => {
    expect.assertions(1);
    const h = harness();
    h.answer("denied");
    h.ask.arm();
    h.ask.gesture();
    await settle();
    expect(h.grants()).toBe(0);
  });

  it("adopts nothing on a dismissal, which leaves the permission at default", async () => {
    expect.assertions(2);
    const h = harness();
    h.answer("default");
    h.ask.arm();
    h.ask.gesture();
    await settle();
    expect(h.grants()).toBe(0);
    expect(h.spent()).toBe(true);
  });
});
