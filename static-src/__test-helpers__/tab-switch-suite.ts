// The tab-switch suites' shared setup over the real tabs.ts. Import it from a suite whose
// `vi.mock` block delegates to `tab-switch-mocks.ts`; `installSwitchSuite()` registers the hooks.
import { beforeEach, afterEach, expect, vi } from "vitest";
import { openTab, tabIdFor, getActiveTabId, _resetForTest } from "../tabs.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "../tab-materialize.js";
import type { TabOpeners } from "../tab-materialize.js";
import { ingestTabsChanged, listTabs, _resetTabsSyncForTest } from "../tabs-sync.js";
import { _resetForTest as _resetFreshnessForTest } from "../subject-versions.js";
import { resetActionFramework } from "../actions/__test-helpers__/action-test-setup.js";
import { bindTabsSync, tabServer, settleTabs } from "./tabs-server.js";
import {
  activationRan,
  chatOpenerFake,
  mountShell,
  pointers,
  resetPointers,
  resident,
  type ChatOpenerFake,
} from "./tab-switch-mocks.js";
import { recordNotices, type Notice } from "./switch-notices.js";
import { dropCover } from "../transcript-cover.js";

bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });

/** The current case's fakes, replaced by each beforeEach. */
export const fx: { chat: ChatOpenerFake; notices: Notice[] } = {
  chat: chatOpenerFake(),
  notices: [],
};

export function installSwitchSuite(): void {
  beforeEach(() => {
    tabServer.reset();
    _resetTabsSyncForTest();
    _resetTabOpenersForTest();
    resetPointers();
    fx.chat = chatOpenerFake();
    // The chat opener's activation-era shape: `select` is the gesture half, `show` the activation
    // half, `refresh` takes the activation's signal and answers when the view has content.
    registerTabOpeners({
      chat: fx.chat,
      editor: { show: vi.fn(), refresh: vi.fn(), close: vi.fn() },
      run: { show: vi.fn(), refresh: vi.fn() },
      subagent: { show: vi.fn(), refresh: vi.fn() },
      spec: { show: vi.fn(), refresh: vi.fn() },
    } as unknown as TabOpeners);
    resetActionFramework();
    fx.notices = recordNotices();
    _resetForTest();
    _resetFreshnessForTest();
    mountShell();
  });
  afterEach(() => {
    vi.useRealTimers();
    _resetTabsSyncForTest();
  });
}

/** Chats `refs` open, the first one active, shown, settled and resident; every spy cleared. */
export async function strip(...refs: string[]): Promise<Record<string, string>> {
  for (const ref of refs) {
    await openTab({ kind: "chat", ref, activate: ref === refs[0] });
  }
  await settleTabs();
  await activationRan();
  const first = refs[0] ?? "";
  expect(getActiveTabId()).toBe(tabIdFor("chat", first));
  pointers.active = first;
  pointers.painted = first;
  resident.add(first);
  // The fake opener mounts nothing, so the paint that would release the first chat's cover is
  // stood in for here.
  dropCover();
  vi.clearAllMocks();
  return Object.fromEntries(refs.map((r) => [r, tabIdFor("chat", r)]));
}
