// The additional-directories list in the Settings panel (agent-dirs-ui.ts).
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { settingsPayload } from "./__test-helpers__/settings.js";
import type * as SettingsActions from "./actions/settings.js";
import type { EffectiveSettings } from "./persist.js";

type Outcome =
  | { status: "success"; value: EffectiveSettings }
  | { status: "error"; error: { message: string; status?: number } };

const mocks = vi.hoisted(() => ({
  dispatch: vi.fn<(args: { dirs: readonly string[] }) => { outcome: Promise<Outcome> }>(),
}));

vi.mock("./actions/settings.js", async (importOriginal) => ({
  ...(await importOriginal<typeof SettingsActions>()),
  setAdditionalDirectories: { dispatch: mocks.dispatch },
}));

import { applyAgentDirs, initAgentDirsUI } from "./agent-dirs-ui.js";
import { byId } from "./dom.js";
import { servedFileRoute } from "./utils-url.js";
import { additionalDirectories, _resetForTest as resetWorkspace } from "./workspace.js";

function accept(committed: Partial<EffectiveSettings> = {}): void {
  mocks.dispatch.mockImplementation(() => ({
    outcome: Promise.resolve(answer(committed)),
  }));
}

let commits = 0;

/** A save's answer, committed at the next revision as the server commits each write. */
function answer(committed: Partial<EffectiveSettings>): Outcome {
  return {
    status: "success",
    value: settingsPayload({ settings_epoch: 1, settings_revision: ++commits, ...committed }),
  };
}

function holdSave(): (o: Outcome) => void {
  let settle: (o: Outcome) => void = () => undefined;
  mocks.dispatch.mockImplementation(() => ({
    outcome: new Promise<Outcome>((resolve) => {
      settle = resolve;
    }),
  }));
  return (o) => {
    settle(o);
  };
}

function refuse(message: string): void {
  mocks.dispatch.mockImplementation(() => ({
    outcome: Promise.resolve({ status: "error", error: { message, status: 400 } }),
  }));
}

async function flush(): Promise<void> {
  for (let i = 0; i < 4; i++) {
    await Promise.resolve();
  }
}

function removeButtons(): HTMLButtonElement[] {
  return [...byId("agent-dirs-chips").querySelectorAll<HTMLButtonElement>(".chip-remove")];
}

function chipLabels(): string[] {
  return [...byId("agent-dirs-chips").querySelectorAll(".chip-label")].map((c) => c.textContent);
}

function add(value: string): void {
  const input = byId<HTMLInputElement>("agent-dirs-input");
  input.value = value;
  byId<HTMLButtonElement>("agent-dirs-add").click();
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.innerHTML = `
    <span class="settings-save-status hidden" data-save-status="additional_directories"></span>
    <div id="agent-dirs-chips"></div>
    <ul id="agent-dirs-refused" class="hidden"></ul>
    <input id="agent-dirs-input" type="text">
    <button id="agent-dirs-add" type="button">Add</button>
    <p id="agent-dirs-error" class="hidden"></p>
    <p id="agent-dirs-empty-hint" class="hidden"></p>`;
  initAgentDirsUI();
  applyAgentDirs(settingsPayload({ additional_directories: ["/data"] }));
});

afterEach(() => {
  document.body.replaceChildren();
  resetWorkspace();
});

const GONE_REASON = `invalid additional directory "/gone": no such directory`;

function withRefusedGone(): void {
  applyAgentDirs(
    settingsPayload({
      additional_directories: ["/data", "/gone"],
      additional_directories_refused: { "/gone": GONE_REASON },
    }),
  );
}

function chipNotes(): (string | null)[] {
  return [...byId("agent-dirs-chips").children].map(
    (c) => c.querySelector(".chip-note")?.textContent ?? null,
  );
}

function refusalReasons(): string[] {
  return [...byId("agent-dirs-refused").querySelectorAll("li")].map((li) => li.textContent);
}

