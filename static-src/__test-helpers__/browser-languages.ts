// Sets what `navigator.languages` reports, the one signal the shared time formatter reads.

const original = Object.getOwnPropertyDescriptor(navigator, "languages");

export function setBrowserLanguages(languages: readonly string[]): void {
  Object.defineProperty(navigator, "languages", { value: languages, configurable: true });
}

export function restoreBrowserLanguages(): void {
  if (original === undefined) {
    Reflect.deleteProperty(navigator, "languages");
    return;
  }
  Object.defineProperty(navigator, "languages", original);
}
