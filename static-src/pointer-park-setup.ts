// A worker's page keeps the real pointer where the last test left it, and Chromium re-hovers whatever the next test
// lays out there: each test starts with the pointer off the page (__test-helpers__/park-pointer.ts).
import { beforeEach } from "vitest";

import { parkPointer } from "./__test-helpers__/park-pointer.js";

beforeEach(async () => {
  await parkPointer();
});
