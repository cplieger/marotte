// Dictation pressed with a real mouse click: words must reach the box and the button must show it is listening.
// `TOUCH_ENGINES=1` runs it again in WebKit at a desktop's size.

import { vi } from "vitest";
import { page } from "vitest/browser";
import { dictationPressSuite } from "./__test-helpers__/dictation-press-suite.js";

vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));

dictationPressSuite({
  press: async (selector) => {
    await page.elementLocator(document.querySelector(selector) as Element).click();
  },
  finger: false,
});
