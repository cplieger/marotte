// The device Memory choice (Settings > Agent capabilities > Memory), kiro-cli's own setting for the signed-in
// account on this machine.

import { apiAction } from "./index.js";
import { stillOr } from "./still-applying.js";
import { decodeDeviceMemory } from "../wire/decoders.gen.js";
import type { DeviceMemory } from "../wire/types.gen.js";

interface MemoryModeArgs {
  mode: string;
}

/** The failure text the Memory row shows under it, in place of a toast. */
export const memoryModeError = stillOr("changing Memory", "Couldn't change Memory");

// No auto-retry: a timed-out write may have landed.
export const setMemoryMode = apiAction<MemoryModeArgs, DeviceMemory>({
  name: "memory.mode",
  scope: () => "memory.mode",
  request: (body) => ({ method: "PUT", path: "/api/memory/mode", body }),
  decode: (data) => decodeDeviceMemory(data),
  error: false,
});
