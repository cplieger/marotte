// The "Deliver now" banner: shown for an idle chat holding workflow messages that read "not delivered yet", once
// however many wait, and never while a turn runs.

import { describe, it, expect, beforeEach, vi } from "vitest";
import type { EntrySteer } from "./wire/types.gen.js";
import { makeSession, makeTurn } from "./__test-helpers__/model.js";

interface BannerLink {
  readonly label: string;
  readonly onClick?: () => void;
}

const showBanner = vi.fn(
  (
    _chat: string,
    _code: string,
    _msg: string,
    _level: string,
    _dismiss: boolean,
    _link?: BannerLink,
  ) => undefined,
);
const clearBannerCodes = vi.fn((_chat: string, _codes: string[]) => undefined);
vi.mock("./banner-stack.js", () => ({ showBanner, clearBannerCodes }));

const submitLabelled = vi.fn(async (_chat: string, _text: string, _label: string) => "sent");
vi.mock("./submit.js", () => ({ submitLabelled }));

const store = await import("./store.js");
const { initWorkflowDeliverNow, waitingWorkflowMessages } =
  await import("./workflow-deliver-now.js");

const WAKE = "Workflow updates are waiting for you. Read them and continue.";

function steer(over: Partial<EntrySteer> = {}): EntrySteer {
  return {
    text: "The review step finished.",
    origin: "step",
    step: "review",
    origin_run: "wf-1",
    produced_ts: 1000,
    ...over,
  };
}

/** One turn per steer, each holding that steer's row. */
function unstamped(): EntrySteer {
  const row = steer();
  delete row.produced_ts;
  return row;
}

function sessionWith(
  steers: EntrySteer[],
  over: { thinking?: boolean; turn_open?: boolean } = {},
  idPrefix = "wfmsg-",
) {
  return makeSession({
    id: "c-1",
    ...over,
    turns: steers.map((payload, i) =>
      makeTurn({
        id: `t-${String(i + 1)}`,
        n: i + 1,
        body: [
          { id: `${idPrefix}${String(i + 1)}`, kind: "steer", payload },
          { id: `c-${String(i + 1)}`, kind: "turn_close", payload: { outcome: "completed" } },
        ],
      }),
    ),
  });
}

/** Seats the chat and lets the effect run: reactive effects flush after the writing task. */
async function seat(
  steers: EntrySteer[],
  over: { thinking?: boolean; turn_open?: boolean } = {},
): Promise<void> {
  store.setSessions([sessionWith(steers, over)]);
  store.setActive("c-1");
  await new Promise<void>((resolve) => {
    setTimeout(resolve, 0);
  });
}

const lastBanner = () => showBanner.mock.calls.at(-1);

beforeEach(() => {
  showBanner.mockClear();
  clearBannerCodes.mockClear();
  submitLabelled.mockClear();
  store.setSessions([]);
});

describe("waitingWorkflowMessages", () => {
  it("counts a step message stamped as sent with no take-up", () => {
    expect(waitingWorkflowMessages(sessionWith([steer(), steer()]))).toBe(2);
  });

  it("skips a notice KAS logged when it read it, which no take-up will ever settle", () => {
    expect(waitingWorkflowMessages(sessionWith([steer(), steer()], {}, "notify-"))).toBe(0);
  });

  it("skips a message that was read, dropped, or never stamped, and every other origin", () => {
    const s = sessionWith([
      steer({ state: "read", read_ts: 2000 }),
      steer({ state: "dropped", reason: "boundary" }),
      unstamped(),
      steer({ origin: "user" }),
      steer({ origin: "agent" }),
    ]);
    expect(waitingWorkflowMessages(s)).toBe(0);
  });
});

describe("the Deliver now banner", () => {
  it("offers the control on an idle chat and starts a labelled turn when pressed", async () => {
    initWorkflowDeliverNow();
    await seat([steer()]);

    const call = lastBanner();
    expect(call?.[0]).toBe("c-1");
    expect(call?.[2]).toBe("A workflow update is waiting for the agent.");
    expect(call?.[5]?.label).toBe("Deliver now");

    call?.[5]?.onClick?.();
    expect(submitLabelled).toHaveBeenCalledExactlyOnceWith("c-1", WAKE, "Deliver workflow updates");
  });

  it("states one banner for several waiting cards", async () => {
    initWorkflowDeliverNow();
    await seat([steer(), steer(), steer()]);

    const banners = showBanner.mock.calls.filter((c) => c[0] === "c-1");
    expect(new Set(banners.map((c) => c[1])).size).toBe(1);
    expect(lastBanner()?.[2]).toBe("3 workflow updates are waiting for the agent.");
  });

  it("shows nothing while a turn runs, since delivery is already imminent", async () => {
    initWorkflowDeliverNow();
    await seat([steer()], { thinking: true });

    expect(showBanner).not.toHaveBeenCalled();
    expect(clearBannerCodes).toHaveBeenCalledWith("c-1", ["workflow_updates_waiting"]);
  });

  it("withdraws the control when a turn starts", async () => {
    initWorkflowDeliverNow();
    await seat([steer()]);
    showBanner.mockClear();
    clearBannerCodes.mockClear();

    await seat([steer()], { turn_open: true });

    expect(showBanner).not.toHaveBeenCalled();
    expect(clearBannerCodes).toHaveBeenCalledWith("c-1", ["workflow_updates_waiting"]);
  });

  it("withdraws the control once the messages are taken up", async () => {
    initWorkflowDeliverNow();
    await seat([steer()]);
    clearBannerCodes.mockClear();

    await seat([steer({ state: "read", read_ts: 2000 })]);

    expect(clearBannerCodes).toHaveBeenCalledWith("c-1", ["workflow_updates_waiting"]);
  });

  it("starts one turn however many times it is pressed before that turn shows", async () => {
    initWorkflowDeliverNow();
    await seat([steer()]);
    const press = lastBanner()?.[5]?.onClick;

    press?.();
    press?.();

    expect(submitLabelled).toHaveBeenCalledOnce();
  });
});
