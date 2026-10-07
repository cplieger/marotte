// Tests for settings.ts's diagnostics surface: the pure version extractor and the initDiagnostics
// DOM flow (copyable textarea + version row + Copy button + clipboard fallback), plus the
// chat-retention row's Keep-forever reveal. settings.ts's many feature-module imports are stubbed
// so the module loads in isolation; only runDiagnostics + bindLoadingState are given behaviour.
// A mock export set to `undefined` exists because Browser Mode links ESM for real, so every name
// the graph imports must be present; `undefined` is what the node runner gave it.

import { vi, describe, it, expect, beforeEach } from "vitest";
import { settingsPayload } from "./__test-helpers__/settings.js";

const H = vi.hoisted(() => ({
  mockRun: vi.fn(),
  mockBind: vi.fn(),
  mockCleanup: vi.fn(),
  mockApplyTheme: vi.fn(),
  mockKiroDispatch: vi.fn(),
  mockInitGitBadge: vi.fn(),
  mockInitGitPanel: vi.fn(),
  mockOpenConfigFile: vi.fn(() => Promise.resolve()),
}));

vi.mock("./actions/tools.js", () => ({
  runDiagnostics: { dispatch: (...a: unknown[]) => H.mockRun(...a) },
}));
vi.mock("./actions/index.js", () => ({
  bindLoadingState: (...a: unknown[]) => H.mockBind(...a),
  registerCleanup: (...a: unknown[]) => H.mockCleanup(...a),
  debouncedDispatch: vi.fn(() =>
    Object.assign(() => undefined, { isPending: () => false, flush: vi.fn() }),
  ),
  subscribeByName: vi.fn(() => () => undefined),
  // retention.js defines an action at module scope. Browser Mode links this factory for real, so a
  // name anywhere in the graph has to exist on it.
  defineAction: vi.fn(() => ({ dispatch: vi.fn() })),
  retryNetwork: vi.fn(),
}));
vi.mock("./actions/settings.js", () => ({
  saveSteering: {},
  logout: {},
  setKiroSetting: { dispatch: (...a: unknown[]) => H.mockKiroDispatch(...a) },
}));
vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(() => Promise.resolve(undefined)),
  apiGetTyped: vi.fn(),
  // settings-steering.ts is in this graph and reads the ETag off the response headers, so the name
  // has to exist for Browser Mode's real linking.
  apiGetWithHeaders: vi.fn(() => Promise.resolve({ data: null, headers: null })),
}));
vi.mock("./wire/decoders.gen.js", () => ({ decodeWhoamiResponse: vi.fn() }));
// governance.ts reaches the SSE bus and its decoders, which the partial decoder mock above does not
// carry; the lock painter has its own suite.
vi.mock("./governance.js", () => ({
  paintSettingLocks: vi.fn(),
  writeSwitch: (input: HTMLInputElement, value: boolean) => {
    input.checked = value;
  },
}));
vi.mock("./save-indicator.js", () => ({
  showSaving: vi.fn(),
  showSaved: vi.fn(),
  showError: vi.fn(),
  STEERING_SAVE_KEY: "steering",
}));
vi.mock("./persist.js", () => ({
  loadSettings: vi.fn(),
  patchSettings: vi.fn(),
  initSettingsTracking: vi.fn(),
}));
// Feature modules settings.ts wires in initUI — inert stubs so the import graph loads without side
// effects (initUI is never called here).
vi.mock("./modals.js", () => ({
  initAllModals: undefined,
}));
vi.mock("./tabs.js", () => ({
  toggleSettingsView: undefined,
  toggleGitView: undefined,
}));
// The badge, not the git view: `initPostAuthUI` wires only this at boot now, and the real module
// reaches git-status-store.ts, whose `apiAction` import this file's partial `./actions/index.js`
// factory does not provide.
vi.mock("./git-badge.js", () => ({ initGitBadge: H.mockInitGitBadge }));
// The git VIEW.
vi.mock("./git.js", () => ({ initGitPanel: H.mockInitGitPanel, loadGitRepos: vi.fn() }));
vi.mock("./versions.js", () => ({
  loadVersions: vi.fn(),
  getVersions: () => ({ marotte: "", kiroCli: "" }),
}));
vi.mock("./editor-openers.js", () => ({ openConfigFile: H.mockOpenConfigFile }));
vi.mock("./git-tabs.js", () => ({
  getGitTab: undefined,
}));
vi.mock("./files.js", () => ({
  noteDefaultBrowsePath: undefined,
}));
vi.mock("./editor-core.js", () => ({
  restoreEditorTabs: undefined,
}));
vi.mock("./tools.js", () => ({
  loadToolsList: undefined,
  initTools: undefined,
}));
vi.mock("./notify.js", () => ({
  restoreNotifications: undefined,
}));
vi.mock("./theme.js", () => ({
  initThemeToggle: undefined,
  applyThemeChoice: H.mockApplyTheme,
}));
vi.mock("./settings-tabs.js", () => ({
  initSettingsTabs: undefined,
}));
vi.mock("./permissions-ui.js", () => ({
  loadNativePolicy: undefined,
  initNativePolicyUI: undefined,
  initPermissionsUI: undefined,
}));
vi.mock("./mcp-ui.js", () => ({
  initMCP: undefined,
}));
vi.mock("./knowledge.js", () => ({
  initKnowledge: undefined,
  loadKnowledge: undefined,
}));
vi.mock("./settings-notifications.js", () => ({
  initNotificationToggles: undefined,
}));

