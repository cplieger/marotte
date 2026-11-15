// The account's fallback for one model (the model picker's Fallback row).

import { apiAction, fromEndpoint } from "./index.js";
import { stillOr } from "./still-applying.js";
import { putModelFallbackEndpoint } from "../wire/client.gen.js";
import { decodeModelFallback } from "../wire/decoders.gen.js";
import type { ModelFallback } from "../wire/types.gen.js";

/** `target` is the fallback model's id, "" for Kiro's default. */
interface FallbackArgs {
  model: string;
  target: string;
}

// No auto-retry: a timed-out write may have landed. Writes for one model run in order, so the last pick is the
// one the account keeps.
export const setModelFallback = apiAction<FallbackArgs, ModelFallback>({
  name: "model.fallback",
  scope: ({ model }) => `model.fallback:${model}`,
  request: (body) => fromEndpoint(putModelFallbackEndpoint.request({ body })),
  decode: (data) => decodeModelFallback(data),
  error: stillOr("changing the fallback model", "Couldn't change the fallback model", "chat"),
});
