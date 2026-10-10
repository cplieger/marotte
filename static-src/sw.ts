// Service worker: Web Push, PWA installability, and the shell precache (content-hashed chunks served
// cache-first). Compiled to static/sw.js by tsconfig.sw.json.

// eslint-disable-next-line @typescript-eslint/triple-slash-reference
/// <reference path="sw-env.d.ts" />

import { type PrecacheManifest, isPreviewPath, isShellPath, parseManifest } from "./precache.js";
import { buildPath } from "./route-path.js";
import type { Route } from "./route-path.js";
import { parsePushTarget, pushTargetRoute, pushTargetTag } from "./push-subject.js";

const sw = self as unknown as ServiceWorkerGlobalScope;

// The shell precache: content-hashed chunks only, never a stable name or the HTML (precache.ts
// `isShellPath` owns the rule). Most are on the first-paint path, so a resume skips ~50 304s.

/** Cache holding the precached chunks, with the manifest stored INSIDE it so the stamp travels
 *  with its assets. A deploy is never masked: index.html is `no-store`. `syncPrecache` runs off
 *  every navigation, since a byte-identical sw.js fires no `install`. */
const SHELL_CACHE = "marotte-shell";

/** Where the build's asset list lives (cmd/bundle writes it). */
const PRECACHE_URL = "/precache.json";

/** The manifest a document just served, or null when unusable (`parseManifest`); `no-store`. */
async function fetchManifest(): Promise<PrecacheManifest | null> {
  try {
    const r = await fetch(PRECACHE_URL, { cache: "no-store" });
    if (!r.ok) {
      return null;
    }
    return parseManifest(await r.json());
  } catch {
    // Offline. The cache already holds whatever the last successful sync put
    // there, which is the state this whole mechanism exists to serve.
    return null;
  }
}

let syncing: Promise<boolean> | null = null;

/** Bring the cache in line with the current build; reports whether it moved. ONE SYNC AT A TIME:
 *  two straddling a deploy let one's prune delete the other's chunks under the new stamp, which
 *  `fillPrecache`'s stamp check then never repairs. A second caller joins the first. */
function syncPrecache(): Promise<boolean> {
  if (syncing !== null) {
    return syncing;
  }
  const run = fillPrecache();
  syncing = run.finally(() => {
    syncing = null;
  });
  return syncing;
}

/** One sync pass; call `syncPrecache`. ORDER IS LOAD-BEARING: fill, record the stamp, then prune
 *  (an early stamp makes a crashed sync look complete; an early prune blanks a loading document). */
async function fillPrecache(): Promise<boolean> {
  const next = await fetchManifest();
  if (next === null) {
    return false;
  }
  const cache = await caches.open(SHELL_CACHE);
  const held = await cache.match(PRECACHE_URL);
  if (held !== undefined) {
    const heldDoc = parseManifest(await held.json().catch(() => null));
    if (heldDoc?.stamp === next.stamp) {
      return false;
    }
  }
  await cache.addAll([...next.assets]);
  await cache.put(PRECACHE_URL, new Response(JSON.stringify(next)));
  const wanted = new Set([...next.assets, PRECACHE_URL]);
  for (const req of await cache.keys()) {
    if (!wanted.has(new URL(req.url).pathname)) {
      await cache.delete(req);
    }
  }
  return true;
}

sw.addEventListener("install", ((event: ExtendableEvent) => {
  // NOT gated on success: a manifest the build did not write, or a network that
  // is down at install time, must still leave a working worker. The next
  // navigation syncs.
  event.waitUntil(
    syncPrecache().catch((err: unknown) => {
      console.warn("sw: precache install failed", err);
      return false;
    }),
  );
}) as EventListener);

sw.addEventListener("activate", ((event: ExtendableEvent) => {
  event.waitUntil(
    (async () => {
      // Any cache from an earlier naming scheme. Nothing else in this origin's
      // storage is this worker's.
      for (const name of await caches.keys()) {
        if (name !== SHELL_CACHE && name.startsWith("marotte-")) {
          await caches.delete(name);
        }
      }
      // No skipWaiting, so claiming cannot hand a document a graph its HTML did not ask for; it makes the
      // first registering page controlled without a reload.
      await sw.clients.claim();
    })(),
  );
}) as EventListener);

