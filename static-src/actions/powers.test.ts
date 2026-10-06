import { vi, describe, it, expect, beforeEach } from "vitest";

const m = vi.hoisted(() => ({
  reply: { status: 200, body: { ok: true } as unknown },
  errors: [] as string[],
}));

import { configure, configureApi } from "@cplieger/actions";
import { installPower, uninstallPower } from "./powers.js";

beforeEach(() => {
  m.reply = { status: 200, body: { ok: true } };
  m.errors.length = 0;
  configureApi({
    fetchFn: () =>
      Promise.resolve(
        new Response(JSON.stringify(m.reply.body), {
          status: m.reply.status,
          headers: { "content-type": "application/json" },
        }),
      ),
  });
  configure({
    success: vi.fn(),
    error: (msg) => {
      m.errors.push(msg);
    },
  });
});

describe("a Power change whose servers could not be configured", () => {
  it("shows the server's sentence for an install, which says the install happened", async () => {
    m.reply = {
      status: 502,
      body: {
        error: "postman was installed, but its MCP servers could not be configured",
        code: "render_failed",
      },
    };
    await installPower.dispatch({ name: "postman" });
    expect(m.errors).toEqual([
      "postman was installed, but its MCP servers could not be configured",
    ]);
  });

  it("shows the server's sentence for an uninstall", async () => {
    m.reply = {
      status: 502,
      body: {
        error: "postman was removed, but its MCP servers could not be configured",
        code: "render_failed",
      },
    };
    await uninstallPower.dispatch({ name: "postman" });
    expect(m.errors).toEqual(["postman was removed, but its MCP servers could not be configured"]);
  });

  it("still says the change failed for an ordinary kiro-cli failure", async () => {
    m.reply = { status: 502, body: { error: "tar not found" } };
    await installPower.dispatch({ name: "postman" });
    expect(m.errors).toEqual(["Couldn't install postman: tar not found"]);
  });
});
