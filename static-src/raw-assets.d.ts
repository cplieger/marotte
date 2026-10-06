// Types for the two Vite import forms the tests use to read shipped assets as text: the `?raw`
// query suffix and `import.meta.glob`.

declare module "*?raw" {
  const contents: string;
  export default contents;
}

interface ImportMeta {
  /** Vite's build-time directory import. Returns one entry per matching file, keyed by the
   *  specifier as written relative to the importing module. */
  glob<T = unknown>(
    pattern: string | string[],
    options?: {
      query?: string;
      import?: string;
      eager?: boolean;
    },
  ): Record<string, T>;
}
