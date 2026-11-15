// The Kiro sign-in's health (GET /api/whoami's `health` and the signin_health event) and its app-wide banner.

import type { SignInHealth } from "./wire/types.gen.js";

/** Paint `h` unless the health shown is newer: the whoami read and the event arrive in any order. */
export function adoptSignInHealth(_h: SignInHealth): void {}

/** Subscribe to the event and re-read the health on a reconcile. Once, from app.ts. */
export function initSignInHealth(): void {}