const {
  adoptThemeFromSettings,
  applyGeneralPanel,
  extractDiagnosticVersion,
  initDiagnostics,
  initExperimentalToggles,
  initGeneralPanelControls,
  initPostAuthUI,
  shellTimeoutMs,
  themeStorage,
  _resetThemeForTest,
} = await import("./settings.js");

/** Wire the General panel's listeners once, then seed it — the two halves the `settings_updated`
 *  arm keeps apart, in the order boot runs them. */
function initRetention(s: Parameters<typeof applyGeneralPanel>[0]): void {
  initGeneralPanelControls();
  applyGeneralPanel(s);
}

async function flush(): Promise<void> {
  for (let i = 0; i < 6; i++) {
    await Promise.resolve();
  }
}

function seedDom(): void {
  document.body.innerHTML = `
    <div class="section-option">
      <button id="diagnostics-run" class="btn">Run diagnostics</button>
      <p id="diagnostics-status" class="section-hint" hidden></p>
    </div>`;
}

function stubClipboard(writeText: () => Promise<void>): void {
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
}

function clickRun(): void {
  document.getElementById("diagnostics-run")?.dispatchEvent(new MouseEvent("click"));
}

beforeEach(() => {
  vi.clearAllMocks();
  seedDom();
});

describe("extractDiagnosticVersion", () => {
  it("reads q-details.version (the Amazon-Q-derived schema)", () => {
    expect(extractDiagnosticVersion('{"q-details":{"version":"2.12.0"}}')).toBe("2.12.0");
  });

  it("falls back to a top-level version field", () => {
    expect(extractDiagnosticVersion('{"version":"1.2.3"}')).toBe("1.2.3");
  });

  it("reads a top-level kiro_cli string", () => {
    expect(extractDiagnosticVersion('{"kiro_cli":"2.12.0"}')).toBe("2.12.0");
  });

  it("returns '' for a non-JSON report", () => {
    expect(extractDiagnosticVersion("signal: killed")).toBe("");
  });

  it("returns '' when no recognisable version is present", () => {
    expect(extractDiagnosticVersion('{"system":{"os":"linux"}}')).toBe("");
  });
});

