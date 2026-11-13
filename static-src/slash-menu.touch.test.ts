// A finger's tap on a `/` or `#` menu row picks it, as a click or Enter does. A row greyed by a
// running turn must take the tap once the turn ends, without another keystroke first.
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { userEvent } from "vitest/browser";

import type * as ApiClient from "./api-client.js";

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetTyped: vi.fn(() => Promise.resolve(null)),
}));

import {
  _resetCatalogForTest,
  _setCatalogForTest,
  _stopSlashMenuForTest,
  initSlashMenu,
} from "./slash-menu.js";
import { setActive, setSessions, setTurnOpen } from "./store.js";
import { adoptInterruptDefault } from "./interrupt-default.js";
import { _resetAttachmentsForTest, addAttachment, removeAttachmentFrom } from "./attachments.js";
import type { RunView, SendVerb } from "./wire/types.gen.js";
import { makeSession } from "./__test-helpers__/model.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { tap } from "./__test-helpers__/tap.js";

const CHAT = "c-1";
/** The run order the server sends while a turn runs, with what a plain send would do. */
const runningRun = (send: SendVerb): RunView => ({
  send,
  entries: [],
  next_wait: { after: "turns", turns: 1 },
  unread_wait: { after: "running" },
  version: 1,
});
const row = (name: string): string => `.slash-opt:has(.slash-opt-name:text-is("${name}"))`;

describe("a tap on a menu row", () => {
  let input: HTMLTextAreaElement;
  let menu: HTMLUListElement;
  let style: HTMLStyleElement;
  let pills: HTMLUListElement;

  beforeEach(() => {
    style = mountAppCSS();
    menu = document.createElement("ul");
    menu.id = "slash-menu";
    menu.className = "slash-menu";
    menu.hidden = true;
    input = document.createElement("textarea");
    input.id = "prompt-input";
    pills = document.createElement("ul");
    pills.id = "attachment-row";
    document.body.append(menu, input, pills);
    setSessions([makeSession({ id: CHAT })]);
    setActive(CHAT);
    initSlashMenu(() => undefined);
    _setCatalogForTest([
      { name: "chat-extract", description: "Re-read the conversation", kind: "prompt" },
    ]);
    input.focus();
  });

  function greyedRows(): string[] {
    return [...menu.querySelectorAll('.slash-opt[aria-disabled="true"] .slash-opt-name')].map(
      (e) => e.textContent,
    );
  }

  afterEach(() => {
    _stopSlashMenuForTest();
    _resetAttachmentsForTest();
    pills.remove();
    input.blur();
    input.remove();
    menu.remove();
    style.remove();
    _resetCatalogForTest();
    setActive("");
    setSessions([]);
  });

  it("fills a saved prompt's name and keeps the box focused", async () => {
    await userEvent.keyboard("/chat");
    expect(menu.hidden).toBe(false);
    await tap(row("/chat-extract"));
    expect(input.value).toBe("/chat-extract ");
    expect(menu.hidden).toBe(true);
    expect(document.activeElement).toBe(input);
  });

  it("turns a # provider row into its prefix", async () => {
    await userEvent.keyboard("see #");
    expect(menu.hidden).toBe(false);
    await tap(row("Folder"));
    expect(input.value).toBe("see #folder:");
    expect(document.activeElement).toBe(input);
  });

  it("takes the tap on a row a running turn greyed as soon as that turn ends", async () => {
    setSessions([
      makeSession({ id: CHAT, turn_open: true, run: runningRun("steer") }),
    ]);
    await userEvent.keyboard("/chat");
    expect(greyedRows()).toEqual(["/chat-extract"]);
    setTurnOpen(CHAT, false);
    await vi.waitFor(() => {
      expect(greyedRows()).toEqual([]);
    });
    await tap(row("/chat-extract"));
    expect(input.value).toBe("/chat-extract ");
  });

  it("takes the tap on a saved prompt mid-turn when a staged file makes the message queue", async () => {
    setSessions([
      makeSession({ id: CHAT, turn_open: true, run: runningRun("steer") }),
    ]);
    addAttachment("docs/plan.md");
    await userEvent.keyboard("/chat");
    expect(greyedRows()).toEqual([]);
    await tap(row("/chat-extract"));
    expect(input.value).toBe("/chat-extract ");
  });

  it("greys an open saved-prompt row once the staged file is removed mid-turn", async () => {
    setSessions([
      makeSession({ id: CHAT, turn_open: true, run: runningRun("steer") }),
    ]);
    addAttachment("docs/plan.md");
    await userEvent.keyboard("/chat");
    expect(greyedRows()).toEqual([]);
    removeAttachmentFrom("", "docs/plan.md");
    await vi.waitFor(() => {
      expect(greyedRows()).toEqual(["/chat-extract"]);
    });
  });

  it("takes the tap on a saved prompt mid-turn when the message would queue", async () => {
    setSessions([makeSession({ id: CHAT, turn_open: true, run: runningRun("queue") })]);
    await userEvent.keyboard("/chat");
    expect(greyedRows()).toEqual([]);
    await tap(row("/chat-extract"));
    expect(input.value).toBe("/chat-extract ");
  });

  it("takes the tap on a saved prompt beside a steerable turn when Queue is the default", async () => {
    adoptInterruptDefault({ interrupt_mode: "queue" });
    try {
      setSessions([makeSession({ id: CHAT, turn_open: true, run: runningRun("steer") })]);
      await userEvent.keyboard("/chat");
      expect(greyedRows()).toEqual([]);
      await tap(row("/chat-extract"));
      expect(input.value).toBe("/chat-extract ");
    } finally {
      adoptInterruptDefault({ interrupt_mode: "steer" });
    }
  });
});
