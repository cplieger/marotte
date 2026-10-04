import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { describe, expect, it } from "vitest";

import { anchorInterception } from "./always-on-interception.js";

interface RouteCall {
  predicate: (url: URL) => boolean;
  handler: (route: { fallback(): Promise<void> }) => unknown;
}

function fakeContext(): {
  routes: RouteCall[];
  route: (p: RouteCall["predicate"], h: RouteCall["handler"]) => Promise<void>;
} {
  const routes: RouteCall[] = [];
  return {
    routes,
    route: (predicate, handler) => {
      routes.push({ predicate, handler });
      return Promise.resolve();
    },
  };
}

function fakeProvider(contextFor: Record<string, ReturnType<typeof fakeContext>>) {
  const opened: string[] = [];
  return {
    opened,
    contexts: new Map(Object.entries(contextFor)),
    openPage(
      ...[sessionId]: [sessionId: string, url: string, options: { parallel: boolean }]
    ): Promise<void> {
      opened.push(sessionId);
      return Promise.resolve();
    },
  };
}

describe("anchorInterception", () => {
  it("routes one never-matching anchor per context, however many sessions share it", async () => {
    const shared = fakeContext();
    const provider = fakeProvider({ s1: shared, s2: shared });
    anchorInterception(provider);

    await provider.openPage("s1", "http://localhost/a", { parallel: false });
    await provider.openPage("s2", "http://localhost/b", { parallel: false });

    expect(provider.opened).toEqual(["s1", "s2"]);
    expect(shared.routes).toHaveLength(1);
    const anchor = shared.routes[0];
    expect(anchor?.predicate(new URL("http://localhost/src/api-client.ts"))).toBe(false);
    expect(anchor?.predicate(new URL("http://localhost/__vitest_browser__/tester.html"))).toBe(
      false,
    );
  });

  it("anchors each new context once", async () => {
    const first = fakeContext();
    const second = fakeContext();
    const provider = fakeProvider({ s1: first, s2: second });
    anchorInterception(provider);

    await provider.openPage("s1", "http://localhost/a", { parallel: false });
    await provider.openPage("s2", "http://localhost/b", { parallel: false });
    await provider.openPage("s2", "http://localhost/c", { parallel: false });

    expect(first.routes).toHaveLength(1);
    expect(second.routes).toHaveLength(1);
  });

  it("defers any request its handler is ever handed", async () => {
    const context = fakeContext();
    const provider = fakeProvider({ s1: context });
    anchorInterception(provider);
    await provider.openPage("s1", "http://localhost/a", { parallel: false });

    let fellBack = 0;
    await context.routes[0]?.handler({
      fallback: () => {
        fellBack++;
        return Promise.resolve();
      },
    });
    expect(fellBack).toBe(1);
  });

  it("fails the page open when the provider keeps no context for the session", async () => {
    const provider = fakeProvider({});
    anchorInterception(provider);

    await expect(
      provider.openPage("s1", "http://localhost/a", { parallel: false }),
    ).rejects.toThrow("no browser context for session s1");
  });
});

// https://github.com/vitest-dev/vitest/pull/11083 adds a probe route per Chromium
// context that keeps the route count above zero by itself, making the anchor redundant.
it("is still needed: the installed @vitest/browser-playwright carries no interception probe", () => {
  const entry = createRequire(import.meta.url).resolve("@vitest/browser-playwright");
  const source = readFileSync(entry, "utf8");
  expect(
    source.includes("__vitest_interception_probe__"),
    "the installed @vitest/browser-playwright ships vitest#11083's interception probe; " +
      "delete __test-helpers__/always-on-interception.ts and its use in vitest.config.ts",
  ).toBe(false);
});
