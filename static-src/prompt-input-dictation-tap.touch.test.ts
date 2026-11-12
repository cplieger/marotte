// Dictation pressed with a finger's real tap, as on an iPad: words must reach the box and the button must show it
// is listening. `TOUCH_ENGINES=1` runs it again in WebKit at a phone's and an iPad's size.

import { vi } from "vitest";
import { dictationPressSuite } from "./__test-helpers__/dictation-press-suite.js";
import { tap } from "./__test-helpers__/tap.js";

vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));

dictationPressSuite({ press: tap, finger: true });
