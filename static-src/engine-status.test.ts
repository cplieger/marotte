import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type * as ApiClient from "./api-client.js";
import type * as BannerStack from "./banner-stack.js";
import type * as Confirm from "./confirm.js";
import type * as SettingsActions from "./actions/settings.js";
import type { EngineStatus } from "./wire/types.gen.js";

vi.mock("./banner-stack.js", async (importOriginal) => ({
  ...(await importOriginal<typeof BannerStack>()),
  showBanner: vi.fn(),
  clearBannerCodes: vi.fn(),
}));

const { mockConfirm, mockRestart, mockGet } = vi.hoisted(() => ({
  mockConfirm: vi.fn<(message: string, label?: string, variant?: string) => Promise<boolean>>(),
  mockRestart: vi.fn<(args: undefined) => Promise<unknown>>(),
  mockGet: vi.fn<(path: string) => Promise<unknown>>(),
}));
vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Confirm>()),
  confirm: mockConfirm,
}));
vi.mock("./actions/settings.js", async (importOriginal) => ({
  ...(await importOriginal<typeof SettingsActions>()),
  restartEngine: { dispatch: mockRestart },
}));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetTyped: mockGet,
}));

import {
  adoptEngineStatus,
  initEngineStatus,
  isNewerStatus,
  paintEngineRows,
  reasonRow,
  reconcileEngineStatus,
  requestRestart,
  restartConfirmText,
  restartPendingText,
} from "./engine-status.js";
import { showBanner, clearBannerCodes } from "./banner-stack.js";

const mockedShow = vi.mocked(showBanner);
const mockedClear = vi.mocked(clearBannerCodes);

const BOOT = 1_760_000_000_000;
let boot = BOOT;
let seq = 0;

/** A status newer than every one built before it, unless `over` names its version. */
function status(over: Partial<EngineStatus> = {}): EngineStatus {
  seq += 1;
  return {
    version: { boot_ms: boot, seq },
    since: "2026-10-16T00:00:00Z",
    state: "ready",
    busy: { turns: [], runs: [] },
    restart: { pending: false, reasons: [] },
    ...over,
  };
}

/** The banner codes shown so far, in call order. */
function shownCodes(): string[] {
  return mockedShow.mock.calls.map((c) => c[1]);
}

function openRead(): (s: EngineStatus | null) => void {
  let answer: (s: EngineStatus | null) => void = () => undefined;
  mockGet.mockReturnValueOnce(
    new Promise((resolve) => {
      answer = resolve;
    }),
  );
  return (s) => {
    answer(s);
  };
}

