// Settings > General "History in the sidebar" over the shipped markup: its choices, its save, the cap Days kept
// puts on it and the off state at Days kept 0.
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import indexHtml from "../static/index.html?raw";
import { settingsPayload } from "./__test-helpers__/settings.js";
import { patchSettings, settingsAsShown } from "./persist.js";
import { applyGeneralPanel, initGeneralPanelControls } from "./settings.js";
import type * as Persist from "./persist.js";

vi.mock("./persist.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Persist>()),
  patchSettings: vi.fn(() => Promise.resolve({ outcome: "saved" })),
  settingsAsShown: vi.fn(() => null),
}));

const host = document.createElement("div");

beforeAll(() => {
  const page = new DOMParser().parseFromString(indexHtml, "text/html");
  const general = page.querySelector('[data-settings-panel="general"]');
  if (general === null) {
    throw new Error("index.html has no General panel");
  }
  host.appendChild(document.importNode(general, true));
  document.body.appendChild(host);
  initGeneralPanelControls();
});

afterAll(() => {
  host.remove();
});

beforeEach(() => {
  vi.mocked(patchSettings).mockClear();
  vi.mocked(settingsAsShown).mockReturnValue(null);
});

function select(): HTMLSelectElement {
  const s = host.querySelector<HTMLSelectElement>("select[id='history-window-hours']");
  if (s === null) {
    throw new Error("no #history-window-hours select in the General panel");
  }
  return s;
}

function note(): HTMLElement {
  const n = host.querySelector<HTMLElement>("[id='history-window-hours-cap']");
  if (n === null) {
    throw new Error("no #history-window-hours-cap note in the General panel");
  }
  return n;
}

const disabledChoices = (): string[] =>
  [...select().options].filter((o) => o.disabled).map((o) => o.value);

/** The note as a reader meets it: nothing while hidden. */
const noteText = (): string =>
  note().hidden ? "" : note().textContent.replace(/\s+/g, " ").trim();

describe("History in the sidebar", () => {
  it("sits in the Chat retention section, under Days kept", () => {
    const row = select().closest(".section-option");
    expect(row?.closest("#general-retention-section")).not.toBeNull();
    expect(row?.previousElementSibling?.id).toBe("chat-retention-days-row");
    expect(host.querySelector("label[for='history-window-hours']")?.textContent).toBe(
      "History in the sidebar",
    );
  });

  it("offers the server's choices with 24 hours picked by default", () => {
    applyGeneralPanel(settingsPayload());
    expect([...select().options].map((o) => [o.value, o.textContent])).toEqual([
      ["1", "1 hour"],
      ["6", "6 hours"],
      ["12", "12 hours"],
      ["24", "24 hours"],
      ["48", "2 days"],
      ["72", "3 days"],
      ["168", "1 week"],
    ]);
    expect(select().value).toBe("24");
    expect(select().disabled).toBe(false);
  });

  it("saves the picked window as history_window_hours", () => {
    applyGeneralPanel(settingsPayload());
    select().value = "6";
    select().dispatchEvent(new Event("change"));
    expect(patchSettings).toHaveBeenCalledExactlyOnceWith({ history_window_hours: 6 });
  });

  it("turns off the choices past Days kept and says so", () => {
    applyGeneralPanel(settingsPayload({ chat_retention_days: 2 }));
    expect(disabledChoices()).toEqual(["72", "168"]);
    expect(noteText()).toBe(
      "Chat retention keeps closed chats 2 days, so the sidebar's History goes back 2 days at most.",
    );
  });

  it.each([7, -1])("leaves every choice open at Days kept %i, with no note", (days) => {
    applyGeneralPanel(settingsPayload({ chat_retention_days: 2 }));
    applyGeneralPanel(settingsPayload({ chat_retention_days: days }));
    expect(disabledChoices()).toEqual([]);
    expect(noteText()).toBe("");
    expect(select().disabled).toBe(false);
  });

  it("keeps a stored window past Days kept picked and says the sidebar stops at Days kept", () => {
    applyGeneralPanel(settingsPayload({ chat_retention_days: 1, history_window_hours: 168 }));
    expect(select().value).toBe("168");
    expect(disabledChoices()).toEqual(["48", "72", "168"]);
    expect(noteText()).toBe(
      "Chat retention keeps closed chats 1 day, so the sidebar's History goes back 1 day at most.",
    );
  });

  it("is off at Days kept 0 and says why", () => {
    applyGeneralPanel(settingsPayload({ chat_retention_days: 0 }));
    expect(select().disabled).toBe(true);
    expect(noteText()).toBe(
      "Off while Days kept is 0, because a chat is deleted as soon as you close it.",
    );
  });

  it("follows another device's change", () => {
    applyGeneralPanel(settingsPayload({ chat_retention_days: 2, history_window_hours: 6 }));
    applyGeneralPanel(settingsPayload({ chat_retention_days: -1, history_window_hours: 48 }));
    expect(select().value).toBe("48");
    expect(disabledChoices()).toEqual([]);
    expect(noteText()).toBe("");
  });

  it("follows a Days kept saved on this page", async () => {
    applyGeneralPanel(settingsPayload({ chat_retention_days: 7 }));
    vi.mocked(settingsAsShown).mockReturnValue(settingsPayload({ chat_retention_days: 2 }));
    const days = host.querySelector<HTMLInputElement>("input[id='chat-retention-days']");
    if (days === null) {
      throw new Error("no #chat-retention-days");
    }
    days.value = "2";
    days.dispatchEvent(new Event("change"));
    await vi.waitFor(() => {
      expect(disabledChoices()).toEqual(["72", "168"]);
    });
  });
});