describe("initDiagnostics", () => {
  it("renders the full report into a copyable textarea + version row", async () => {
    const report = '{"q-details":{"version":"2.12.0"}}';
    H.mockRun.mockResolvedValue({ report });
    const writeText = vi.fn(() => Promise.resolve());
    stubClipboard(writeText);

    initDiagnostics();
    clickRun();
    await flush();

    const result = document.querySelector<HTMLTextAreaElement>(".diagnostics-result");
    expect(result).not.toBeNull();
    expect(result?.value).toBe(report);
    expect(result?.hidden).toBe(false);
    expect(result?.readOnly).toBe(true);

    const version = document.querySelector<HTMLElement>(".diagnostics-version");
    expect(version?.hidden).toBe(false);
    expect(version?.textContent).toBe("kiro-cli 2.12.0");

    const status = document.getElementById("diagnostics-status");
    expect(status?.getAttribute("aria-live")).toBe("polite");
    expect(status?.textContent).toContain("Report ready");
    expect(writeText).toHaveBeenCalledWith(report);
  });

  it("keeps the report available when the clipboard is blocked", async () => {
    const report = "plain diagnostics text";
    H.mockRun.mockResolvedValue({ report });
    stubClipboard(() => Promise.reject(new Error("blocked")));

    initDiagnostics();
    clickRun();
    await flush();

    expect(document.querySelector<HTMLTextAreaElement>(".diagnostics-result")?.value).toBe(report);
    expect(document.querySelector<HTMLTextAreaElement>(".diagnostics-result")?.hidden).toBe(false);
    // No version row for a non-JSON report.
    expect(document.querySelector<HTMLElement>(".diagnostics-version")?.hidden).toBe(true);
    expect(document.getElementById("diagnostics-status")?.textContent).toContain(
      "Copy it from the box below",
    );
  });

  it("surfaces an error status and hides the result on failure", async () => {
    H.mockRun.mockResolvedValue({ error: "diagnostic command failed" });
    stubClipboard(() => Promise.resolve());

    initDiagnostics();
    clickRun();
    await flush();

    expect(document.getElementById("diagnostics-status")?.textContent).toBe(
      "diagnostic command failed",
    );
    expect(document.querySelector<HTMLTextAreaElement>(".diagnostics-result")?.hidden).toBe(true);
  });

  it("copies the report via the Copy button", async () => {
    const report = "some report";
    H.mockRun.mockResolvedValue({ report });
    const writeText = vi.fn(() => Promise.resolve());
    stubClipboard(writeText);

    initDiagnostics();
    clickRun();
    await flush();
    writeText.mockClear();

    document
      .querySelector<HTMLButtonElement>(".diagnostics-copy")
      ?.dispatchEvent(new MouseEvent("click"));
    await flush();
    expect(writeText).toHaveBeenCalledWith(report);
    expect(document.getElementById("diagnostics-status")?.textContent).toBe(
      "Copied report to clipboard.",
    );
  });

  // The Copy control takes the run button's own slot, so hiding goes through the `.hidden` utility
  // for both: `.btn`/`.btn-small` each declare `display`, and an author-origin `display` beats the
  // UA's `[hidden]` rule at any specificity.
  it("offers no Copy control until a report exists", () => {
    initDiagnostics();

    const run = document.getElementById("diagnostics-run");
    const copy = document.querySelector<HTMLButtonElement>(".diagnostics-copy");
    expect(copy).not.toBeNull();
    expect(copy?.classList.contains("hidden")).toBe(true);
    expect(run?.classList.contains("hidden")).toBe(false);
    // One slot, so the two controls are siblings in the run row.
    expect(copy?.parentElement).toBe(run?.parentElement);
  });

  it("swaps Copy into the run button's slot once the report lands", async () => {
    H.mockRun.mockResolvedValue({ report: "some report" });
    stubClipboard(() => Promise.resolve());

    initDiagnostics();
    clickRun();
    await flush();

    expect(
      document.querySelector<HTMLButtonElement>(".diagnostics-copy")?.classList.contains("hidden"),
    ).toBe(false);
    expect(document.getElementById("diagnostics-run")?.classList.contains("hidden")).toBe(true);
  });

  it("returns the run button after the slot expires, keeping the report", async () => {
    vi.useFakeTimers();
    try {
      H.mockRun.mockResolvedValue({ report: "some report" });
      stubClipboard(() => Promise.resolve());

      initDiagnostics();
      clickRun();
      await flush();

      vi.advanceTimersByTime(15 * 60 * 1000);

      expect(document.getElementById("diagnostics-run")?.classList.contains("hidden")).toBe(false);
      expect(
        document
          .querySelector<HTMLButtonElement>(".diagnostics-copy")
          ?.classList.contains("hidden"),
      ).toBe(true);
      // The textarea is the report's only store, so the timer never drops it.
      const result = document.querySelector<HTMLTextAreaElement>(".diagnostics-result");
      expect(result?.hidden).toBe(false);
      expect(result?.value).toBe("some report");
    } finally {
      vi.useRealTimers();
    }
  });

  it("releases the slot timer on unload", async () => {
    H.mockRun.mockResolvedValue({ report: "some report" });
    stubClipboard(() => Promise.resolve());

    initDiagnostics();
    clickRun();
    await flush();

    const release = H.mockCleanup.mock.calls.at(-1)?.[0] as (() => void) | undefined;
    expect(typeof release).toBe("function");
    expect(() => release?.()).not.toThrow();
  });
});