function settingsRow(controlID: string): string {
  return `<div class="section-option"><input id="${controlID}" aria-describedby="${controlID}-hint"><div><p class="section-hint" id="${controlID}-hint">About it.</p></div></div>`;
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("engine banner", () => {
  it("shows the pending banner with Restart now only while a restart is pending", () => {
    adoptEngineStatus(status({ restart: { pending: true, reasons: ["knowledge_enabled"] } }));
    expect(shownCodes()).toEqual(["engine_restart_pending"]);
    const [chatID, , message, , dismissible, link] = mockedShow.mock.calls[0]!;
    expect(chatID).toBe("*");
    expect(message).toBe(
      "Some settings apply after the agent engine restarts: Knowledge base search. It restarts by itself when nothing is running.",
    );
    expect(dismissible, "a pending change is a persistent indicator").toBe(false);
    expect(link?.label).toBe("Restart now");

    mockedShow.mockClear();
    adoptEngineStatus(status());
    expect(shownCodes(), "nothing pending shows nothing").toEqual([]);
    expect(mockedClear).toHaveBeenCalledWith("*", ["engine_restart_pending"]);
  });

  for (const state of ["restarting", "down"] as const) {
    it(`says the engine is restarting, with no button, while ${state}`, () => {
      adoptEngineStatus(status({ state, restart: { pending: true, reasons: ["auto_routing"] } }));
      expect(mockedShow).toHaveBeenCalledTimes(1);
      const [, code, message, , dismissible, link] = mockedShow.mock.calls[0]!;
      expect(code).toBe(state === "down" ? "engine_down" : "engine_restarting");
      expect(message).toBe("The agent engine is restarting.");
      expect(dismissible).toBe(false);
      expect(link, "a restart under way offers no button").toBeUndefined();
      expect(mockedClear.mock.calls.flatMap((c) => c[1])).toContain("engine_restart_pending");
    });
  }

  it("clears the restarting banner once the engine is ready again", () => {
    adoptEngineStatus(status());
    expect(mockedClear).toHaveBeenCalledWith("*", ["engine_restarting", "engine_down"]);
  });
});

describe("status ordering", () => {
  const pending = { pending: true, reasons: ["knowledge_enabled"] };

  it("keeps a pending banner when an older GET answer lands after the event", () => {
    const olderGet = status();
    adoptEngineStatus(status({ restart: pending }));
    mockedClear.mockClear();
    adoptEngineStatus(olderGet);
    expect(mockedClear, "the stale answer clears nothing").not.toHaveBeenCalled();
    adoptEngineStatus(status());
    expect(mockedClear).toHaveBeenCalledWith("*", ["engine_restart_pending"]);
  });

  it("takes a newer event over a GET answer adopted first", () => {
    adoptEngineStatus(status());
    mockedShow.mockClear();
    adoptEngineStatus(status({ restart: pending }));
    expect(shownCodes()).toEqual(["engine_restart_pending"]);
  });

  it("takes a later process at any seq and never an earlier one again", () => {
    const held = status();
    const restarted = status({ version: { boot_ms: boot + 5_000, seq: 1 } });
    expect(isNewerStatus(restarted, held)).toBe(true);
    expect(isNewerStatus(held, held), "the same snapshot again is not newer").toBe(false);
    expect(isNewerStatus(held, null)).toBe(true);
    const lateOld = status();
    expect(lateOld.version.seq, "the old process's answer carries a higher seq").toBeGreaterThan(1);
    expect(isNewerStatus(lateOld, restarted), "the replaced process's late answer").toBe(false);
  });

  it("keeps the restarted process's banner when a late answer from the old one lands", () => {
    const oldBoot = boot;
    boot += 10_000;
    adoptEngineStatus(status({ version: { boot_ms: boot, seq: 1 }, restart: pending }));
    mockedClear.mockClear();
    adoptEngineStatus(status({ version: { boot_ms: oldBoot, seq: seq + 1 } }));
    expect(mockedClear, "the replaced process's answer clears nothing").not.toHaveBeenCalled();
    adoptEngineStatus(status());
    expect(mockedClear).toHaveBeenCalledWith("*", ["engine_restart_pending"]);
  });

  /** The read it leaves open stands for the old process's answer to a read asked before the stream reached the
   *  replacement. */
  function reconcileWithOpenRead(): (s: EngineStatus) => Promise<void> {
    const answer = openRead();
    const done = reconcileEngineStatus(new AbortController().signal);
    return async (s) => {
      answer(s);
      await done;
    };
  }

  for (const [name, step] of [
    ["forward", 10_000],
    ["backwards", -10_000],
  ] as const) {
    it(`takes the replacement after a reconcile when the clock moved ${name}, and no late answer from the old process`, async () => {
      const oldBoot = boot;
      const lateOld = reconcileWithOpenRead();
      adoptEngineStatus(status({ restart: pending }));
      expect(shownCodes()).toEqual(["engine_restart_pending"]);

      boot = oldBoot + step;
      mockGet.mockResolvedValueOnce(status({ version: { boot_ms: boot, seq: 1 } }));
      mockedClear.mockClear();
      await reconcileEngineStatus(new AbortController().signal);
      expect(mockedClear, "the replacement's status is painted").toHaveBeenCalledWith("*", [
        "engine_restart_pending",
      ]);

      mockedShow.mockClear();
      await lateOld(status({ version: { boot_ms: oldBoot, seq: seq + 1 }, restart: pending }));
      expect(shownCodes(), "the old process's late answer paints nothing").toEqual([]);

      adoptEngineStatus(status({ version: { boot_ms: boot, seq: 2 }, restart: pending }));
      expect(shownCodes(), "the replacement's next event is taken").toEqual([
        "engine_restart_pending",
      ]);
      boot = Math.max(oldBoot, boot) + 10_000;
    });
  }

  describe("when the reconcile's read fails", () => {
    let host: HTMLDivElement;

    beforeEach(() => {
      host = document.createElement("div");
      host.innerHTML = `${settingsRow("flag-knowledge")}<button id="engine-restart"></button>`;
      document.body.append(host);
    });

    afterEach(() => {
      host.remove();
    });

    async function failReconcile(): Promise<void> {
      mockGet.mockResolvedValueOnce(null);
      await reconcileEngineStatus(new AbortController().signal);
    }

    it("clears the old process's banners and offers the Settings Restart now again", async () => {
      adoptEngineStatus(status({ state: "restarting" }));
      const btn = host.querySelector<HTMLButtonElement>("#engine-restart")!;
      expect(btn.disabled).toBe(true);
      mockedClear.mockClear();
      await failReconcile();
      expect(mockedClear).toHaveBeenCalledWith("*", [
        "engine_restart_pending",
        "engine_restarting",
        "engine_down",
      ]);
      expect(btn.disabled, "the old process's restart no longer disables it").toBe(false);
    });

    it("hides the old process's inline Restart now", async () => {
      adoptEngineStatus(status({ restart: pending }));
      const inline = (): Element | null =>
        host.querySelector("#flag-knowledge-engine-hint .engine-restart-now");
      expect(inline()?.classList.contains("hidden")).toBe(false);
      await failReconcile();
      expect(inline()?.classList.contains("hidden")).toBe(true);
    });
  });

  it("keeps an event taken while the reconcile's read was failing", async () => {
    const answer = openRead();
    const done = reconcileEngineStatus(new AbortController().signal);
    adoptEngineStatus(status({ restart: pending }));
    mockedClear.mockClear();
    answer(null);
    await done;
    expect(mockedClear, "the event's banner stays").not.toHaveBeenCalled();
  });
});

describe("Settings Restart now", () => {
  let btn: HTMLButtonElement;

  beforeEach(() => {
    btn = document.createElement("button");
    btn.id = "engine-restart";
    document.body.append(btn);
  });

  afterEach(() => {
    btn.remove();
  });

  for (const [state, disabled] of [
    ["starting", false],
    ["ready", false],
    ["down", false],
    ["restarting", true],
  ] as const) {
    it(`is ${disabled ? "disabled" : "offered"} while ${state}`, () => {
      adoptEngineStatus(status({ state }));
      expect(btn.disabled).toBe(disabled);
    });
  }

  it("opens the confirmation when pressed", async () => {
    mockGet.mockResolvedValue(status());
    mockConfirm.mockResolvedValueOnce(false);
    initEngineStatus();
    btn.click();
    await vi.waitFor(() => {
      expect(mockConfirm).toHaveBeenCalledTimes(1);
    });
    mockGet.mockReset();
  });
});

describe("restart reasons", () => {
  it("map each engine-wide setting key to its Settings row", () => {
    expect(reasonRow("knowledge_enabled")?.controlID).toBe("flag-knowledge");
    expect(reasonRow("telemetry.enabled")?.controlID).toBe("flag-telemetry");
    expect(reasonRow("engine_heap_mib")?.controlID).toBe("engine-heap-mib");
    expect(reasonRow("account_switch"), "the account has no Settings row").toBeNull();
    expect(reasonRow("kiro_cli_version")).toBeNull();
    // An inherited name must not read as a row.
    expect(reasonRow("constructor")).toBeNull();
  });

  it("render an account switch as the account sentence alone", () => {
    expect(restartPendingText(["account_switch"])).toBe(
      "The new Kiro account applies after the agent engine restarts. It restarts by itself when nothing is running.",
    );
  });

  it("name a new kiro-cli version, and list several settings by name", () => {
    expect(
      restartPendingText(["model_fallback_enabled", "auto_delegation", "kiro_cli_version"]),
    ).toBe(
      "Some settings apply after the agent engine restarts: Model fallback and Smart helper models. " +
        "A new kiro-cli version applies after the agent engine restarts. It restarts by itself when nothing is running.",
    );
  });

  it("fall back to the plain sentence for a key this page does not know", () => {
    expect(restartPendingText(["knowledge_enabled", "new_setting"])).toBe(
      "Some settings apply after the agent engine restarts. It restarts by itself when nothing is running.",
    );
  });
});

describe("restart confirmation", () => {
  it("lists the busy chats and workflows by name", () => {
    expect(
      restartConfirmText({
        turns: [
          { chat_id: "c-1", name: "A" },
          { chat_id: "c-2", name: "B" },
        ],
        runs: [{ workflow_id: "wf_1", name: "W" }],
      }),
    ).toBe(
      "Restarting stops the reply in 2 chats: A, B, and pauses 1 workflow: W, which resumes by itself.",
    );
    expect(
      restartConfirmText({
        turns: [],
        runs: [
          { workflow_id: "wf_1", name: "" },
          { workflow_id: "wf_2", name: "X" },
        ],
      }),
    ).toBe("Restarting pauses 2 workflows: wf_1, X, which resume by themselves.");
    expect(restartConfirmText({ turns: [{ chat_id: "c-1", name: "" }], runs: [] })).toBe(
      "Restarting stops the reply in 1 chat: Untitled chat.",
    );
    expect(restartConfirmText({ turns: [], runs: [] })).toBe(
      "Restart the agent engine? Nothing is running, so nothing is interrupted.",
    );
  });

  it("reads what is running now, confirms it, then restarts", async () => {
    mockGet.mockResolvedValueOnce(
      status({ busy: { turns: [{ chat_id: "c-1", name: "A" }], runs: [] } }),
    );
    mockConfirm.mockResolvedValueOnce(true);
    await requestRestart();
    expect(mockGet.mock.calls[0]?.[0]).toBe("/api/engine");
    expect(mockConfirm).toHaveBeenCalledWith(
      "Restarting stops the reply in 1 chat: A.",
      "Restart now",
      "destructive",
    );
    expect(mockRestart).toHaveBeenCalledTimes(1);
  });

  it("restarts nothing when the reader cancels", async () => {
    mockGet.mockResolvedValueOnce(status());
    mockConfirm.mockResolvedValueOnce(false);
    await requestRestart();
    expect(mockConfirm.mock.calls[0]?.[2], "nothing running is not destructive").toBe("normal");
    expect(mockRestart).not.toHaveBeenCalled();
  });

  const runningIn = (name: string): EngineStatus["busy"] => ({
    turns: [{ chat_id: `c-${name}`, name }],
    runs: [],
  });

  it("lists the replacement's work when a reconcile overtakes the read", async () => {
    const oldBoot = boot;
    const oldAnswer = openRead();
    mockConfirm.mockResolvedValueOnce(false);
    const done = requestRestart();
    boot += 10_000;
    mockGet.mockResolvedValueOnce(
      status({ version: { boot_ms: boot, seq: 1 }, busy: runningIn("B") }),
    );
    await reconcileEngineStatus(new AbortController().signal);
    oldAnswer(status({ version: { boot_ms: oldBoot, seq: seq + 1 }, busy: runningIn("A") }));
    await done;
    expect(mockConfirm.mock.calls[0]?.[0]).toBe("Restarting stops the reply in 1 chat: B.");
  });

  it("says it could not read what is running when a reconcile overtakes the read before the replacement answers", async () => {
    const oldAnswer = openRead();
    mockConfirm.mockResolvedValueOnce(false);
    const done = requestRestart();
    const replacementAnswer = openRead();
    const reconciled = reconcileEngineStatus(new AbortController().signal);
    oldAnswer(status({ busy: runningIn("A") }));
    await done;
    expect(mockConfirm.mock.calls[0]?.[0]).toContain("could not read what is running");
    replacementAnswer(null);
    await reconciled;
  });

  it("lists the work of an event newer than the read's answer", async () => {
    const answer = openRead();
    mockConfirm.mockResolvedValueOnce(false);
    const olderAnswer = status();
    const done = requestRestart();
    adoptEngineStatus(status({ busy: runningIn("B") }));
    answer(olderAnswer);
    await done;
    expect(mockConfirm.mock.calls[0]?.[0]).toBe("Restarting stops the reply in 1 chat: B.");
  });

  it("says it could not read what is running when the read fails", async () => {
    mockGet.mockResolvedValueOnce(null);
    mockConfirm.mockResolvedValueOnce(true);
    await requestRestart();
    expect(mockConfirm.mock.calls[0]?.[0]).toContain("could not read what is running");
    expect(mockConfirm.mock.calls[0]?.[2]).toBe("destructive");
    expect(mockRestart).toHaveBeenCalledTimes(1);
  });
});

describe("engine-wide Settings rows", () => {
  let host: HTMLDivElement;

  beforeEach(() => {
    host = document.createElement("div");
    host.innerHTML =
      settingsRow("flag-knowledge") + settingsRow("auto-routing") + settingsRow("memory-mode");
    document.body.append(host);
  });

  afterEach(() => {
    host.remove();
  });

  it("gives every engine-wide row the restart hint once, described by its control", () => {
    paintEngineRows(null);
    paintEngineRows(null);
    const hint = document.getElementById("flag-knowledge-engine-hint");
    expect(hint?.textContent).toContain("Applies after the agent engine restarts.");
    expect(host.querySelectorAll("#flag-knowledge-engine-hint")).toHaveLength(1);
    expect(document.getElementById("flag-knowledge")?.getAttribute("aria-describedby")).toBe(
      "flag-knowledge-hint flag-knowledge-engine-hint",
    );
    expect(
      document.getElementById("memory-mode-engine-hint"),
      "a session-door row gets none",
    ).toBeNull();
  });

  it("shows the inline Restart now only on the rows whose change is pending", () => {
    paintEngineRows(status({ restart: { pending: true, reasons: ["auto_routing"] } }));
    const visible = (id: string): boolean =>
      document
        .querySelector(`#${id}-engine-hint .engine-restart-now`)
        ?.classList.contains("hidden") === false;
    expect(visible("auto-routing")).toBe(true);
    expect(visible("flag-knowledge")).toBe(false);

    paintEngineRows(
      status({ state: "restarting", restart: { pending: true, reasons: ["auto_routing"] } }),
    );
    expect(visible("auto-routing"), "no button while a restart is under way").toBe(false);

    paintEngineRows(status());
    expect(visible("auto-routing")).toBe(false);
  });

  it("confirms before restarting from a row", async () => {
    paintEngineRows(status({ restart: { pending: true, reasons: ["auto_routing"] } }));
    mockGet.mockResolvedValueOnce(status());
    mockConfirm.mockResolvedValueOnce(true);
    document
      .querySelector<HTMLButtonElement>("#auto-routing-engine-hint .engine-restart-now")
      ?.click();
    await vi.waitFor(() => {
      expect(mockRestart).toHaveBeenCalledTimes(1);
    });
    expect(mockConfirm).toHaveBeenCalledTimes(1);
  });
});
