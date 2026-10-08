// Canonical scroll.js mock: add an export here when scroll.ts gains one (scroll-mock.test.ts).
import { vi } from "vitest";
// Type-only, so the mocked module is not loaded; the types widen the mocks' return types (else
// `readingState` would be the literal "following" and "reading" would not typecheck).
import type { ReadingState, ViewScrollState } from "../scroll.js";

export const scrollMock = {
  // The real value: `readingLineOffset` is mocked to 0, so a reader does its own arithmetic.
  READING_LINE_FRACTION: 1 / 3,
  getScrollEl: vi.fn(() => document.createElement("div")),
  // The default snapshot is a fresh view's state, so a mocked park/unpark round-trips unprimed.
  attach: vi.fn(),
  detach: vi.fn((): ViewScrollState => ({ scrollTop: 0, readingState: "following" })),
  scrollToBottom: vi.fn(),
  setUserScrolledUp: vi.fn(),
  jumpTo: vi.fn(),
  // `scrollToOffset` moves nothing here; a suite asserting a landing drives the scroller itself.
  beginSelfScroll: vi.fn(),
  endSelfScroll: vi.fn(),
  scrollToOffset: vi.fn(),
  atLiveEdgeNow: vi.fn(() => true),
  // A real answer: a suite asserting a landing must prime it, or the two coincide for any arithmetic.
  readingLineOffset: vi.fn(() => 0),
  resetScrollState: vi.fn(),
  setLoadMore: vi.fn(),
  rebaseLoadMore: vi.fn(),
  readingState: vi.fn((): ReadingState => "following"),
  onReadingStateChange: vi.fn(),
  // Inert registrations: a mocked scroller never fires them; each returns the promised unregister.
  onTranscriptMutate: vi.fn(() => () => undefined),
  onReaderGesture: vi.fn(() => () => undefined),
  onViewportChange: vi.fn(() => () => undefined),
  onContentResize: vi.fn(() => () => undefined),
  onAttach: vi.fn(() => () => undefined),
  setAnchorProvider: vi.fn(),
  setResumeLabel: vi.fn(),
  // The deferral helper runs its mutation.
  deferWhileReading: vi.fn((mutate: () => void) => {
    mutate();
  }),
  fillViewport: vi.fn(),
  // The previous value is the real default, so a caller's restore round-trips.
  setPinSettleMs: vi.fn((_ms: number) => 700),
  // Non-zero: the turn rail hides below MIN_SCROLL_PX, so 0 would withdraw it from every suite.
  scrollableBy: vi.fn(() => 500),
};
