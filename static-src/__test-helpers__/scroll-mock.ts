// Canonical scroll.js mock: add an export here when scroll.ts gains one (scroll-mock.test.ts).
import { vi } from "vitest";
// Type-only, so the mocked module is not loaded; the types widen the mocks' return types (else
// `readingState` would be the literal "following" and "reading" would not typecheck).
import type { ReadingState, ViewScrollState } from "../scroll-controller.js";

export const scrollMock = {
  getScrollEl: vi.fn(() => document.createElement("div")),
  // The default snapshot is a fresh view's state, so a mocked park/unpark round-trips unprimed.
  attach: vi.fn(),
  detach: vi.fn((): ViewScrollState => ({ scrollTop: 0, readingState: "following" })),
  scrollToBottom: vi.fn(),
  pressHold: vi.fn(),
  jumpTo: vi.fn(),
  shiftScroll: vi.fn(),
  // No jump in flight: a landing's hold corrects at once.
  jumpInFlight: vi.fn(() => false),
  // `jumpToOffset` moves nothing here; a suite asserting a landing drives the scroller itself.
  jumpToOffset: vi.fn((): Promise<boolean> => Promise.resolve(true)),
  // Holds nothing, as the owner while Following: residency falls back to its own top-edge reading.
  readerAt: vi.fn((): Element | null => null),
  // The held change runs.
  heldMutation: vi.fn((fn: () => void) => {
    fn();
  }),
  followTargetChanged: vi.fn(),
  // A real answer: a suite asserting a landing must prime it, or the two coincide for any arithmetic.
  readingLineOffset: vi.fn(() => 0),
  resetScrollState: vi.fn(),
  setLoadMore: vi.fn(),
  noteHistoryEdge: vi.fn(),
  readingState: vi.fn((): ReadingState => "following"),
  onReadingStateChange: vi.fn(),
  // Inert registrations: a mocked scroller never fires them; each returns the promised unregister.
  onTranscriptMutate: vi.fn(() => () => undefined),
  onReaderGesture: vi.fn(() => () => undefined),
  onViewportChange: vi.fn(() => () => undefined),
  onLiveEdgeRequest: vi.fn(() => () => undefined),
  onContentResize: vi.fn(() => () => undefined),
  onAttach: vi.fn(() => () => undefined),
  setAnchorProvider: vi.fn(),
  setResumeLabel: vi.fn(),
  // The deferral helper runs its mutation.
  deferWhileReading: vi.fn((mutate: () => void) => {
    mutate();
  }),
  fillViewport: vi.fn(),
  // Non-zero: the turn rail hides below MIN_SCROLL_PX, so 0 would withdraw it from every suite.
  scrollableBy: vi.fn(() => 500),
  readerGestureOpen: vi.fn(() => false),
};
