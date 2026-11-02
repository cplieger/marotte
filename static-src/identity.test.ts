import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { setupWorker } from "msw/browser";

import { resolveIdentity } from "./identity.js";
import type { WhoamiResponse } from "./wire/types.gen.js";

const worker = setupWorker();

beforeAll(async () => {
  await worker.start({ quiet: true });
});

afterEach(() => {
  worker.resetHandlers();
});

afterAll(() => {
  worker.stop();
});

function answer(w: WhoamiResponse): void {
  worker.use(http.get("/api/whoami", () => HttpResponse.json(w)));
}

describe("resolveIdentity", () => {
  it("carries the vault account's labels to the account menu", async () => {
    answer({
      state: "signed_in",
      email: "a@example.invalid",
      auth: "Logged in with IAM Identity Center",
      accountType: "IamIdentityCenter",
      startUrl: "https://example.awsapps.com/start",
      region: "eu-west-1",
    });
    expect(await resolveIdentity()).toEqual({
      state: "signed_in",
      email: "a@example.invalid",
      method: "Logged in with IAM Identity Center",
      startUrl: "https://example.awsapps.com/start",
      region: "eu-west-1",
      apiKey: false,
    });
  });

  it("marks an API-key sign-in, which names no account", async () => {
    answer({ state: "signed_in", auth: "API key (KIRO_API_KEY)", apiKey: true });
    expect(await resolveIdentity()).toEqual({
      state: "signed_in",
      email: "",
      method: "API key (KIRO_API_KEY)",
      startUrl: "",
      region: "",
      apiKey: true,
    });
  });
});
