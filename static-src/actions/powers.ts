// No auto-retry: a timed-out install keeps running on the server, and
// powers_changed repaints the list when it lands.

import { apiAction, fromEndpoint } from "./index.js";
import { deletePowersByNameEndpoint, postPowersByNameInstallEndpoint } from "../wire/client.gen.js";
import type { ActionErrorLike } from "./index.js";

interface PowerArgs {
  name: string;
}

function failure(verb: string, name: string, err: ActionErrorLike): string {
  // The server's sentence already says the change was made.
  if (err.code === "render_failed") {
    return err.message;
  }
  if (err.code === "timeout") {
    return `Still ${verb === "install" ? "installing" : "removing"} ${name}; the list updates when it finishes`;
  }
  return `Couldn't ${verb} ${name}: ${err.message}`;
}

export const installPower = apiAction<PowerArgs>({
  name: "powers.install",
  dedupe: (args) => `powers.install:${args.name}`,
  request: ({ name }) => fromEndpoint(postPowersByNameInstallEndpoint.request({ name })),
  error: (args, err) => failure("install", args.name, err),
});

export const uninstallPower = apiAction<PowerArgs>({
  name: "powers.uninstall",
  dedupe: (args) => `powers.uninstall:${args.name}`,
  request: ({ name }) => fromEndpoint(deletePowersByNameEndpoint.request({ name })),
  error: (args, err) => failure("uninstall", args.name, err),
});
