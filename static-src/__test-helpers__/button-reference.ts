// The size every button and menu row is held to, per pointer tier. The chat toolbar's icon buttons are the
// reference: on a 430pt iPhone they draw a 44px box (its own target) with a 20px glyph, 2px apart, a 46px pitch;
// a mouse keeps the 36px box and 16px glyph.

type Tier = "fine" | "coarse";

export interface ButtonReference {
  readonly box: number;
  readonly glyph: number;
  readonly gap: number;
}

export const BUTTON_REFERENCE: Readonly<Record<Tier, ButtonReference>> = {
  fine: { box: 36, glyph: 16, gap: 2 },
  coarse: { box: 44, glyph: 20, gap: 2 },
};

/** The button box on each tier. */
export const BUTTON_BOX: Readonly<Record<Tier, number>> = {
  fine: BUTTON_REFERENCE.fine.box,
  coarse: BUTTON_REFERENCE.coarse.box,
};

/** How much a glyph inside a button grows on touch: the reference glyph's step. */
export const GLYPH_STEP = BUTTON_REFERENCE.coarse.glyph - BUTTON_REFERENCE.fine.glyph;

/** A popup menu's single-line row: its height, its padding (block, inline) and its glyph. */
export const MENU_ROW: Readonly<
  Record<
    Tier,
    { readonly h: number; readonly pad: readonly [number, number]; readonly glyph: number }
  >
> = {
  fine: { h: 32, pad: [4, 8], glyph: 16 },
  coarse: { h: 44, pad: [4, 8], glyph: 16 },
};