// Three arms (the handler's presence also satisfies PWA installability): NAVIGATION to the network
// (and the deploy check), a CONTENT-HASHED CHUNK from cache, everything else never `respondWith`'d;
// `isShellPath` is synchronous for that reason.
sw.addEventListener("fetch", ((event: FetchEvent) => {
  const url = new URL(event.request.url);
  if (url.origin === location.origin && isPreviewPath(url.pathname)) {
    return;
  }
  if (event.request.mode === "navigate") {
    event.respondWith(fetch(event.request));
    event.waitUntil(
      syncPrecache().catch((err: unknown) => {
        console.warn("sw: precache sync failed", err);
        return false;
      }),
    );
    return;
  }
  if (event.request.method !== "GET") {
    return;
  }
  if (url.origin !== location.origin || !isShellPath(url.pathname)) {
    return;
  }
  event.respondWith(
    (async () => {
      const cache = await caches.open(SHELL_CACHE);
      const hit = await cache.match(event.request);
      return hit ?? (await fetch(event.request));
    })(),
  );
}) as EventListener);

/** The push is marotte.NotificationPayload as internal/notice wrote it: shown as sent, with at
 *  most ONE subject field (`chat_id`, or a kind-prefixed `subject`); the page owns routes. */
interface PushData {
  title?: string;
  body?: string;
  kind?: string;
  chat_id?: string;
  subject?: string;
}

/** Message to an open page: the SUBJECT (the page owns routes) and whether the user asked to go
 *  there, the notification arrived for the page to deliver, or the presence tag must be re-derived. */
interface PushPageMessage {
  type: "push";
  reason: "clicked" | "arrived" | "subscription_changed";
  chatId: string;
  subject: string;
  kind: string;
  title: string;
  body: string;
}

/** Read one string field off a notification's `data` bag. The bag is whatever the
 *  browser round-tripped, so every field is checked rather than cast; an absent or
 *  wrong-typed field reads as "", which every consumer treats as unset. */
function readStringField(raw: unknown, field: string): string {
  if (typeof raw !== "object" || raw === null) {
    return "";
  }
  const v: unknown = (raw as Record<string, unknown>)[field];
  return typeof v === "string" ? v : "";
}

/** Whether this client is on the route's pathname, both through the URL parser. An unparseable
 *  client is simply not preferred. */
function samePath(clientURL: string, route: Route): boolean {
  try {
    const here = new URL(clientURL);
    return here.pathname === new URL(buildPath(route), here).pathname;
  } catch {
    return false;
  }
}

/** Window clients for this origin, newest API shape first. */
async function windowClients(): Promise<WindowClient[]> {
  const list = await sw.clients.matchAll({ type: "window", includeUncontrolled: true });
  return list.filter((c): c is WindowClient => "focus" in c);
}

sw.addEventListener("push", ((event: PushEvent) => {
  if (event.data === null) {
    return;
  }
  let data: PushData;
  try {
    data = event.data.json() as PushData;
  } catch {
    data = { title: "Marotte", body: event.data.text() };
  }
  const title = data.title ?? "Marotte";
  const body = data.body ?? "";
  const kind = data.kind ?? "";
  const chatID = data.chat_id ?? "";
  const subject = data.subject ?? "";

  event.waitUntil(
    (async () => {
      // A focused page delivers it as its own `notification` frame (only the page knows which tab
      // is on screen): the one sanctioned exception to userVisibleOnly (Chrome would substitute a
      // generic notice).
      const clients = await windowClients();
      if (clients.some((c) => c.focused)) {
        for (const c of clients) {
          c.postMessage({
            type: "push",
            reason: "arrived",
            chatId: chatID,
            subject,
            kind,
            title,
            body,
          } satisfies PushPageMessage);
        }
        return;
      }
      try {
        await sw.registration.showNotification(title, {
          body,
          icon: "/favicon.svg",
          badge: "/icon-192.png",
          tag: pushTargetTag(parsePushTarget({ chatId: chatID, subject })),
          // A same-tag replacement is silent by default, and here a replacement always means the
          // chat moved to something else worth a glance.
          renotify: true,
          // Read back in notificationclick; the only place the target lives.
          data: { chatId: chatID, subject },
        });
      } catch (err: unknown) {
        console.error("sw: showNotification failed", err);
      }
    })(),
  );
}) as EventListener);

