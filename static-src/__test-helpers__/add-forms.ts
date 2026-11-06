import indexHtml from "../../static/index.html?raw";

const shipped = new DOMParser().parseFromString(indexHtml, "text/html");

/** A fresh copy of the shipped `.add-form` holding `#fieldId`, so a fixture keeps the page's real row. */
export function shippedAddForm(fieldId: string): HTMLFormElement {
  const form = shipped.querySelector(`[id="${fieldId}"]`)?.closest("form.add-form");
  if (form == null) {
    throw new Error(`static/index.html has no .add-form holding #${fieldId}`);
  }
  return document.importNode(form, true) as HTMLFormElement;
}

/** Type a value as a reader does: the row's button follows the `input` event, not the property write, and a
 *  disabled field takes no typing at all. */
export function fill(field: HTMLInputElement | HTMLSelectElement, value: string): void {
  if (field.disabled) {
    return;
  }
  field.value = value;
  field.dispatchEvent(new Event("input", { bubbles: true }));
}
