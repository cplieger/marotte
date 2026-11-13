// Text typed while a close has emptied the strip belongs to no chat, and a close put back must keep
// it beside the draft the restored chat already held, and out of every other chat. Real tabs.ts and
// the real composer state; the chat opener's select saves and restores the composer as
// `selectChat` does.
import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as TabsDrag from "./tabs-drag.js";

vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  pushRoute: vi.fn(),
}));
vi.mock("./icons.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tab-switch-mocks.js").then((m) => m.iconsMock())),
}));
vi.mock("./device-view.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tab-switch-mocks.js").then((m) => m.deviceViewMock())),
}));
vi.mock("./dom.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tab-switch-mocks.js").then((m) => m.domMock())),
}));
vi.mock("./tabs-drag.js", async (importOriginal) => ({
  ...(await importOriginal<typeof TabsDrag>()),
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
}));
vi.mock("./store.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/store-mock.js")).storeMock,
  ...(await import("./__test-helpers__/tab-switch-mocks.js")).storePointerOverrides(),
}));
vi.mock("./run-store.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  runLabelOf: vi.fn(() => ""),
}));
vi.mock("./context-menu.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  showContextMenu: vi.fn(),
}));
vi.mock("./chat-export.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  downloadChatExport: vi.fn(),
}));
vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock())),
}));
vi.mock("./transport.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tabs-server.js").then((m) => m.tabTransportMock())),
}));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tabs-server.js").then((m) => ({
    apiBlobOrError: vi.fn(),
    apiGetTyped: m.tabListRead(),
    apiGet: vi.fn(() => Promise.resolve(null)),
  }))),
}));

import { activateTab, closeTab, getActiveTabId } from "./tabs.js";
import { getSessions } from "./store.js";
import {
  _resetComposerStateForTest,
  composerDraft,
  dropComposerState,
  noteComposerText,
  restoreComposerState,
  saveComposerState,
} from "./composer-state.js";
import { addAttachment, takeAttachments } from "./attachments.js";
import { $ } from "./dom.js";
import { tabServer, settleTabs } from "./__test-helpers__/tabs-server.js";
import { fx, installSwitchSuite, strip } from "./__test-helpers__/tab-switch-suite.js";
import { activationRan, frame, pointers, resident } from "./__test-helpers__/tab-switch-mocks.js";
import type { Session } from "./types.js";

installSwitchSuite();

beforeEach(() => {
  _resetComposerStateForTest();
  for (const id of ["a", "c"]) {
    dropComposerState(id);
  }
  fx.chat.select.mockImplementation((id: string) => {
    saveComposerState();
    pointers.active = id;
    restoreComposerState(id);
  });
});

/** Strip a, c with C active, C holding `draft` as its saved draft and A holding `aDraft`. */
async function withDraftOnC(draft: string, aDraft = ""): Promise<Record<string, string>> {
  const ids = await strip("a", "c");
  $.promptInput.value = aDraft;
  noteComposerText(aDraft);
  activateTab(ids["c"] ?? "");
  await activationRan();
  resident.add("a");
  resident.add("c");
  vi.mocked(getSessions).mockReturnValue(
    ["a", "c"].map((id) => ({ id, name: id }) as unknown as Session),
  );
  $.promptInput.value = draft;
  noteComposerText(draft);
  return ids;
}

type Outcome = "refused" | "unanswered";

/** The next close fails after 2 s, or is never answered inside its 5 s deadline and never
 *  commits, so its verify finds the row still open and puts it back. */
function arrangeNextClose(outcome: Outcome): void {
  tabServer.failNext("close_tab", 500);
  tabServer.answerAfterNext("close_tab", outcome === "refused" ? 2_000 : 60_000, {
    ok: false,
    status: 500,
    error: "nope",
  });
}

const SAVED = "c's saved draft";
const TYPED = "half a thought";