// Keep forever means -1, which has no day count, so the Days-kept field is HIDDEN rather than
// disabled — a greyed-out field still showing the last number reads as the value in force. The
// number survives in the DOM, so unchecking restores it instead of falling back to a default.
describe("the chat-retention row", () => {
  function seedRetentionDom(): void {
    document.body.innerHTML = `
      <div class="section-option">
        <label class="toggle">
          <input type="checkbox" id="chat-retention-forever">
          <span class="toggle-slider"></span>
        </label>
        <div><span class="section-option-label">Keep forever</span></div>
      </div>
      <div class="section-option section-option-fixed" id="chat-retention-days-row">
        <div class="section-field">
          <label for="chat-retention-days" class="section-option-label">Days kept</label>
          <input type="number" id="chat-retention-days" class="rf-input rf-number" min="0" max="90" value="1">
        </div>
      </div>`;
  }

  function daysRow(): HTMLElement {
    return document.getElementById("chat-retention-days-row")!;
  }

  function daysInput(): HTMLInputElement {
    return document.getElementById("chat-retention-days") as HTMLInputElement;
  }

  function foreverInput(): HTMLInputElement {
    return document.getElementById("chat-retention-forever") as HTMLInputElement;
  }

  function toggleForever(checked: boolean): void {
    foreverInput().checked = checked;
    foreverInput().dispatchEvent(new Event("change"));
  }

  beforeEach(() => {
    seedRetentionDom();
  });

  it("shows the Days-kept row for a day count", () => {
    initRetention(settingsPayload({ chat_retention_days: 14 }));

    expect(foreverInput().checked).toBe(false);
    expect(daysRow().classList.contains("hidden")).toBe(false);
    expect(daysInput().value).toBe("14");
  });

  it("hides the Days-kept row when the stored value is forever", () => {
    initRetention(settingsPayload({ chat_retention_days: -1 }));

    expect(foreverInput().checked).toBe(true);
    expect(daysRow().classList.contains("hidden")).toBe(true);
    // Hidden, never disabled: a disabled field greys out the last number and presents it as the
    // value in force.
    expect(daysInput().disabled).toBe(false);
  });

  // The seed half runs again on every `settings_updated`, which is what makes a retention window
  // chosen on another device reach THIS screen's controls rather than only its behaviour.
  it("follows a remote change and still writes once per click", async () => {
    const { patchSettings } = await import("./persist.js");
    initRetention(settingsPayload({ chat_retention_days: 14 }));

    applyGeneralPanel(settingsPayload({ chat_retention_days: -1 }));
    expect(foreverInput().checked).toBe(true);
    expect(daysRow().classList.contains("hidden")).toBe(true);

    toggleForever(false);
    expect(patchSettings).toHaveBeenCalledTimes(1);
  });

  it("does not rewrite the day box while the reader is in it", () => {
    initRetention(settingsPayload({ chat_retention_days: 14 }));
    daysInput().focus();
    daysInput().value = "3";

    applyGeneralPanel(settingsPayload({ chat_retention_days: 30 }));

    expect(daysInput().value, "a half-typed number survives an unrelated remote change").toBe("3");
  });

  it("hides the row on check and restores it, with its number, on uncheck", async () => {
    const { patchSettings } = await import("./persist.js");
    initRetention(settingsPayload({ chat_retention_days: 30 }));

    toggleForever(true);
    expect(daysRow().classList.contains("hidden")).toBe(true);
    expect(patchSettings).toHaveBeenLastCalledWith({ chat_retention_days: -1 }, expect.anything());

    toggleForever(false);
    expect(daysRow().classList.contains("hidden")).toBe(false);
    expect(daysInput().value).toBe("30");
    expect(patchSettings).toHaveBeenLastCalledWith({ chat_retention_days: 30 }, expect.anything());
  });
});

describe("the config.json door in Settings > General", () => {
  it("opens config.json from the config directory", () => {
    document.body.innerHTML = `<button type="button" id="settings-open-config">config.json</button>`;
    initGeneralPanelControls();

    document.getElementById("settings-open-config")?.click();

    expect(H.mockOpenConfigFile.mock.calls).toEqual([["config.json"]]);
  });
});

// The theme's authority is a config.json key; the localStorage blob holds a pre-paint CACHE of it.
// This is the policy joining the two, and each case here is a way that policy can be wrong in a
// manner a reader would notice:

describe("the theme, between config.json and its paint cache", () => {
  beforeEach(async () => {
    localStorage.clear();
    _resetThemeForTest();
    // The real controller's set() calls back through the storage adapter, and that call is the ONLY
    // path to the write-back guard. A mock that merely records the call would make every case below
    // vacuous — measured: with the guard deleted the suite stayed green until this line existed.
    H.mockApplyTheme.mockReset();
    H.mockApplyTheme.mockImplementation((choice: unknown) => {
      themeStorage.set(choice as string);
    });
    const { patchSettings } = await import("./persist.js");
    vi.mocked(patchSettings).mockClear();
  });

  it("adopts the server's theme and refreshes the cache", async () => {
    const { cachedTheme } = await import("./device-view.js");
    adoptThemeFromSettings(settingsPayload({ theme: "light" }));

    expect(cachedTheme()).toBe("light");
    expect(H.mockApplyTheme).toHaveBeenCalledWith("light");
  });

  it("does not write an adopted value back, which is what would loop", async () => {
    const { patchSettings } = await import("./persist.js");
    adoptThemeFromSettings(settingsPayload({ theme: "dark" }));
    expect(patchSettings).not.toHaveBeenCalled();
  });

  it("carries the cache across ONCE when the server has no theme", async () => {
    const { cacheTheme } = await import("./device-view.js");
    const { patchSettings } = await import("./persist.js");
    cacheTheme("light");

    adoptThemeFromSettings(settingsPayload({}));

    // Written through, so the value stops being cache-only and starts travelling to every other
    // device — which is exactly what it could not do before.
    expect(patchSettings).toHaveBeenCalledWith({ theme: "light" });
  });

  it("carries nothing across when the cache is empty too", async () => {
    const { patchSettings } = await import("./persist.js");
    adoptThemeFromSettings(settingsPayload({}));
    expect(patchSettings).not.toHaveBeenCalled();
  });

  it("does not re-adopt the cache on a LATER payload with no theme", async () => {
    const { cacheTheme } = await import("./device-view.js");
    const { patchSettings } = await import("./persist.js");
    // The reader chose a theme, then cleared it. A second adoption of the cache would bring the
    // cleared value back, which is the difference between a cache and a migration path.
    adoptThemeFromSettings(settingsPayload({ theme: "dark" }));
    vi.mocked(patchSettings).mockClear();
    cacheTheme("dark");

    adoptThemeFromSettings(settingsPayload({}));

    expect(patchSettings).not.toHaveBeenCalled();
  });

  it("repaints when another device's choice arrives, and only when it differs", () => {
    adoptThemeFromSettings(settingsPayload({ theme: "dark" }));
    expect(H.mockApplyTheme).toHaveBeenCalledTimes(1);

    // The writer's own echo. Repainting anyway would be a second theme transition for a change this
    // device made itself.
    adoptThemeFromSettings(settingsPayload({ theme: "dark" }));
    expect(H.mockApplyTheme).toHaveBeenCalledTimes(1);

    adoptThemeFromSettings(settingsPayload({ theme: "light" }));
    expect(H.mockApplyTheme).toHaveBeenLastCalledWith("light");
  });

  it("ignores a value that is not one of the three choices", async () => {
    const { cachedTheme } = await import("./device-view.js");
    adoptThemeFromSettings(settingsPayload({ theme: "chartreuse" }));
    expect(cachedTheme()).toBeNull();
    expect(H.mockApplyTheme).not.toHaveBeenCalled();
  });
});

// The experimental flags each report against their OWN key.

