// What a location may mean: whether it may open its view, and what an unknown chat id settles to. Principle: never
// derive a terminal verdict without the server's own answer.

import { resolveUnknownChat } from "./chat.js";
import { chatListLoaded, serverMayAnswer } from "./store-load.js";
import { parseRoute } from "./route-path.js";
import type { Route } from "./route-path.js";
import { replaceRoute } from "./router.js";
import type { RouteOrigin } from "./router.js";
import { getActiveTabRoute, tabIdForRoute } from "./tabs.js";
import { error as toastError } from "./toast.js";

/** What settling did, for a test: `held` and `stale` look identical from outside. */
export type DeepLinkOutcome =
  /** The chat exists and its tab is open. */
  | "opened"
  /** The SERVER said there is no such chat: the URL was canonicalized and the
   *  reader told so. The one terminal outcome. */
  | "gone"
  /** Nobody answered. The URL still names the id and the reader has a retry. */
  | "unresolved"
  /** Nobody answered and the reader has ALREADY been told the server is
   *  unreachable, so nothing was raised. The URL is held either way. */
  | "held"
  /** An answer arrived for a location the reader has since left. Dropped. */
  | "stale";

/**
 * Whether the URL still names the chat asked about. The id is captured before the await and compared to the location
 * now, so a late verdict never replaces a newer location.
 */
function stillNames(id: string): boolean {
  const now = parseRoute(location.pathname, location.hash);
  return now.kind === "chat" && now.id === id;
}

/** Points the URL at what is on screen (`getActiveTabRoute()`), else the empty chat route, as boot does for "/". */
function canonicalize(): void {
  replaceRoute(getActiveTabRoute() ?? { kind: "chat", id: "" });
}

/** What a location was allowed to mean, for the caller and for a test to read. */
export type LocationVerdict =
  /** It may open the view it names. */
  | "opens"
  /** It named a view this workspace no longer holds, so the URL was pointed at
   *  what IS on screen and nothing is to be applied. */
  | "canonicalized";

/**
 * Whether a location may open its view. A deliberate navigation may; a `history` entry or restored load may only
 * activate something already open (`openTab` persists and broadcasts, so opening would resurrect a tab closed elsewhere).
 */
export function admitLocation(route: Route, origin: RouteOrigin): LocationVerdict {
  if (origin === "deeplink" || tabIdForRoute(route) !== "") {
    return "opens";
  }
  canonicalize();
  return "canonicalized";
}

/**
 * Settle a deep-linked chat id with no store row by asking the server: exists (opens), server says gone (canonicalize
 * and say so), no answer (hold the URL, non-terminal notice with retry), or no answer already reported (silent).
 * The ask is skipped when there is evidence the server cannot answer (`serverMayAnswer`).
 */
export async function settleDeepLinkedChat(id: string): Promise<DeepLinkOutcome> {
  if (!serverMayAnswer()) {
    return "held";
  }
  const verdict = await resolveUnknownChat(id);
  if (verdict === "opened") {
    // `resolveUnknownChat` opened the tab and raised its own refusal notice.
    return "opened";
  }
  if (!stillNames(id)) {
    return "stale";
  }
  if (verdict === "gone") {
    canonicalize();
    toastError("That conversation no longer exists.");
    return "gone";
  }
  if (!chatListLoaded()) {
    return "held";
  }
  // Not canonicalized, so the retry, a reload or a re-share address the asked-for chat. The words claim nothing
  // terminal. Retry re-asks rather than reloading.
  toastError("Could not open that conversation. The server did not answer.", {
    label: "Retry",
    onClick: () => {
      void settleDeepLinkedChat(id);
    },
  });
  return "unresolved";
}
