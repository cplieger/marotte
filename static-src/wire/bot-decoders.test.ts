// The Bot facts at the client's decoders: the sidebar sorts a tab into the Bots section from
// `TabSubject.bot`, History labels a row from `ResumableSession.bot`, and a chat's state reads from
// `ChatHeader.bot`, so each must survive decoding and stay absent on a regular chat.

import { describe, it, expect } from "vitest";
import fc from "fast-check";

import { arbChatHeader, arbResumableSession, arbTabSubject } from "./arbitraries.gen.js";
import { decodeChatHeader, decodeResumableSession, decodeTabSubject } from "./decoders.gen.js";

/** One valid wire value with its `bot` key removed, as a regular chat's server sends it. */
function without<T extends object>(arb: fc.Arbitrary<T>): Record<string, unknown> {
  const [value] = fc.sample(arb, { numRuns: 1, seed: 7 });
  const copy = JSON.parse(JSON.stringify(value)) as Record<string, unknown>;
  delete copy["bot"];
  return copy;
}

describe("Bot wire facts", () => {
  it("keeps a Bot's state on its chat header", () => {
    const header = { ...without(arbChatHeader), bot: { state: "paused" } };
    expect(decodeChatHeader(header).bot).toEqual({ state: "paused" });
  });

  it("leaves a regular chat's header without bot", () => {
    expect(decodeChatHeader(without(arbChatHeader))).not.toHaveProperty("bot");
  });

  it("refuses a Bot summary with no state or one outside the vocabulary", () => {
    const header = without(arbChatHeader);
    expect(() => decodeChatHeader({ ...header, bot: { state: "sleeping" } })).toThrow(/state/);
    expect(() => decodeChatHeader({ ...header, bot: {} })).toThrow(/state/);
  });

  it("keeps the bot fact on a tab subject", () => {
    const tab = without(arbTabSubject);
    expect(decodeTabSubject({ ...tab, bot: true }).bot).toBe(true);
    expect(decodeTabSubject(tab)).not.toHaveProperty("bot");
  });

  it("keeps the bot label on a History row", () => {
    const row = without(arbResumableSession);
    expect(decodeResumableSession({ ...row, bot: true }).bot).toBe(true);
    expect(decodeResumableSession(row)).not.toHaveProperty("bot");
  });
});
