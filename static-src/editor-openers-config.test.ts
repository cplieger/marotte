import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as Router from "./router.js";
import type * as Tabs from "./tabs.js";
import type * as Toast from "./toast.js";
import type * as Versions from "./versions.js";

vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Tabs>()),
  openEditorView: vi.fn(() => Promise.resolve()),
}));
vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Router>()),
  pushRoute: vi.fn(),
}));
vi.mock("./versions.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Versions>()),
  configFilePath: vi.fn(),
}));
vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("./__test-helpers__/toast-mock.js")).toastMock(),
}));

const { openConfigFile } = await import("./editor-openers.js");
const { openEditorView } = await import("./tabs.js");
const { configFilePath } = await import("./versions.js");
const { error: toastError } = await import("./toast.js");

beforeEach(() => {
  vi.mocked(openEditorView).mockClear();
  vi.mocked(toastError).mockClear();
});

describe("openConfigFile", () => {
  it("opens the file under the config directory the server names", async () => {
    vi.mocked(configFilePath).mockResolvedValue("/data/marotte/tools.json");

    await openConfigFile("tools.json");

    expect(configFilePath).toHaveBeenCalledWith("tools.json");
    expect(vi.mocked(openEditorView).mock.calls.map((c) => c[0])).toEqual([
      "/data/marotte/tools.json",
    ]);
    expect(toastError).not.toHaveBeenCalled();
  });

  it("opens nothing and says why while the config directory is unknown", async () => {
    vi.mocked(configFilePath).mockResolvedValue(null);

    await openConfigFile("config.json");

    expect(openEditorView).not.toHaveBeenCalled();
    expect(vi.mocked(toastError).mock.calls).toEqual([
      [
        "Could not open config.json: the server did not say where its config directory is. Try again.",
      ],
    ]);
  });
});