describe("additional directories", () => {
  it("renders the stored list, and the empty hint only when it is empty", () => {
    expect(chipLabels()).toEqual(["/data"]);
    expect(byId("agent-dirs-empty-hint").classList.contains("hidden")).toBe(true);
    applyAgentDirs(settingsPayload({ additional_directories: [] }));
    expect(chipLabels()).toEqual([]);
    expect(byId("agent-dirs-empty-hint").classList.contains("hidden")).toBe(false);
  });

  it("sends the whole list on add and shows the list the server then holds", async () => {
    accept({ additional_directories: ["/data", "/srv/media"] });
    add("/srv/media");
    await flush();
    expect(mocks.dispatch).toHaveBeenCalledWith({ dirs: ["/data", "/srv/media"] });
    expect(chipLabels()).toEqual(["/data", "/srv/media"]);
    expect(byId<HTMLInputElement>("agent-dirs-input").value).toBe("");
  });

  it("shows the server's reason beside the field and keeps the list and the typed text on a refusal", async () => {
    const reason = `invalid additional directory "/config": it is or lies inside marotte's config directory`;
    refuse(reason);
    add("/config");
    await flush();
    const error = byId("agent-dirs-error");
    expect(error.textContent).toBe(reason);
    expect(error.classList.contains("hidden")).toBe(false);
    expect(byId("agent-dirs-input").getAttribute("aria-invalid")).toBe("true");
    expect(byId<HTMLInputElement>("agent-dirs-input").value).toBe("/config");
    expect(byId<HTMLInputElement>("agent-dirs-input").disabled).toBe(false);
    expect(chipLabels()).toEqual(["/data"]);
  });

  it("returns focus to the field after a refused save, beside its reason", async () => {
    const settle = holdSave();
    const input = byId<HTMLInputElement>("agent-dirs-input");
    input.focus();
    const blurred = new Promise((resolve) => {
      input.addEventListener("blur", resolve, { once: true });
    });
    input.value = "relative";
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter" }));
    await blurred;
    settle({ status: "error", error: { message: "refused", status: 400 } });
    await flush();
    expect(input.getAttribute("aria-invalid")).toBe("true");
    expect(document.activeElement).toBe(input);
  });

  it("clears the reason once the reader edits the field", async () => {
    refuse("refused");
    add("relative");
    await flush();
    byId("agent-dirs-input").dispatchEvent(new Event("input"));
    expect(byId("agent-dirs-error").classList.contains("hidden")).toBe(true);
    expect(byId("agent-dirs-input").hasAttribute("aria-invalid")).toBe(false);
  });

  it("removes an entry by sending the list without it", async () => {
    accept({ additional_directories: [] });
    byId("agent-dirs-chips").querySelector<HTMLButtonElement>(".chip-remove")?.click();
    await flush();
    expect(mocks.dispatch).toHaveBeenCalledWith({ dirs: [] });
    expect(chipLabels()).toEqual([]);
  });

  it("sends nothing for an empty field or an entry already listed", () => {
    accept();
    add("");
    add("/data");
    expect(mocks.dispatch).not.toHaveBeenCalled();
  });

  it("sends the typed path byte for byte, so a name ending in a space can be added", async () => {
    accept({ additional_directories: ["/data", "/srv/reports "] });
    add("/srv/reports ");
    await flush();
    expect(mocks.dispatch).toHaveBeenCalledWith({ dirs: ["/data", "/srv/reports "] });
    expect(chipLabels()).toEqual(["/data", "/srv/reports "]);
  });

  it("leaves a blank-only entry for the server to explain", () => {
    refuse(`invalid additional directory " ": give an absolute path`);
    add(" ");
    expect(mocks.dispatch).toHaveBeenCalledWith({ dirs: ["/data", " "] });
  });

  it("starts no second write until the first one's answer is adopted, then builds on it", async () => {
    const settle = holdSave();
    add("/srv/a");
    await flush();
    add("/srv/b");
    const chip = byId("agent-dirs-chips").firstElementChild as HTMLElement;
    chip.querySelector<HTMLButtonElement>(".chip-remove")?.click();
    chip.dispatchEvent(new KeyboardEvent("keydown", { key: "Delete" }));
    expect(mocks.dispatch).toHaveBeenCalledTimes(1);
    expect(byId<HTMLInputElement>("agent-dirs-input").disabled).toBe(true);
    expect(removeButtons().map((b) => b.disabled)).toEqual([true]);

    settle(answer({ additional_directories: ["/data", "/srv/a"] }));
    await flush();
    expect(chipLabels()).toEqual(["/data", "/srv/a"]);
    expect(byId<HTMLInputElement>("agent-dirs-input").disabled).toBe(false);
    expect(removeButtons().map((b) => b.disabled)).toEqual([false, false]);

    accept({ additional_directories: ["/data", "/srv/a", "/srv/b"] });
    add("/srv/b");
    expect(mocks.dispatch).toHaveBeenLastCalledWith({ dirs: ["/data", "/srv/a", "/srv/b"] });
  });

  it("keeps chips mounted during a write locked", async () => {
    const settle = holdSave();
    add("/srv/a");
    applyAgentDirs(settingsPayload({ additional_directories: ["/data", "/srv/b"] }));
    expect(removeButtons().map((b) => b.disabled)).toEqual([true, true]);
    settle(answer({ additional_directories: ["/data", "/srv/a"] }));
    await flush();
  });

  it("marks a stored entry the server refuses now and shows its reason", () => {
    withRefusedGone();
    expect(chipLabels()).toEqual(["/data", "/gone"]);
    expect(chipNotes()).toEqual([null, "not granted"]);
    expect(refusalReasons()).toEqual([GONE_REASON]);
    expect(byId("agent-dirs-refused").classList.contains("hidden")).toBe(false);
  });

  it("drops the mark and the reason once the server grants the entry again", () => {
    withRefusedGone();
    applyAgentDirs(settingsPayload({ additional_directories: ["/data", "/gone"] }));
    expect(chipNotes()).toEqual([null, null]);
    expect(refusalReasons()).toEqual([]);
    expect(byId("agent-dirs-refused").classList.contains("hidden")).toBe(true);
  });

  it("neither links nor routes a refused entry's paths", () => {
    withRefusedGone();
    expect(additionalDirectories()).toEqual(["/data"]);
    expect(servedFileRoute("/gone/plan.md")).toBeNull();
    expect(servedFileRoute("/data/plan.md")).toEqual({ kind: "file", path: "/data/plan.md" });
  });

  it("keeps the server's verdict on the entries a save kept", async () => {
    withRefusedGone();
    accept({
      additional_directories: ["/gone"],
      additional_directories_refused: { "/gone": GONE_REASON },
    });
    byId("agent-dirs-chips").querySelector<HTMLButtonElement>(".chip-remove")?.click();
    await flush();
    expect(mocks.dispatch).toHaveBeenCalledWith({ dirs: ["/gone"] });
    expect(chipLabels()).toEqual(["/gone"]);
    expect(chipNotes()).toEqual(["not granted"]);
    expect(refusalReasons()).toEqual([GONE_REASON]);
  });

  it("shows a list another device saved during a write, then the list the write committed", async () => {
    const settle = holdSave();
    add("/srv/a");
    applyAgentDirs(settingsPayload({ additional_directories: ["/data", "/srv/b"] }));
    expect(chipLabels()).toEqual(["/data", "/srv/b"]);
    expect(additionalDirectories()).toEqual(["/data", "/srv/b"]);
    settle(answer({ additional_directories: ["/data", "/srv/a"] }));
    await flush();
    expect(chipLabels()).toEqual(["/data", "/srv/a"]);
    expect(additionalDirectories()).toEqual(["/data", "/srv/a"]);
  });

  it("follows the granted entries even while a write is in flight", async () => {
    const settle = holdSave();
    add("/srv/a");
    withRefusedGone();
    expect(additionalDirectories()).toEqual(["/data"]);
    settle(answer({ additional_directories: ["/data", "/srv/a"] }));
    await flush();
  });
});