describe("initExperimentalToggles", () => {
  /** Resolvers for the dispatches made so far, in order. */
  let answer: ((r: unknown) => void)[];

  function seedFlagsDom(): void {
    document.body.innerHTML = `
      <input type="checkbox" id="flag-hooks-status">
      <input type="checkbox" id="flag-telemetry">
      <input type="checkbox" id="flag-disable-inherit-resources">`;
  }

  function box(id: string): HTMLInputElement {
    return document.getElementById(id) as HTMLInputElement;
  }

  function toggle(id: string, checked: boolean): void {
    box(id).checked = checked;
    box(id).dispatchEvent(new Event("change"));
  }

  /** Wire the toggles and wait out the initial read, so a late arrival of it cannot land between
   *  a test's own toggle and its assertion. The default mock answers no value for any key, and
   *  `hooks.showStatus` is the one row whose own default is ON, so it is what says the read has
   *  landed. */
  async function initFlags(): Promise<void> {
    initExperimentalToggles();
    await vi.waitFor(() => {
      expect(box("flag-hooks-status").checked).toBe(true);
    });
  }

  beforeEach(() => {
    answer = [];
    H.mockKiroDispatch.mockImplementation(
      () =>
        new Promise((resolve) => {
          answer.push(resolve);
        }),
    );
    seedFlagsDom();
  });

  it("spins the flag's own key when it is flipped", async () => {
    const { showSaving } = await import("./save-indicator.js");
    await initFlags();

    toggle("flag-telemetry", false);

    expect(showSaving).toHaveBeenCalledExactlyOnceWith("telemetry.enabled");
  });

  it("reports each flag against its own key when an earlier write answers late", async () => {
    const { showSaved } = await import("./save-indicator.js");
    await initFlags();

    toggle("flag-telemetry", false);
    toggle("flag-hooks-status", false);
    expect(H.mockKiroDispatch).toHaveBeenCalledTimes(2);

    // The first flag answers while the second is still outstanding.
    answer[0]?.({});
    await vi.waitFor(() => {
      expect(showSaved).toHaveBeenCalledWith("telemetry.enabled");
    });

    answer[1]?.({});
    await vi.waitFor(() => {
      expect(showSaved).toHaveBeenCalledWith("hooks.showStatus");
    });
    expect(showSaved).toHaveBeenCalledTimes(2);
  });

  it("does not report a flag a newer write of the same flag has overtaken", async () => {
    const { showSaved } = await import("./save-indicator.js");
    await initFlags();

    toggle("flag-telemetry", false);
    toggle("flag-telemetry", true);
    expect(H.mockKiroDispatch).toHaveBeenCalledTimes(2);

    // Both answer; only the newer one owns the slot, so a broken guard shows up as a second call
    // rather than as a missing one.
    answer[0]?.({});
    answer[1]?.({});
    await vi.waitFor(() => {
      expect(showSaved).toHaveBeenCalled();
    });
    expect(showSaved).toHaveBeenCalledExactlyOnceWith("telemetry.enabled");
  });

  it("names the flag's own key when its write fails", async () => {
    const { showError } = await import("./save-indicator.js");
    await initFlags();

    toggle("flag-disable-inherit-resources", true);
    answer[0]?.(null);

    await vi.waitFor(() => {
      expect(showError).toHaveBeenCalledExactlyOnceWith("chat.disableInheritingDefaultResources");
    });
  });

  // ONE request for every flag, naming them all.
  it("reads every flag in one request", async () => {
    const { apiGet } = await import("./api-client.js");
    await initFlags();

    expect(apiGet).toHaveBeenCalledTimes(1);
    const url = vi.mocked(apiGet).mock.calls[0]?.[0] ?? "";
    const asked = new URL(url, "http://localhost").searchParams.get("keys")?.split(",") ?? [];
    expect(asked).toEqual([
      "hooks.showStatus",
      "telemetry.enabled",
      "chat.disableInheritingDefaultResources",
    ]);
  });

  // The answer is read BY KEY, not by position: the server sorts the document it returns, so a
  // reader that trusted request order would flip two checkboxes.
  it("adopts each flag's value by key rather than by response order", async () => {
    const { apiGet } = await import("./api-client.js");
    vi.mocked(apiGet).mockResolvedValueOnce({
      settings: {
        "chat.disableInheritingDefaultResources": "true",
        "hooks.showStatus": "false",
        "telemetry.enabled": "false",
      },
    });

    initExperimentalToggles();

    await vi.waitFor(() => {
      expect(box("flag-hooks-status").checked).toBe(false);
    });
    expect(box("flag-telemetry").checked).toBe(false);
    expect(box("flag-disable-inherit-resources").checked).toBe(true);
  });

  // The endpoint answers "" for a key `cli.json` does not carry AND for a read it could not make,
  // and the three rows do not share a polarity — so one blanket unset-means-on rule claimed
  // telemetry was on while it was off, and claimed default-resource inheritance was disabled while
  // it was not.
  it("renders an absent key at that row's own default, not at one shared rule", async () => {
    const { apiGet } = await import("./api-client.js");
    vi.mocked(apiGet).mockResolvedValueOnce({ settings: {} });

    initExperimentalToggles();

    await vi.waitFor(() => {
      expect(box("flag-hooks-status").checked).toBe(true);
    });
    expect(box("flag-telemetry").checked).toBe(false);
    expect(box("flag-disable-inherit-resources").checked).toBe(false);
  });

  // The loader runs on every General-tab activation. Each listener it left behind meant another
  // identical PUT per click, and each of those is a `kiro-cli settings` SPAWN on the server.
  it("leaves one listener per checkbox however many times the panel is opened", async () => {
    await initFlags();
    await initFlags();
    await initFlags();

    toggle("flag-telemetry", false);

    expect(H.mockKiroDispatch).toHaveBeenCalledTimes(1);
  });

  it("discards a superseded read rather than painting it over a newer one", async () => {
    const { apiGet } = await import("./api-client.js");
    let releaseFirst = (): void => {
      /* replaced below */
    };
    const first = new Promise<unknown>((resolve) => {
      releaseFirst = () => {
        resolve({ settings: { "telemetry.enabled": "false" } });
      };
    });
    vi.mocked(apiGet).mockReturnValueOnce(first as Promise<never>);
    vi.mocked(apiGet).mockResolvedValueOnce({ settings: { "telemetry.enabled": "true" } });

    initExperimentalToggles();
    initExperimentalToggles();
    await vi.waitFor(() => {
      expect(box("flag-telemetry").checked).toBe(true);
    });

    releaseFirst();
    await first;
    await new Promise((r) => setTimeout(r, 0));

    expect(box("flag-telemetry").checked).toBe(true);
  });
});

// The post-auth door, and what it may NOT reach.