describe("text typed on a strip a close emptied", () => {
  const cases: { outcome: Outcome; a: "confirmed" | "pending" }[] = [
    { outcome: "refused", a: "confirmed" },
    { outcome: "refused", a: "pending" },
    { outcome: "unanswered", a: "confirmed" },
    { outcome: "unanswered", a: "pending" },
  ];
  it.each(cases)(
    "C $outcome, A's close $a: the restored chat shows its saved draft and the typed text",
    async (k) => {
      const ids = await withDraftOnC(SAVED);
      await frame();
      vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"], shouldAdvanceTime: true });
      try {
        arrangeNextClose(k.outcome);
        const closedC = closeTab(ids["c"] ?? "");
        await activationRan();
        if (k.a === "pending") {
          tabServer.answerAfterNext("close_tab", 120_000, { ok: true } as never);
        }
        void closeTab(ids["a"] ?? "");
        await activationRan();
        expect(getActiveTabId(), "the strip emptied").toBe("");
        expect($.promptInput.value, "the box shows no chat's draft").toBe("");
        $.promptInput.value = TYPED;

        await vi.advanceTimersByTimeAsync(9_000);
        await closedC;
        await settleTabs();
        await activationRan();

        expect(getActiveTabId()).toBe(ids["c"]);
        expect(pointers.active).toBe("c");
        const kept = `${SAVED}\n\n${TYPED}`;
        expect($.promptInput.value, "the composer, which is what Send sends").toBe(kept);
        expect(composerDraft("c"), "the draft a switch away and back shows").toBe(kept);
      } finally {
        vi.useRealTimers();
      }
    },
  );

  it("gives a restored chat with no draft the typed text alone", async () => {
    const ids = await withDraftOnC("");
    await frame();
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"], shouldAdvanceTime: true });
    try {
      arrangeNextClose("refused");
      const closedC = closeTab(ids["c"] ?? "");
      await activationRan();
      void closeTab(ids["a"] ?? "");
      await activationRan();
      $.promptInput.value = TYPED;

      await vi.advanceTimersByTimeAsync(4_000);
      await closedC;
      await settleTabs();
      await activationRan();

      expect(getActiveTabId()).toBe(ids["c"]);
      expect($.promptInput.value).toBe(TYPED);
      expect(composerDraft("c")).toBe(TYPED);
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps the typed text out of the other chat when both closes are put back", async () => {
    const A_DRAFT = "a's own draft";
    const ids = await withDraftOnC(SAVED, A_DRAFT);
    await frame();
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"], shouldAdvanceTime: true });
    try {
      arrangeNextClose("refused");
      const closedC = closeTab(ids["c"] ?? "");
      await activationRan();
      arrangeNextClose("unanswered");
      const closedA = closeTab(ids["a"] ?? "");
      await activationRan();
      expect(getActiveTabId(), "the strip emptied").toBe("");
      $.promptInput.value = TYPED;

      await vi.advanceTimersByTimeAsync(2_500);
      await closedC;
      await settleTabs();
      await activationRan();
      expect(getActiveTabId(), "C's refusal put C back").toBe(ids["c"]);
      $.promptInput.value += " more";
      noteComposerText($.promptInput.value);

      await vi.advanceTimersByTimeAsync(6_500);
      await closedA;
      await settleTabs();
      await activationRan();

      expect(getActiveTabId(), "A's verify put A back where the reader left it").toBe(ids["a"]);
      expect($.promptInput.value, "A's composer, which is what Send sends").toBe(A_DRAFT);
      expect(composerDraft("a")).toBe(A_DRAFT);
      const kept = `${SAVED}\n\n${TYPED} more`;
      expect(composerDraft("c")).toBe(kept);
      activateTab(ids["c"] ?? "");
      await activationRan();
      expect($.promptInput.value, "C shows the typed text once").toBe(kept);
    } finally {
      vi.useRealTimers();
    }
  });

  it("hands a file staged on the emptied strip to the restored chat", async () => {
    const ids = await withDraftOnC(SAVED);
    await frame();
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"], shouldAdvanceTime: true });
    try {
      arrangeNextClose("refused");
      const closedC = closeTab(ids["c"] ?? "");
      await activationRan();
      void closeTab(ids["a"] ?? "");
      await activationRan();
      addAttachment("notes.md");

      await vi.advanceTimersByTimeAsync(4_000);
      await closedC;
      await settleTabs();
      await activationRan();

      expect(getActiveTabId()).toBe(ids["c"]);
      expect(
        takeAttachments().map((f) => f.path),
        "the file Send carries",
      ).toEqual(["notes.md"]);
    } finally {
      vi.useRealTimers();
    }
  });
});
