// What this page tells the server about itself: whether it is visible and focused, and the chat or
// run tab it shows, so the server's notice ledger offers each notice to one page per profile; and
// what it did with a notice it was offered.

import { BUS_TAB_CHANGED, onBus, onSSE } from "./bus.js";
import { onPresentedTag, presentedTag } from "./sse-adapter.js";
import { getActiveTabId, openTabSubjects } from "./tabs.js";
import type { NoticeOutcome, PageView, TabRef } from "./wire/types.gen.js";
import { postEventsNoticeEndpoint, postEventsViewEndpoint } from "./wire/client.gen.js";

/** liveness.Keepalive: the server reads a visible row silent past its alive window as hidden. */
const VISIBLE_REPORT_MS = 15_000;

const page = crypto.randomUUID();

/** This page load's id, the one `show_in` names. */
export function pageID(): string {
  return page;
}

/** The active tab when it is a chat or run tab: the only tabs a notice is about. */
function activeTab(): TabRef | null {
  const active = getActiveTabId();
  const subject = openTabSubjects().find((s) => s.id === active);
  if (subject === undefined || (subject.kind !== "chat" && subject.kind !== "run")) {
    return null;
  }
  return { kind: subject.kind, ref: subject.ref };
}

function currentView(): PageView {
  const view: PageView = {
    page,
    visible: document.visibilityState === "visible",
    focused: document.hasFocus(),
  };
  const tab = activeTab();
  if (tab !== null) {
    view.tab = tab;
  }
  return view;
}

/** keepalive, so the hidden report a phone sends as it locks outlives the freeze that may follow;
 *  a raw fetch because a beacon cannot carry the SSE-Client header. */
function post(path: string, body: unknown): void {
  const tag = presentedTag();
  if (tag === "") {
    return;
  }
  fetch(path, {
    method: "POST",
    keepalive: true,
    headers: { "Content-Type": "application/json", "SSE-Client": tag },
    body: JSON.stringify(body),
  }).catch(() => undefined);
}

/** Tell the server this page showed or declined notice `id`. */
export function reportNotice(id: string, outcome: NoticeOutcome): void {
  post(postEventsNoticeEndpoint.path, { id, outcome });
}

/** Start reporting this page's view; returns the teardown. */
export function installPageView(): () => void {
  let scheduled = false;
  const report = (): void => {
    if (scheduled) {
      return;
    }
    scheduled = true;
    queueMicrotask(() => {
      scheduled = false;
      post(postEventsViewEndpoint.path, currentView());
    });
  };
  const ac = new AbortController();
  const signal = ac.signal;
  document.addEventListener("visibilitychange", report, { signal });
  window.addEventListener("focus", report, { signal });
  window.addEventListener("blur", report, { signal });
  window.addEventListener(
    "pagehide",
    () => {
      post(postEventsViewEndpoint.path, { page, closed: true });
    },
    { signal },
  );
  const offs = [onSSE("connected", report), onBus(BUS_TAB_CHANGED, report), onPresentedTag(report)];
  const timer = setInterval(() => {
    if (document.visibilityState === "visible") {
      report();
    }
  }, VISIBLE_REPORT_MS);
  // A tab attaching to a worker host whose stream is already open gets no `connected` frame.
  report();
  return () => {
    ac.abort();
    clearInterval(timer);
    for (const off of offs) {
      off();
    }
  };
}