sw.addEventListener("notificationclick", ((event: NotificationEvent) => {
  event.notification.close();
  const raw: unknown = event.notification.data;
  const chatID = readStringField(raw, "chatId");
  const subject = readStringField(raw, "subject");
  // Named `route`, NOT `target`: `target` is already this block's name for the chosen
  // WindowClient, and two meanings of one word inside eight lines is how the next
  // reader gets it wrong.
  const route = pushTargetRoute(parsePushTarget({ chatId: chatID, subject }));

  event.waitUntil(
    (async () => {
      // Focus an existing page and post it the target: a single-page app's URLs never match exactly, and
      // WindowClient.navigate() is illegal for the uncontrolled clients included here.
      const clients = await windowClients();
      if (clients.length > 0) {
        const onTarget = clients.filter((c) => samePath(c.url, route));
        const target =
          onTarget.find((c) => c.focused) ?? // a matching window the reader is already in
          onTarget[0] ?? // any matching window
          clients.find((c) => c.focused) ??
          clients[0];
        if (target !== undefined) {
          target.postMessage({
            type: "push",
            reason: "clicked",
            chatId: chatID,
            subject,
            kind: "",
            title: event.notification.title,
            body: event.notification.body,
          } satisfies PushPageMessage);
          await target.focus();
          return;
        }
      }
      // No page open at all: this is the one path that needs a URL.
      await sw.clients.openWindow(buildPath(route));
    })(),
  );
}) as EventListener);

sw.addEventListener("pushsubscriptionchange", ((event: PushSubscriptionChangeEvent) => {
  const old = event.oldSubscription;
  event.waitUntil(
    resolveSubscribeOptions(old)
      .then((opts) => sw.registration.pushManager.subscribe(opts))
      .then((newSub: PushSubscription) =>
        fetch("/api/push/subscribe", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(newSub.toJSON()),
        }),
      )
      // A rotated subscription is a new presence tag; until pages re-derive, a push to the new endpoint
      // is sent rather than suppressed (fail-open).
      .then(async () => {
        for (const c of await windowClients()) {
          c.postMessage({
            type: "push",
            reason: "subscription_changed",
            chatId: "",
            subject: "",
            kind: "",
            title: "",
            body: "",
          } satisfies PushPageMessage);
        }
      })
      .catch((err: unknown) => {
        console.error("sw: re-subscribe failed", err);
      }),
  );
}) as EventListener);

/** pushsubscriptionchange options: the old subscription's when supplied; otherwise (it can be
 *  null) a bare `{userVisibleOnly:true}` fails on VAPID-enforcing services, so fetch the VAPID key. */
async function resolveSubscribeOptions(
  old: PushSubscription | null,
): Promise<PushSubscriptionOptionsInit> {
  if (old !== null) {
    return old.options;
  }
  const r = await fetch("/api/push/vapid-key");
  if (!r.ok) {
    throw new Error(`vapid-key fetch failed: HTTP ${String(r.status)}`);
  }
  const d = (await r.json()) as { publicKey?: string };
  if (typeof d.publicKey !== "string" || d.publicKey === "") {
    throw new Error("no VAPID public key available for re-subscribe");
  }
  return { userVisibleOnly: true, applicationServerKey: urlBase64ToUint8Array(d.publicKey) };
}

/** Base64url → Uint8Array for applicationServerKey. Not push-util.ts's helper: its implicit buffer
 *  backing is rejected by `applicationServerKey`. */
function urlBase64ToUint8Array(base64String: string): Uint8Array<ArrayBuffer> {
  const padding = "=".repeat((4 - (base64String.length % 4)) % 4);
  const base64 = (base64String + padding).replace(/-/g, "+").replace(/_/g, "/");
  const raw = atob(base64);
  // Explicit ArrayBuffer backing so the result satisfies BufferSource
  // under TS's generic TypedArray types (applicationServerKey rejects
  // Uint8Array<ArrayBufferLike>).
  const arr = new Uint8Array(new ArrayBuffer(raw.length));
  for (let i = 0; i < raw.length; i++) {
    arr[i] = raw.charCodeAt(i);
  }
  return arr;
}
