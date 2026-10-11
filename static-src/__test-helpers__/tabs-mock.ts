// A COMPLETE, inert `tabs.js` mock. Browser Mode links ESM for real, so every name any module in
// the graph reaches must exist; a partial factory breaks without naming what is missing. Spread it:
//   vi.mock("./tabs.js", async () => ({
//     ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
//     activateTab: mockActivateTab,
//   }));
// `tabs-mock.test.ts` fails until a new `tabs.ts` export is added here.

import { vi } from "vitest";

/** Every value `tabs.ts` exports, inert. Readers return the empty answer for their type (a mock
 *  claiming an open tab passes a test for a reason production did not supply); async mutators
 *  resolve, since a caller awaiting one exercises its own continuation. */
export function tabsMock(): Record<string, unknown> {
  return {
    // `openTab` answers its success outcome, so callers stay off their failure branches.
    openTab: vi.fn(async () => "opened"),
    closeTab: vi.fn(async () => {}),
    setTabPinned: vi.fn(async () => {}),
    setTabParent: vi.fn(async () => false),
    openEditorView: vi.fn(async () => {}),
    openRunTab: vi.fn(async () => {}),
    openSubagentTab: vi.fn(async () => {}),
    toggleSettingsView: vi.fn(async () => {}),
    openSettingsView: vi.fn(async () => {}),
    toggleGitView: vi.fn(async () => {}),
    openGitView: vi.fn(async () => {}),
    toggleFilesView: vi.fn(async () => {}),
    openFilesView: vi.fn(async () => {}),
    toggleDocsView: vi.fn(async () => {}),

    adoptSubject: vi.fn(),
    activateTab: vi.fn(),
    activateRestoredTab: vi.fn(),
    refreshActiveView: vi.fn(),
    paintProvisionalTabs: vi.fn(),
    renameTab: vi.fn(),
    setTabStatus: vi.fn(),
    setTabRunStatus: vi.fn(),
    setTabDirty: vi.fn(),
    setSettingsTab: vi.fn(),
    setGitTab: vi.fn(),
    setDocsTab: vi.fn(),
    setHistoryTab: vi.fn(),
    setFilesRoute: vi.fn(),

    hasTab: vi.fn(() => false),
    tabIdFor: vi.fn(() => ""),
    filesTabIdFor: vi.fn(() => ""),
    tabSetVersion: vi.fn(() => 0),
    tabIdForRoute: vi.fn(() => ""),
    filesTabForRoute: vi.fn(() => ({ id: "", ref: "" })),
    getActiveTabId: vi.fn(() => ""),
    getActiveTabRoute: vi.fn(() => null),
    getActiveTabKind: vi.fn(() => null),
    activeChatRef: vi.fn(() => ""),
    parentChatRef: vi.fn(() => ""),
    openChatRefs: vi.fn(() => []),
    // A statement of DEMAND: `subagent-view.ts` drops a mounted page no open subagent tab names, so a
    // test mounting one must override this or the page is released under it.
    openSubagentRefs: vi.fn(() => []),
    openRunRefs: vi.fn(() => []),
    openSpecRefs: vi.fn(() => []),
    openTabSubjects: vi.fn(() => []),
    cueCandidates: vi.fn(() => []),

    // Hands back an unsubscribe so a caller's cleanup does not throw on undefined.
    subscribeTabCues: vi.fn(() => () => {}),
    setOnTabClosed: vi.fn(),
    setOnEmpty: vi.fn(),
    registerTabNotice: vi.fn(),
    // Unregistered is the never-suppress answer, so no test has a cue silently blanked.
    setChatSettledProbe: vi.fn(),

    _resetForTest: vi.fn(),
  };
}
