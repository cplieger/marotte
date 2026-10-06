// A custom agent's own knowledge bases index into that agent's store, which the
// Settings list never shows, so this banner is the only place one appears.

import { join as joinKey } from "@cplieger/keyenc";

import { clearBannerCodes, showBanner } from "../banner-stack.js";
import { BUS_PAGE_RESUMED, BUS_RECONCILE, onBus, onSSE } from "../bus.js";
import { notice } from "../toast.js";
import { named, noticeSubject } from "../notice-subject.js";

/** How long an index must still be running before the banner appears. KAS
 *  re-indexes on every session start, load and mode switch, so a small base
 *  would otherwise flash a banner on every chat open. */
export const INDEXING_GRACE_MS = 1500;

interface Pending {
  readonly chatID: string;
  readonly code: string;
  timer: ReturnType<typeof setTimeout> | undefined;
}

const pending = new Map<string, Pending>();

function bannerCode(name: string): string {
  return joinKey("knowledge-indexing", name);
}

function filesPhrase(count: number): string {
  return count === 1 ? "1 file" : `${String(count)} files`;
}

function clearEntry(key: string): void {
  const entry = pending.get(key);
  if (entry === undefined) {
    return;
  }
  clearTimeout(entry.timer);
  pending.delete(key);
  clearBannerCodes(entry.chatID, [entry.code]);
}

function clearAll(): void {
  for (const key of [...pending.keys()]) {
    clearEntry(key);
  }
}

onSSE("knowledge_indexing", (chatID, p) => {
  if (chatID === "") {
    return;
  }
  const key = joinKey(chatID, p.name);
  if (p.phase === "started") {
    clearEntry(key);
    const entry: Pending = { chatID, code: bannerCode(p.name), timer: undefined };
    const text = `Indexing knowledge base \u201C${p.name}\u201D (${filesPhrase(p.file_count ?? 0)})\u2026`;
    entry.timer = setTimeout(() => {
      entry.timer = undefined;
      showBanner(chatID, entry.code, text, "info", false);
    }, INDEXING_GRACE_MS);
    pending.set(key, entry);
    return;
  }
  clearEntry(key);
  if (p.status === "failed") {
    const subject = noticeSubject(chatID);
    notice(
      named(
        subject,
        `Couldn\u2019t index knowledge base \u201C${p.name}\u201D. The agent\u2019s searches may miss its contents.`,
      ),
      "error",
      subject.open,
    );
  }
});

// A completion missed across a gap must not leave a banner standing.
onBus(BUS_PAGE_RESUMED, clearAll);
onBus(BUS_RECONCILE, clearAll);

// A deleted chat's bridge never delivers the completion, so its entries would
// otherwise stay forever and a pending timer would raise a banner on a chat that
// no longer exists.
onSSE("chat_deleted", (_chatID, p) => {
  for (const [key, entry] of [...pending]) {
    if (entry.chatID === p.id) {
      clearEntry(key);
    }
  }
});