describe("initPostAuthUI", () => {
  it("wires the sidebar badge and not the git view", async () => {
    // The badge is boot-visible chrome in the toolbar, so the one `status-all` scan its
    // subscription starts is a read for something on screen.
    initPostAuthUI();

    expect(H.mockInitGitBadge).toHaveBeenCalledTimes(1);
    expect(H.mockInitGitPanel).not.toHaveBeenCalled();
  });
});

// NOT TESTED, and the absence is deliberate rather than an oversight: that `initUI` issues no `GET
// /api/steering`.

describe("the payload-link guard switch", () => {
  const box = (): HTMLInputElement =>
    document.getElementById("flag-guard-payload-links") as HTMLInputElement;

  beforeEach(() => {
    document.body.innerHTML = `<input type="checkbox" id="flag-guard-payload-links">`;
  });

  it.each([true, false])("seeds the switch from guard_payload_links = %s", (on) => {
    box().checked = !on;
    applyGeneralPanel(settingsPayload({ guard_payload_links: on }));
    expect(box().checked).toBe(on);
  });

  it("writes guard_payload_links when the reader flips it", async () => {
    const { patchSettings } = await import("./persist.js");
    initRetention(settingsPayload({ guard_payload_links: true }));
    box().checked = false;
    box().dispatchEvent(new Event("change"));
    expect(patchSettings).toHaveBeenCalledExactlyOnceWith({ guard_payload_links: false }, box());
  });
});

describe("the wait-for-MCP-servers switch", () => {
  const box = (): HTMLInputElement =>
    document.getElementById("mcp-wait-for-ready") as HTMLInputElement;

  beforeEach(() => {
    document.body.innerHTML = `<input type="checkbox" id="mcp-wait-for-ready">`;
  });

  it.each([true, false])("seeds the switch from mcp_wait_for_ready = %s", (on) => {
    box().checked = !on;
    applyGeneralPanel(settingsPayload({ mcp_wait_for_ready: on }));
    expect(box().checked).toBe(on);
  });

  it("writes mcp_wait_for_ready when the reader flips it", async () => {
    const { patchSettings } = await import("./persist.js");
    initRetention(settingsPayload({ mcp_wait_for_ready: false }));
    box().checked = true;
    box().dispatchEvent(new Event("change"));
    expect(patchSettings).toHaveBeenCalledExactlyOnceWith({ mcp_wait_for_ready: true }, box());
  });
});

describe("the automatic-compaction switch and slider", () => {
  const toggle = (): HTMLInputElement =>
    document.getElementById("flag-auto-compaction") as HTMLInputElement;
  const range = (): HTMLInputElement =>
    document.getElementById("auto-compact-pct") as HTMLInputElement;
  const row = (): HTMLElement => document.getElementById("auto-compact-row") as HTMLElement;
  const warning = (): HTMLElement => document.getElementById("auto-compact-warning") as HTMLElement;

  beforeEach(() => {
    document.body.innerHTML = `
      <input type="checkbox" id="flag-auto-compaction">
      <div id="auto-compact-row">
        <output id="auto-compact-pct-value"></output>
        <input type="range" id="auto-compact-pct" min="50" max="90" step="5">
        <p id="auto-compact-warning"></p>
      </div>`;
  });

  it("hides the slider while the switch is off and publishes the policy", async () => {
    const { compactionPolicy } = await import("./context-ring.js");
    applyGeneralPanel(settingsPayload({ auto_compaction_enabled: false, auto_compact_pct: 70 }));
    expect(toggle().checked).toBe(false);
    expect(row().classList.contains("hidden")).toBe(true);
    expect(compactionPolicy.value).toEqual({ enabled: false, pct: 70 });
  });

  it.each([
    [80, true],
    [85, false],
  ])("at %i the warning hidden is %s", (pct, hidden) => {
    applyGeneralPanel(settingsPayload({ auto_compaction_enabled: true, auto_compact_pct: pct }));
    expect(row().classList.contains("hidden")).toBe(false);
    expect(range().value).toBe(String(pct));
    expect(warning().classList.contains("hidden")).toBe(hidden);
  });

  it("writes the value on change and not while dragging", async () => {
    const { patchSettings } = await import("./persist.js");
    const { compactionPolicy } = await import("./context-ring.js");
    initRetention(settingsPayload({ auto_compaction_enabled: true, auto_compact_pct: 80 }));
    vi.mocked(patchSettings).mockClear();
    range().value = "65";
    range().dispatchEvent(new Event("input"));
    expect(patchSettings).not.toHaveBeenCalled();
    expect(compactionPolicy.value).toEqual({ enabled: true, pct: 65 });
    range().dispatchEvent(new Event("change"));
    expect(patchSettings).toHaveBeenCalledExactlyOnceWith({ auto_compact_pct: 65 }, range());
  });

  it("writes the switch and hides the slider when the reader turns it off", async () => {
    const { patchSettings } = await import("./persist.js");
    initRetention(settingsPayload({ auto_compaction_enabled: true, auto_compact_pct: 80 }));
    vi.mocked(patchSettings).mockClear();
    toggle().checked = false;
    toggle().dispatchEvent(new Event("change"));
    expect(patchSettings).toHaveBeenCalledExactlyOnceWith(
      { auto_compaction_enabled: false },
      toggle(),
    );
    expect(row().classList.contains("hidden")).toBe(true);
  });
});

