import { type PlaywrightBrowserProvider, playwright } from "@vitest/browser-playwright";

interface AnchorContext {
  route(
    url: (url: URL) => boolean,
    handler: (route: { fallback(): Promise<void> }) => unknown,
  ): Promise<unknown>;
}

interface AnchorableProvider {
  readonly contexts: Map<string, AnchorContext>;
  openPage(sessionId: string, url: string, options: { parallel: boolean }): Promise<void>;
}

/** Routes one never-matching anchor per browser context the provider opens a page in.
 *  Per CONTEXT, not per session: `persistentContext` shares one context across sessions. */
export function anchorInterception(instance: AnchorableProvider): void {
  const anchored = new WeakSet<AnchorContext>();
  const openPage = instance.openPage.bind(instance);
  instance.openPage = async (sessionId, url, options) => {
    await openPage(sessionId, url, options);
    const context = instance.contexts.get(sessionId);
    if (context === undefined) {
      throw new Error(`anchorInterception: no browser context for session ${sessionId}`);
    }
    if (anchored.has(context)) return;
    anchored.add(context);
    await context.route(
      () => false,
      (route) => route.fallback(),
    );
  };
}

// Keeps every Chromium context's route count above zero. Unrouting the last mock route removes
// Playwright's interceptor, so a mock still resolving throws `Route is already handled!` and a
// request in the gap gets the real module (https://github.com/vitest-dev/vitest/issues/8339;
// fixed by https://github.com/vitest-dev/vitest/pull/11083, not in 5.0.3).
export function alwaysOnInterception(
  ...args: Parameters<typeof playwright>
): ReturnType<typeof playwright> {
  const provider = playwright(...args);
  return {
    ...provider,
    providerFactory(project) {
      // The factory is typed as the BrowserProvider interface; playwright() builds this class.
      const instance = provider.providerFactory(project) as PlaywrightBrowserProvider;
      anchorInterception(instance);
      return instance;
    },
  };
}
