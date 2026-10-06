/** Canonical toast mock factory: `vi.mock("../toast.js", toastMock)`. */
import { vi } from "vitest";

export const toastMock = () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  errorWithAction: vi.fn(),
  showToast: vi.fn(),
  notice: vi.fn(),
});
