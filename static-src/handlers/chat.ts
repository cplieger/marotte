// SSE handlers for chat lifecycle: create, update, delete.

import { onSSE } from "../bus.js";
import { upsertHeader, removeChat, getActiveId, setAgentStatus } from "../store.js";
import { dropDecisions } from "../decision-dock.js";
import { forgetDeferredCue } from "../agent-finished-cue.js";
import { dropComposerState, adoptRemoteComposerState } from "../composer-state.js";
import { parseRoute } from "../route-path.js";
import { replaceRoute } from "../router.js";

// Defensive `=== undefined` guards: the wire decoder marks payloads
// non-nullable but the test suite (and a malformed frame at runtime)
// can hand us undefined, so the lint reads every check as dead.
/* eslint-disable @typescript-eslint/no-unnecessary-condition */

// There is no adoptHeader wrapper any more: the chat id is the server's from
// the moment the chat exists, so the ordinary openTab emit covers it.

onSSE("chat_created", (_chatID, header) => {
  if (header === undefined) {
    return;
  }
  upsertHeader(header);
  // `applyInitialRoute` canonicalizes an active chat, so this covers a chat becoming active while
  // the route is still "/" (e.g. created on another device). Never hijack a reader who navigated away.
  const route = parseRoute(location.pathname, location.hash);
  if (header.id === getActiveId() && route.kind === "chat" && route.id === "") {
    replaceRoute({ kind: "chat", id: header.id });
  }
});

onSSE("chat_updated", (_chatID, header) => {
  if (header === undefined) {
    return;
  }
  upsertHeader(header);
});

// Lets an idle device converge on a draft written elsewhere. The local map is authoritative for
// the chat on screen, so the live chat is ignored (adoptRemoteComposerState).
onSSE("draft_changed", (chatID, p) => {
  if (chatID === "" || p === undefined) {
    return;
  }
  adoptRemoteComposerState(chatID, p.text ?? "", p.attachments ?? []);
});

// chat_status: the agent's self-declared activity. Ephemeral — cleared on
// the next prompt send and on a transport gap, never persisted.
onSSE("chat_status", (chatID, p) => {
  if (chatID === "" || p === undefined) {
    return;
  }
  setAgentStatus(chatID, p.status ?? "");
});

onSSE("chat_deleted", (_chatID, p) => {
  if (p === undefined || typeof p.id !== "string" || p.id === "") {
    return;
  }
  // No tab close: the membership coordinator closes a deleted chat's tabs under the delete's lock.
  // The per-chat cleanups below are keyed by chat id, outlive the tab, and are idempotent.
  dropDecisions(p.id);
  forgetDeferredCue(p.id);
  dropComposerState(p.id);
  removeChat(p.id);
  // Drop the chat's in-memory banner entries; persisted dismissals are not
  // pruned here since only the BannerEntry DOM objects need dropping.
  void import("../banner-stack.js").then(
    (m) => {
      m.clearBannersForChat(p.id);
    },
    (e: unknown) => {
      console.warn("[handlers/chat] clearBannersForChat import failed", e);
    },
  );
});
