// What a message sent while a turn runs does by default, for every chat: the server's interrupt_mode setting.

import { signal, type ReadonlySignal } from "@cplieger/reactive";
import type { EffectiveSettings, InterruptMode } from "./wire/types.gen.js";

const mode = signal<InterruptMode>("steer");

/** The default half of the composer's mid-turn pill, and what Enter does beside a steerable turn. */
export const interruptDefault: ReadonlySignal<InterruptMode> = mode;

/** The other action: the pill's icon half. */
export function otherInterrupt(m: InterruptMode): InterruptMode {
  return m === "steer" ? "queue" : "steer";
}

/** Takes the setting from a settings answer, at boot and on every `settings_updated`. */
export function adoptInterruptDefault(s: Pick<EffectiveSettings, "interrupt_mode">): void {
  mode.value = s.interrupt_mode;
}