describe("the agent-capability selects and the shell timeout", () => {
  const sel = (id: string): HTMLSelectElement => document.getElementById(id) as HTMLSelectElement;
  const box = (id: string): HTMLInputElement => document.getElementById(id) as HTMLInputElement;
  const timeout = (): HTMLInputElement =>
    document.getElementById("shell-command-timeout") as HTMLInputElement;
  const askRow = (): HTMLElement => document.getElementById("spec-planning-ask-row") as HTMLElement;

  beforeEach(() => {
    document.body.innerHTML = `
      <select id="spec-planning"><option value="off">Off</option><option value="quick">Quick</option><option value="full">Full</option></select>
      <div id="spec-planning-ask-row" class="hidden"><input type="checkbox" id="flag-spec-ask-first"></div>
      <input type="checkbox" id="flag-work-validation">
      <input type="checkbox" id="flag-cloudformation-safety">
      <select id="output-style"><option value="default">Default</option><option value="concise">Concise</option></select>
      <input type="number" id="shell-command-timeout">`;
  });

  it("seeds every select and shows the ask-first row only while spec planning is on", () => {
    applyGeneralPanel(
      settingsPayload({
        spec_planning: "quick",
        output_style: "concise",
        terminal_command_timeout_ms: 300_000,
      }),
    );
    expect(sel("spec-planning").value).toBe("quick");
    expect(sel("output-style").value).toBe("concise");
    expect(timeout().value).toBe("300");
    expect(askRow().classList.contains("hidden")).toBe(false);
  });

  it("leaves the timeout box empty when no timeout is set", () => {
    applyGeneralPanel(settingsPayload({ terminal_command_timeout_ms: 0 }));
    expect(timeout().value).toBe("");
    expect(askRow().classList.contains("hidden")).toBe(true);
  });

  it.each([
    ["an unset", "", "unchecked"],
    ["an off", "off", "unchecked"],
    ["an on", "on", "checked"],
  ] as const)("renders %s feature switch %s", (_name, value, shown) => {
    applyGeneralPanel(
      settingsPayload({ work_validation: value, cloudformation_safety_check: value }),
    );
    expect(box("flag-work-validation").checked).toBe(shown === "checked");
    expect(box("flag-cloudformation-safety").checked).toBe(shown === "checked");
  });

  it("stores a flipped feature switch as on or off, never unset", async () => {
    const { patchSettings } = await import("./persist.js");
    initRetention(settingsPayload());
    vi.mocked(patchSettings).mockClear();
    box("flag-work-validation").checked = true;
    box("flag-work-validation").dispatchEvent(new Event("change"));
    expect(patchSettings).toHaveBeenLastCalledWith(
      { work_validation: "on" },
      box("flag-work-validation"),
    );
    box("flag-cloudformation-safety").checked = false;
    box("flag-cloudformation-safety").dispatchEvent(new Event("change"));
    expect(patchSettings).toHaveBeenLastCalledWith(
      { cloudformation_safety_check: "off" },
      box("flag-cloudformation-safety"),
    );
  });

  it("writes seconds as milliseconds and clears the box on an empty entry", async () => {
    const { patchSettings } = await import("./persist.js");
    initRetention(settingsPayload());
    vi.mocked(patchSettings).mockClear();
    timeout().value = "45";
    timeout().dispatchEvent(new Event("change"));
    expect(patchSettings).toHaveBeenLastCalledWith(
      { terminal_command_timeout_ms: 45_000 },
      timeout(),
    );
    timeout().value = "";
    timeout().dispatchEvent(new Event("change"));
    expect(patchSettings).toHaveBeenLastCalledWith({ terminal_command_timeout_ms: 0 }, timeout());
  });

  it.each([
    ["", 0],
    ["0", 0],
    ["-5", 0],
    ["abc", 0],
    ["1", 1000],
    ["1800", 1_800_000],
    ["5000", 1_800_000],
  ])("shellTimeoutMs(%j) is %i", (raw, want) => {
    expect(shellTimeoutMs(raw)).toBe(want);
  });
});
