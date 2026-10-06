#!/usr/bin/env python3
"""Measure WCAG contrast between marotte's design tokens, per theme.

Resolves the token graph in static-src/css/01-tokens.css for both themes
(`:root` = dark, `:root[data-theme="light"]` = light), including `var()`
indirection, `oklch()` with alpha, and `color-mix()` in oklch and srgb, then
reports contrast ratios.

Colour path: OKLCH -> OKLab -> linear sRGB -> clip -> sRGB encode -> 8-bit ->
WCAG relative luminance. Clipping is a naive per-channel clamp where a browser
would gamut-map, so a heavily out-of-gamut colour is approximate; every token
measured here is in gamut (reported when it is not).

Usage:
  python3 scripts/css-contrast.py ramp          # the surface ramp, both themes
  python3 scripts/css-contrast.py pairs         # the interaction/selection pairs
  python3 scripts/css-contrast.py text          # text-on-surface AA checks
  python3 scripts/css-contrast.py ink           # every text ink x every surface text sits on
  python3 scripts/css-contrast.py selected      # the inks that sit ON a selected fill
  python3 scripts/css-contrast.py shadow        # elevation layers vs the surface below
  python3 scripts/css-contrast.py ansi          # the 16-colour ANSI palette vs its surface
  python3 scripts/css-contrast.py show TOKEN..  # resolve named tokens
  python3 scripts/css-contrast.py all
"""

from __future__ import annotations

import math
import re
import sys
from pathlib import Path

CSS_DIR = Path(__file__).resolve().parent.parent / "static-src" / "css"
TOKENS = CSS_DIR / "01-tokens.css"
ANSI_SHEET = CSS_DIR / "15-ansi.css"


class Colour:
    """A colour in gamma-encoded sRGB (0..1 per channel) plus alpha."""

    # Alphabetical: `__slots__` is a name set; `__init__` states the channel order.
    __slots__ = ("a", "b", "clipped", "g", "r")

    def __init__(
        self, r: float, g: float, b: float, a: float = 1.0, clipped: bool = False
    ):
        self.r, self.g, self.b, self.a, self.clipped = r, g, b, a, clipped

    def __repr__(self) -> str:
        return f"#{self.hex()}" + ("" if self.a >= 1 else f" a={self.a:.3f}")

    def hex(self) -> str:
        return "".join(
            f"{round(max(0.0, min(1.0, c)) * 255):02x}"
            for c in (self.r, self.g, self.b)
        )

    def over(self, bg: Colour) -> Colour:
        """Composite self over an opaque backdrop (CSS composites in sRGB)."""
        if self.a >= 1:
            return Colour(self.r, self.g, self.b, 1.0, self.clipped)
        t = self.a
        return Colour(
            self.r * t + bg.r * (1 - t),
            self.g * t + bg.g * (1 - t),
            self.b * t + bg.b * (1 - t),
            1.0,
            self.clipped or bg.clipped,
        )

    def luminance(self) -> float:
        """WCAG 2.x relative luminance, from the 8-bit value a display shows."""

        def lin(c: float) -> float:
            v = round(max(0.0, min(1.0, c)) * 255) / 255
            return v / 12.92 if v <= 0.04045 else ((v + 0.055) / 1.055) ** 2.4

        return 0.2126 * lin(self.r) + 0.7152 * lin(self.g) + 0.0722 * lin(self.b)


def srgb_encode(x: float) -> float:
    return 12.92 * x if x <= 0.0031308 else 1.055 * (x ** (1 / 2.4)) - 0.055


def srgb_decode(x: float) -> float:
    return x / 12.92 if x <= 0.04045 else ((x + 0.055) / 1.055) ** 2.4


def oklch_to_colour(L: float, C: float, H: float, a: float) -> Colour:
    h = math.radians(H)
    A, B = C * math.cos(h), C * math.sin(h)
    l_ = L + 0.3963377774 * A + 0.2158037573 * B
    m_ = L - 0.1055613458 * A - 0.0638541728 * B
    s_ = L - 0.0894841775 * A - 1.2914855480 * B
    l, m, s = l_**3, m_**3, s_**3
    r = 4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s
    g = -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s
    b = -0.0041960863 * l - 0.7034186147 * m + 1.7076147010 * s
    clipped = any(v < -1e-4 or v > 1 + 1e-4 for v in (r, g, b))
    return Colour(*(srgb_encode(max(0.0, min(1.0, v))) for v in (r, g, b)), a, clipped)


def colour_to_oklab(c: Colour) -> tuple[float, float, float]:
    r, g, b = (srgb_decode(v) for v in (c.r, c.g, c.b))
    l = 0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b
    m = 0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b
    s = 0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b
    l_, m_, s_ = (v ** (1 / 3) if v >= 0 else -((-v) ** (1 / 3)) for v in (l, m, s))
    return (
        0.2104542553 * l_ + 0.7936177850 * m_ - 0.0040720468 * s_,
        1.9779984951 * l_ - 2.4285922050 * m_ + 0.4505937099 * s_,
        0.0259040371 * l_ + 0.7827717662 * m_ - 0.8086757660 * s_,
    )


def colour_to_oklch(c: Colour) -> tuple[float, float, float]:
    L, A, B = colour_to_oklab(c)
    C = math.hypot(A, B)
    H = math.degrees(math.atan2(B, A)) % 360
    return L, C, H


def mix_oklch(c1: Colour, p1: float, c2: Colour) -> Colour:
    """color-mix(in oklch, c1 p1, c2 (1-p1)) with premultiplied alpha."""
    L1, C1, H1 = colour_to_oklch(c1)
    L2, C2, H2 = colour_to_oklch(c2)
    p2 = 1 - p1
    # Powerless hue when chroma is zero (CSS Color 4).
    if C1 < 1e-6:
        H1 = H2
    if C2 < 1e-6:
        H2 = H1
    d = ((H2 - H1 + 540) % 360) - 180  # shorter arc, CSS default
    H = (H1 + d * p2) % 360
    a = c1.a * p1 + c2.a * p2
    if a <= 1e-9:
        return Colour(0, 0, 0, 0)
    # Premultiplied interpolation: mixing with `transparent` must not darken.
    L = (L1 * c1.a * p1 + L2 * c2.a * p2) / a
    C = (C1 * c1.a * p1 + C2 * c2.a * p2) / a
    out = oklch_to_colour(L, C, H, a)
    return out


def mix_srgb(c1: Colour, p1: float, c2: Colour) -> Colour:
    p2 = 1 - p1
    a = c1.a * p1 + c2.a * p2
    if a <= 1e-9:
        return Colour(0, 0, 0, 0)
    ch = []
    for x, y in ((c1.r, c2.r), (c1.g, c2.g), (c1.b, c2.b)):
        ch.append((x * c1.a * p1 + y * c2.a * p2) / a)
    return Colour(ch[0], ch[1], ch[2], a, c1.clipped or c2.clipped)


def contrast(fg: Colour, bg: Colour) -> float:
    a, b = fg.luminance(), bg.luminance()
    lo, hi = min(a, b), max(a, b)
    return (hi + 0.05) / (lo + 0.05)


NAMED = {
    "transparent": Colour(0, 0, 0, 0),
    "white": Colour(1, 1, 1),
    "black": Colour(0, 0, 0),
    "currentcolor": None,
}


def split_args(s: str) -> list[str]:
    out, buf, depth = [], [], 0
    for ch in s:
        if ch == "(":
            depth += 1
        elif ch == ")":
            depth -= 1
        if ch == "," and depth == 0:
            out.append("".join(buf).strip())
            buf = []
        else:
            buf.append(ch)
    if "".join(buf).strip():
        out.append("".join(buf).strip())
    return out


def func_body(s: str, name: str) -> str | None:
    i = s.find(name + "(")
    if i < 0:
        return None
    depth, j = 0, i + len(name)
    while j < len(s):
        if s[j] == "(":
            depth += 1
        elif s[j] == ")":
            depth -= 1
            if depth == 0:
                return s[i + len(name) + 1 : j]
        j += 1
    return None


class Theme:
    def __init__(self, decls: dict[str, str], name: str):
        self.decls = decls
        self.name = name
        self._cache: dict[str, Colour] = {}

    def resolve(self, expr: str, _depth: int = 0) -> Colour:
        if _depth > 24:
            raise RecursionError(expr)
        e = " ".join(expr.split())
        low = e.lower()

        if low in NAMED and NAMED[low] is not None:
            return NAMED[low]

        if low.startswith("var("):
            inner = func_body(e, "var")
            assert inner is not None
            parts = split_args(inner)
            name = parts[0]
            if name in self.decls:
                return self.resolve(self.decls[name], _depth + 1)
            if len(parts) > 1:
                return self.resolve(parts[1], _depth + 1)
            raise KeyError(
                f"{name} is READ but never DECLARED in the {self.name} theme"
            )

        if low.startswith("over("):
            # Not CSS: `over(A, B)` is alpha compositing, what a browser paints when translucent A
            # sits on opaque B, which color-mix() is NOT.
            inner = func_body(e, "over")
            assert inner is not None
            fg, bg = (self.resolve(p, _depth + 1) for p in split_args(inner))
            return fg.over(bg)

        if low.startswith("color-mix("):
            inner = func_body(e, "color-mix")
            assert inner is not None
            args = split_args(inner)
            space = args[0].strip().lower()
            if not space.startswith("in "):
                raise ValueError(f"color-mix needs a space: {e}")
            space = space[3:].strip().split()[0]
            c_specs = args[1:]
            colours, pcts = [], []
            for spec in c_specs:
                m = re.search(r"(-?[\d.]+)%\s*$", spec)
                if m:
                    pcts.append(float(m.group(1)) / 100)
                    colours.append(self.resolve(spec[: m.start()].strip(), _depth + 1))
                else:
                    pcts.append(None)
                    colours.append(self.resolve(spec, _depth + 1))
            if len(colours) != 2:
                raise ValueError(f"expected 2 colours: {e}")
            p0, p1 = pcts
            if p0 is None and p1 is None:
                p0 = p1 = 0.5
            elif p0 is None:
                p0 = 1 - p1
            elif p1 is None:
                p1 = 1 - p0
            total = p0 + p1
            if total <= 0:
                raise ValueError(f"zero-weight mix: {e}")
            w0 = p0 / total
            if space == "oklch":
                return mix_oklch(colours[0], w0, colours[1])
            if space in ("srgb", "srgb-linear"):
                return mix_srgb(colours[0], w0, colours[1])
            raise ValueError(f"unsupported mix space {space}: {e}")

        if low.startswith("oklch(from "):
            return self._relative_oklch(e, _depth)

        if low.startswith("oklch("):
            inner = func_body(e, "oklch")
            assert inner is not None
            alpha = 1.0
            if "/" in inner:
                inner, av = inner.rsplit("/", 1)
                av = av.strip()
                alpha = float(av[:-1]) / 100 if av.endswith("%") else float(av)
            nums = inner.replace("deg", " ").split()
            L = float(nums[0][:-1]) / 100 if nums[0].endswith("%") else float(nums[0])
            C = float(nums[1])
            # `none` is a MISSING hue, the only spelling that makes it powerless in color-mix(); an
            # explicit `0deg` gets interpolated. Zero is safe because mix_oklch re-derives a
            # powerless hue from the other operand whenever chroma is zero.
            H = 0.0
            if len(nums) > 2 and nums[2] != "none":
                H = float(nums[2])
            return oklch_to_colour(L, C, H, alpha)

        if low.startswith(("rgb(", "rgba(")):
            inner = func_body(e, "rgba" if low.startswith("rgba(") else "rgb")
            assert inner is not None
            alpha = 1.0
            if "/" in inner:
                inner, av = inner.rsplit("/", 1)
                av = av.strip()
                alpha = float(av[:-1]) / 100 if av.endswith("%") else float(av)
            parts = [p for p in re.split(r"[,\s]+", inner.strip()) if p]
            if len(parts) == 4:  # legacy rgba(r, g, b, a)
                av = parts.pop()
                alpha = float(av[:-1]) / 100 if av.endswith("%") else float(av)
            ch = [
                float(p[:-1]) / 100 if p.endswith("%") else float(p) / 255
                for p in parts
            ]
            return Colour(ch[0], ch[1], ch[2], alpha)

        m = re.fullmatch(r"#([0-9a-fA-F]{3,8})", e)
        if m:
            h = m.group(1)
            if len(h) == 3:
                h = "".join(c * 2 for c in h)
            vals = [int(h[i : i + 2], 16) / 255 for i in range(0, len(h), 2)]
            a = vals[3] if len(vals) > 3 else 1.0
            return Colour(vals[0], vals[1], vals[2], a)

        raise ValueError(f"cannot resolve {e!r}")

    def _relative_oklch(self, e: str, depth: int) -> Colour:
        """`oklch(from <colour> <l> <c> <h> [/ <a>])`, CSS Color 5 relative colour
        syntax. Each channel is the keyword naming the origin's own channel (`l`,
        `c`, `h`, `alpha`), a literal (`35.5%`, `0.04`, `86.5deg`), or a
        `calc(<keyword> +|- <number>)` over one. This is what lets a tint hold a
        surface's LIGHTNESS and spend chroma, which `color-mix()` cannot: mixing
        a pastel into a dark rung lifts it toward the ink (01-tokens.css names the
        trap at the diff backgrounds), so a hue wash over the band ended up as deep
        as the elevated fill and took hint ink under AA."""
        inner = func_body(e, "oklch")
        assert inner is not None
        body = inner.strip()[len("from") :].strip()
        # The origin colour is the first top-level argument; split on whitespace
        # outside parentheses.
        depth_n, cut = 0, None
        for i, ch in enumerate(body):
            if ch == "(":
                depth_n += 1
            elif ch == ")":
                depth_n -= 1
            elif ch.isspace() and depth_n == 0:
                cut = i
                break
        if cut is None:
            raise ValueError(f"relative colour with no channels: {e}")
        origin = self.resolve(body[:cut], depth + 1)
        rest = body[cut:].strip()
        alpha_expr = None
        if "/" in rest:
            rest, alpha_expr = rest.rsplit("/", 1)
        L0, C0, H0 = colour_to_oklch(origin)
        base = {"l": L0 * 100, "c": C0, "h": H0, "alpha": origin.a}
        chans = [p for p in re.split(r"\s+(?![^()]*\))", rest.strip()) if p]
        if len(chans) != 3:
            raise ValueError(f"relative oklch wants l c h, got {chans!r}: {e}")

        def chan(expr: str, name: str) -> float:
            x = expr.strip().lower()
            if x in base:
                return base[x]
            m = re.fullmatch(
                r"calc\(\s*(l|c|h|alpha)\s*([+-])\s*([\d.]+)(%|deg)?\s*\)", x
            )
            if m:
                v = base[m.group(1)]
                d = float(m.group(3))
                return v + d if m.group(2) == "+" else v - d
            if x.endswith("%"):
                return float(x[:-1])
            if x.endswith("deg"):
                return float(x[:-3])
            if x == "none":
                return 0.0
            return float(x)

        L = chan(chans[0], "l") / 100
        C = chan(chans[1], "c")
        H = chan(chans[2], "h")
        a = chan(alpha_expr, "alpha") if alpha_expr is not None else origin.a
        return oklch_to_colour(L, C, H, a)

    def colour(self, token: str) -> Colour:
        if token not in self._cache:
            if token not in self.decls:
                raise KeyError(f"{token} is not declared in the {self.name} theme")
            self._cache[token] = self.resolve(self.decls[token])
        return self._cache[token]

    def flat(self, token: str, backdrop: str | None = None) -> Colour:
        """A token composited over a backdrop token if it is translucent."""
        c = self.colour(token) if token.startswith("--") else self.resolve(token)
        if c.a < 1 and backdrop:
            c = c.over(self.flat(backdrop))
        return c


def block_body(src: str, start: int) -> str:
    i = src.index("{", start)
    depth = 0
    for j in range(i, len(src)):
        if src[j] == "{":
            depth += 1
        elif src[j] == "}":
            depth -= 1
            if depth == 0:
                return src[i + 1 : j]
    raise ValueError("unbalanced")


def parse_themes() -> tuple[Theme, Theme]:
    src = re.sub(r"/\*.*?\*/", " ", TOKENS.read_text(), flags=re.DOTALL)

    def decls_of(body: str) -> dict[str, str]:
        # strip nested at-rule blocks so a nested @media does not leak in
        out, depth, buf = {}, 0, []
        i = 0
        while i < len(body):
            ch = body[i]
            if ch == "{":
                depth += 1
            elif ch == "}":
                depth -= 1
            elif depth == 0:
                buf.append(ch)
            i += 1
        for d in "".join(buf).split(";"):
            d = d.strip()
            if d.startswith("--") and ":" in d:
                k, v = d.split(":", 1)
                out[k.strip()] = v.strip()
        return out

    root = re.search(r":root\s*\{", src)
    assert root
    dark = decls_of(block_body(src, root.start()))

    light = dict(dark)
    m = re.search(r':root\[data-theme="light"\]\s*\{', src)
    if m:
        light.update(decls_of(block_body(src, m.start())))
    return Theme(dark, "dark"), Theme(light, "light")


def fmt(v: float) -> str:
    return f"{v:.3f}"


def show_ramp(themes: list[Theme], rungs: list[str]) -> None:
    for th in themes:
        present = [r for r in rungs if r in th.decls]
        print(f"  {th.name}:")
        print(f"    {'token':<22} {'hex':<9} {'Y':<8}  step vs previous")
        prev = None
        for r in present:
            c = th.flat(r)
            step = "" if prev is None else f"{fmt(contrast(c, prev))}:1"
            flag = "  OUT OF GAMUT" if c.clipped else ""
            print(f"    {r:<22} {c.hex():<9} {fmt(c.luminance()):<8}  {step}{flag}")
            prev = c
        if len(present) >= 2:
            e2e = contrast(th.flat(present[0]), th.flat(present[-1]))
            print(f"    end to end ({present[0]} -> {present[-1]}): {fmt(e2e)}:1")
        print()


# Where a well sits: the turn body and a box. The page and the band never host one.
WELL_HOSTS = ("--c-turn-body", "--c-bg-secondary")

# The dark ramp's page-to-card half-step is 4.0 oklch-L; a well must drop at least that.
# No shadow reaches 1.15:1 below the body, so the ratio is reported, not gated.
WELL_MIN_DROP = 4.0


def show_well(themes: list[Theme]) -> None:
    print("THE WELL (--c-well) vs the two rungs that host it: an oklch-L DROP,")
    print(f"  floored at {WELL_MIN_DROP} points, the ramp's own smallest step")
    for th in themes:
        print(f"  {th.name}:")
        for host in WELL_HOSTS:
            hb = th.flat(host)
            well = th.flat("--c-well").over(hb)
            drop = (colour_to_oklch(hb)[0] - colour_to_oklch(well)[0]) * 100
            verdict = (
                "PASS" if drop >= WELL_MIN_DROP else f"FAIL (want {WELL_MIN_DROP})"
            )
            print(
                f"    well on {host.replace('--c-', ''):<14} drop {drop:5.1f} L"
                f"  ratio {fmt(contrast(well, hb))}:1  [{well.hex()} vs {hb.hex()}]  {verdict}"
            )
        print()


# Every text ink is authored against the hovered box, so one (ink, surface) table is the
# contract. The press fill, HOVERED band and selected fill host only primary and
# secondary: floored for those two, reported bare for the rest.
INK_TEXT: list[str] = [
    "--c-text-secondary",
    "--c-text-tertiary",
    "--c-green",
    "--c-red",
    "--c-yellow",
    "--c-blue",
    "--c-danger",
    "--c-warning",
    "--c-link",
    "--c-teal",
]
INK_TWO_LEVEL: list[str] = ["--c-text-primary", "--c-text-secondary"]
INK_SURFACES: list[tuple[str, str]] = [
    ("page", "var(--c-bg-primary)"),
    ("card", "var(--c-turn-body)"),
    ("box", "var(--c-bg-secondary)"),
    ("band", "var(--c-bg-tertiary)"),
    ("band(stopped)", "var(--c-band-stopped)"),
    ("band(broken)", "var(--c-band-broken)"),
    ("band(added)", "var(--c-band-added)"),
    ("hover(page)", "over(var(--c-hover), var(--c-bg-primary))"),
    ("hover(card)", "over(var(--c-hover), var(--c-turn-body))"),
    ("hover(box)", "over(var(--c-hover), var(--c-bg-secondary))"),
    ("hover-select(box)", "over(var(--c-hover-select), var(--c-bg-secondary))"),
]
INK_TWO_LEVEL_SURFACES: list[tuple[str, str]] = [
    ("elevated", "var(--c-bg-elevated)"),
    ("hover(band)", "over(var(--c-hover), var(--c-bg-tertiary))"),
    ("selected", "var(--c-selected-bg)"),
]
INK_FLOOR = 4.5


def show_ink_ramp(themes: list[Theme]) -> None:
    print("INK RAMP (WCAG 1.4.3: every text ink clears 4.5:1 on every surface text")
    print("sits on, hovered included; the three two-level surfaces host primary and")
    print("secondary only and are floored for those two alone)")
    for th in themes:
        print(f"  {th.name}:")
        inks = {t: th.flat(t) for t in INK_TEXT}
        pri = th.flat("--c-text-primary")
        head = "".join(
            f"{t.replace('--c-text-', '').replace('--c-', ''):>10}" for t in INK_TEXT
        )
        print(f"    {'surface':<18}{head}")
        for name, expr in INK_SURFACES:
            s = th.resolve(expr)
            row = ""
            for c in inks.values():
                r = contrast(c, s)
                row += f"{fmt(r):>9}{' ' if r >= INK_FLOOR else '!'}"
            print(f"    {name:<18}{row}")
        fails = [
            f"{t} on {name}"
            for name, expr in INK_SURFACES
            for t in INK_TEXT
            if contrast(th.flat(t), th.resolve(expr)) < INK_FLOOR
        ]
        print(f"    => {'PASS' if not fails else 'FAIL: ' + ', '.join(fails)}")
        print(
            "    two-level surfaces (primary / secondary floored; the rest reported):"
        )
        for name, expr in INK_TWO_LEVEL_SURFACES:
            s = th.resolve(expr)
            two = []
            for t in INK_TWO_LEVEL:
                r = contrast(th.flat(t), s)
                two.append(
                    f"{t.replace('--c-text-', '')} {fmt(r)}:1 {'PASS' if r >= INK_FLOOR else 'FAIL'}"
                )
            hint = contrast(inks["--c-text-tertiary"], s)
            print(f"    {name:<18}{'  '.join(two)}   (hint {fmt(hint)}:1)")
        sec, ter = th.flat("--c-text-secondary"), th.flat("--c-text-tertiary")
        print(
            f"    steps: hint->secondary {fmt(contrast(sec, ter))}:1"
            f"  secondary->primary {fmt(contrast(pri, sec))}:1"
            f"   hint sRGB {ter.hex()} (the --img-chevron stroke literal)"
        )
        print()


def show_pairs(
    themes: list[Theme], pairs: list[tuple[str, str, str, float | None]]
) -> None:
    """Contrast each pair, compositing a translucent first colour ONTO the second.

    A wash measured as if it were opaque is not a measurement of anything: a 16%
    white border read 19.7:1 against the base before this composited it, when
    what a reader sees is 1.5:1. `b` is the backdrop, so `a` over `b` is the
    physical arrangement for every pair here (a border on a surface, an inset on
    a surface, ink on a fill).
    """
    for th in themes:
        print(f"  {th.name}:")
        for label, a, b, floor in pairs:
            try:
                cb = th.flat(b)
                ca = th.flat(a)
                if ca.a < 1:
                    ca = ca.over(cb)
            except (KeyError, ValueError) as exc:
                print(f"    {label:<44} n/a  ({exc})")
                continue
            r = contrast(ca, cb)
            verdict = ""
            if floor is not None:
                verdict = "  PASS" if r >= floor else f"  FAIL (want {floor}:1)"
            print(f"    {label:<44} {fmt(r)}:1  [{ca.hex()} vs {cb.hex()}]{verdict}")
        print()


# Inks ON a selected fill: metadata and status glyphs declare their own colour, so each
# needs an on-selected variant. Muted metadata mixes toward the FILL; a status hue mixes
# toward the row's INK, which `color-mix(in oklch)` does without moving H.
SELECTED_INKS: list[tuple[str, str, str]] = [
    # (token, the colour it is mixed with, the direction label)
    ("--c-selected-muted-fg", "var(--c-selected-bg)", "toward fill"),
    ("--c-selected-green-fg", "var(--c-green)", "toward ink"),
    ("--c-selected-red-fg", "var(--c-red)", "toward ink"),
    ("--c-selected-yellow-fg", "var(--c-yellow)", "toward ink"),
    ("--c-selected-blue-fg", "var(--c-blue)", "toward ink"),
]

# The floor is resting AND hover (where a pointer rests); press is reported, not held.
SELECTED_FILLS = ["--c-selected-bg", "--c-selected-bg-hover", "--c-selected-bg-press"]
HELD_FILLS = SELECTED_FILLS[:2]


def selected_expr(other: str, pct: int) -> str:
    return f"color-mix(in oklch, var(--c-selected-fg) {pct}%, {other})"


def show_selected(themes: list[Theme]) -> None:
    print("ON-SELECTED INK (WCAG 1.4.3: 4.5:1 for the small text every one of")
    print("these is; the floor is the resting AND hovered fill, not the pressed one)")
    print()
    for th in themes:
        print(f"  {th.name}:")
        print(f"    {'ink':<26} {'resting':<12} {'hover':<12} {'press':<12} verdict")
        for tok, _other, _dir in SELECTED_INKS:
            if tok not in th.decls:
                print(f"    {tok:<26} not declared")
                continue
            ink = th.colour(tok)
            cells, held = [], []
            for fill in SELECTED_FILLS:
                r = contrast(ink, th.flat(fill))
                cells.append(f"{fmt(r)}:1")
                if fill in HELD_FILLS:
                    held.append(r)
            verdict = (
                "PASS" if min(held) >= 4.5 else f"FAIL (held min {fmt(min(held))}:1)"
            )
            print(
                f"    {tok:<26} {cells[0]:<12} {cells[1]:<12} {cells[2]:<12} {verdict}"
            )
        # The row's own ink, for the hierarchy check below.
        own = th.colour("--c-selected-fg")
        print(
            f"    {'--c-selected-fg (the row)':<26} "
            + " ".join(f"{fmt(contrast(own, th.flat(f))):<11}" for f in SELECTED_FILLS)
        )
        print()

    print("FRACTION SWEEP: the smallest 1%-step fraction clearing 4.5:1 on the")
    print("resting AND hovered fill in BOTH themes. This is what sizes the tokens.")
    print()
    print(f"  {'construction':<44} {'min %':<7} {'dark':<10} {'light':<10} held min")
    for tok, other, direction in SELECTED_INKS:
        best = None
        for pct in range(1, 101):
            expr = selected_expr(other, pct)
            worst = min(
                contrast(th.resolve(expr), th.flat(fill))
                for th in themes
                for fill in HELD_FILLS
            )
            if worst >= 4.5:
                best = (pct, worst)
                break
        label = f"{tok.replace('--c-selected-', '')} ({direction})"
        if best is None:
            print(f"  {label:<44} none    -          -          cannot reach 4.5:1")
            continue
        pct, worst = best
        per = [
            fmt(
                min(
                    contrast(th.resolve(selected_expr(other, pct)), th.flat(f))
                    for f in HELD_FILLS
                )
            )
            for th in themes
        ]
        print(f"  {label:<44} {pct:<7} {per[0]:<10} {per[1]:<10} {fmt(worst)}:1")
    print()

    print("HIERARCHY: muted metadata must stay visibly quieter than the row's own")
    print("ink, or the distinction it exists to express is gone.")
    for th in themes:
        if "--c-selected-muted-fg" not in th.decls:
            continue
        sep = contrast(th.colour("--c-selected-muted-fg"), th.colour("--c-selected-fg"))
        print(f"  {th.name:<6} muted vs the row's ink: {fmt(sep)}:1")
    print()

    print("HUE PRESERVATION: a status ink that no longer reads as its status is")
    print("worse than a dim one, so the mix must move L and leave H alone.")
    for th in themes:
        print(f"  {th.name}:")
        for tok, other, direction in SELECTED_INKS:
            if direction != "toward ink" or tok not in th.decls:
                continue
            seed = other[4:-1] if other.startswith("var(") else other
            h_before = colour_to_oklch(th.colour(seed))[2]
            h_after = colour_to_oklch(th.colour(tok))[2]
            drift = ((h_after - h_before + 540) % 360) - 180
            print(
                f"    {seed:<16} {h_before:6.1f}deg -> {h_after:6.1f}deg  "
                f"drift {drift:+.1f}deg"
            )
        print()


# Elevation: a black shadow cannot darken a near-black base (~1.0:1), so an ambient layer
# derived off the INK carries the separation (a rim on dark, a hairline on light).
SHADOW_SITES: list[tuple[str, str]] = [
    # (label, the shadow colour as authored)
    ("uip-toast", "color-mix(in srgb, #000 25%, transparent)"),
    ("uip-tooltip", "oklch(0% 0 0deg / 40%)"),
    ("uip-ask (modal)", "oklch(0% 0 0deg / 50%)"),
    ("tab-drag-ghost", "oklch(0% 0 0deg / 25%)"),
    ("popup", "oklch(0% 0 0deg / 50%)"),
    ("tab-context-menu", "oklch(0% 0 0deg / 25%)"),
    ("scroll-to-bottom", "oklch(0% 0 0deg / 30%)"),
    ("pill-expand-content", "oklch(0% 0 0deg / 12%)"),
    ("git-ci-panel", "rgb(0 0 0 / 22%)"),
    ("chip-menu", "oklch(0% 0 0deg / 40%)"),
    ("git-branch-popover", "oklch(0% 0 0deg / 30%)"),
    ("chat-find (inner)", "rgb(0 0 0 / 20%)"),
    ("chat-find (outer)", "rgb(0 0 0 / 28%)"),
]

# Candidates for the ambient layer; the border wash already inverts per theme.
AMBIENT_CANDIDATES = [
    "--c-border",
    "--c-hover",
    "--c-elevation-ambient",
]


# The tab activity dot (12-tabs.css / 70-selection.css). A status dot is a graphic, so
# its floor is WCAG 1.4.11's 3:1. One ink paints it on all five row fills, because
# selection belongs to the ROW, never to the status ink. So the floor is the two
# unselected fills and the selected rungs are REPORTED.

# The fills a tab row can present; only the two UNSELECTED ones are held.
DOT_FILLS: list[tuple[str, str]] = [
    ("resting", "--c-bg-secondary"),
    # The tab strip's hover is `--c-hover-select`, the tinted rung a tab-shaped control takes.
    ("hover", "over(var(--c-hover-select), var(--c-bg-secondary))"),
]
DOT_SELECTED_FILLS: list[tuple[str, str]] = [
    ("selected", "--c-selected-bg"),
    ("sel+hover", "--c-selected-bg-hover"),
    ("sel+press", "--c-selected-bg-press"),
]
DOT_HELD = ["resting", "hover"]

# (state, ink, channels). `channels` is the state's NON-COLOUR identity transcribed from
# the CSS (fill, surround, motion, shape, band), so it is CHECKED, not derived: the
# pairwise test fails if two co-present states differ by hue alone (WCAG 1.4.1), run
# with and without motion. `band` is needed because the workflow mark is a ring in every
# state; its pairs gate on BAND_RATIO_FLOOR, not inequality.
DOT_STATES: list[tuple[str, str, dict[str, str]]] = [
    (
        "idle",
        "var(--c-dot-idle)",
        {
            "fill": "hollow",
            "surround": "none",
            "motion": "still",
            "shape": "circle",
            "band": "1.5",
        },
    ),
    (
        "working",
        "--c-dot-working",
        {
            "fill": "solid",
            "surround": "none",
            "motion": "animated",
            "shape": "circle",
            "band": "",
        },
    ),
    (
        "waiting",
        "--c-dot-input",
        {
            "fill": "hollow",
            "surround": "ring",
            "motion": "still",
            "shape": "circle",
            "band": "1.5",
        },
    ),
    (
        "input",
        "--c-dot-input",
        {
            "fill": "solid",
            "surround": "ring",
            "motion": "still",
            "shape": "circle",
            "band": "",
        },
    ),
    (
        "failed",
        "--c-dot-failed",
        {
            "fill": "solid",
            "surround": "none",
            "motion": "still",
            "shape": "diamond",
            "band": "",
        },
    ),
    (
        "done",
        "--c-dot-done",
        {
            "fill": "solid",
            "surround": "none",
            "motion": "still",
            "shape": "circle",
            "band": "",
        },
    ),
    # Editor tabs only: never in a chat strip position, so the pairwise check excludes it.
    (
        "dirty",
        "--c-accent",
        {
            "fill": "solid",
            "surround": "none",
            "motion": "still",
            "shape": "circle",
            "band": "",
        },
    ),
]

# The WORKFLOW MARK (12-tabs.css `.tab-run-dot`), adjacent to and always co-present with
# the dot above, so both go through one pairwise pass. It shares the dot's inks; what it
# must not share is a channel signature. A ring in every state, it separates on `band`,
# and its rounded-square silhouette answers every cross-mark pair, reduced motion included.
RUN_MARK_STATES: list[tuple[str, str, dict[str, str]]] = [
    (
        "working",
        "--c-dot-working",
        {
            "fill": "hollow",
            "surround": "none",
            "motion": "animated",
            "shape": "square",
            "band": "2",
        },
    ),
    (
        "waiting",
        "--c-dot-input",
        {
            "fill": "hollow",
            "surround": "ring",
            "motion": "still",
            "shape": "square",
            "band": "1",
        },
    ),
    (
        "input",
        "--c-dot-input",
        {
            "fill": "hollow",
            "surround": "ring",
            "motion": "still",
            "shape": "square",
            "band": "2",
        },
    ),
]

# A RUN's OWN ROW, its own population (never co-present with `.tab-run-dot`), named by
# MEMBER rather than re-transcribed: the mark's three live states plus the dot's two
# outcomes. FILL says finished-or-not here and BAND does the wants-you work. Every
# non-`failed` member renders square, so the shapes are substituted.
RUN_ROW_MEMBERS = ["run:working", "run:waiting", "run:input", "dot:done", "dot:failed"]

# No aliases: `waiting` and `input` share one ink ("action required") and separate on
# FILL, which the pairwise check verifies. An entry here is a state the check ignores.
DOT_ALIASES: list[tuple[str, str]] = []

# `dirty` is an editor-tab state; every other member is a chat state.
DOT_CHAT_ONLY = "dirty"

DOT_FLOOR = 3.0

# Under reduced motion an animated state loses motion and gains a hole, NOT a ring: the
# ring is the wants-you marker.
REDUCED_MOTION_SUBSTITUTION = {"fill": "donut", "motion": "still", "band": "2.2"}

# The mark's `working` is already hollow, so its motion is replaced by the heaviest band.
RUN_REDUCED_MOTION_SUBSTITUTION = {"motion": "still", "band": "3"}

# Band-only separations must clear this ratio (the exec view's 2px vs Chromium's 1px snap
# is the precedent): a 1.5px/2px pair is measurable here and invisible to a reader.
BAND_RATIO_FLOOR = 2.0

# The sibling app's own --status-* declaration per state (web-terminal-kiro's
# static-src/app.ts, not the library defaults it overrides) and the marotte token
# answering it, carried as written. The claim is the VALUE where in reach and the HUE
# where not; `show_dot_sizing` prints which.
DOT_SOURCE_HUES = [
    ("working", "--c-dot-working", "--status-working", "#c6a0ff"),
    ("input", "--c-dot-input", "--status-input", "oklch(78% 0.15 95deg)"),
    ("failed", "--c-dot-failed", "--status-failed", "#dc2626"),
    ("done", "--c-dot-done", "--status-done", "oklch(78% 0.15 150deg)"),
]

# The sweep's space when a source value misses the floor: every in-gamut L and C at the
# source's hue, ranked nearest-L first, so a value stays as close to the source as
# contrast allows.
DOT_SWEEP_LIGHTNESS = range(30, 93)
DOT_SWEEP_CHROMA = (0.05, 0.28, 0.005)

# The favicon is an opaque flat-filled rect, so the badge is never seen against a theme
# surface and one icon serves both themes. The backdrop is read from the shipped asset:
# no token holds its brand violet.
FAVICON_SVG = Path(__file__).resolve().parent.parent / "static" / "favicon.svg"
FAVICON_CUES = [
    ("input", "--c-dot-input"),
    ("done", "--c-dot-done"),
    ("alert", "--c-dot-failed"),
]


def favicon_backdrop() -> str:
    """The flat fill of the base favicon's background rect."""
    m = re.search(r"<rect\b[^>]*\bfill=\"(#[0-9a-fA-F]{6})\"", FAVICON_SVG.read_text())
    if m is None:
        raise SystemExit(f"no background rect fill in {FAVICON_SVG}")
    return m.group(1)


def as_expr(ink: str) -> str:
    """A bare token name becomes a var() reference; an expression passes through."""
    return f"var({ink})" if ink.startswith("--") else ink


def show_dot(themes: list[Theme]) -> None:
    print("TAB ACTIVITY DOT (WCAG 1.4.11: 3:1 for a graphical object; the floor is")
    print("the two UNSELECTED fills, and the three selected rungs are reported —")
    print("selection never re-tints a status ink, so their lower ratios are an")
    print("accepted cost rather than a regression this can gate)")
    print()
    for th in themes:
        print(f"  {th.name}:")
        cols = [c for c, _ in DOT_FILLS + DOT_SELECTED_FILLS]
        print(
            f"    {'state':<10} {'ink':<20} "
            + " ".join(f"{c:<11}" for c in cols)
            + " verdict"
        )
        for state, ink, _ch in DOT_STATES:
            row, held = [], []
            for col, fill in DOT_FILLS + DOT_SELECTED_FILLS:
                bg = th.flat(fill)
                r = contrast(th.resolve(as_expr(ink)).over(bg), bg)
                row.append(f"{fmt(r)}:1")
                if col in DOT_HELD:
                    held.append((col, r))
            worst_col, worst = min(held, key=lambda kv: kv[1])
            verdict = (
                "PASS"
                if worst >= DOT_FLOOR
                else f"FAIL ({worst_col} {fmt(worst)}:1, want {DOT_FLOOR}:1)"
            )
            print(
                f"    {state:<10} {ink:<20} "
                + " ".join(f"{c:<11}" for c in row)
                + f" {verdict}"
            )
        print()

    print("IDLE-RING SWEEP: the smallest 1%-step ink fraction clearing 3:1 on the")
    print("UNSELECTED fills in BOTH themes, which is the whole floor — a selected")
    print("row paints this same ring rather than an on-selected substitute.")
    print()
    print(f"  {'construction':<52} {'min %':<7} held min")
    for base in ("var(--c-text-primary)", "var(--c-text-secondary)"):
        best = None
        for pct in range(1, 101):
            expr = f"color-mix(in oklch, {base} {pct}%, transparent)"
            worst = min(
                contrast(th.resolve(expr).over(th.flat(fill)), th.flat(fill))
                for th in themes
                for col, fill in DOT_FILLS
                if col in DOT_HELD
            )
            if worst >= DOT_FLOOR:
                best = (pct, worst)
                break
        label = f"color-mix(in oklch, {base} N%, transparent)"
        if best is None:
            print(f"  {label:<52} none    cannot reach {DOT_FLOOR}:1")
            continue
        print(f"  {label:<52} {best[0]:<7} {fmt(best[1])}:1")
    print()

    show_dot_hues(themes)
    show_dot_sizing(themes)

    print("NON-COLOUR CHANNELS (WCAG 1.4.1: colour may not be the only means of")
    print("conveying a state). Every pair of marks ONE ROW can present must differ on")
    print("at least one of fill / surround / motion / shape / band, with motion")
    print("available AND removed. Two rows present more than one mark: a CHAT row's")
    print("activity dot and workflow mark sit 8px apart and are permanently")
    print("co-present, and a RUN sub-tab's dot answers with a mix of both marks'")
    print("states (RUN_ROW_MEMBERS), so each row is its own pass. A pair separated")
    print(
        f"by band ALONE must clear {BAND_RATIO_FLOOR}:1, the exec column's own ratio."
    )
    print("The mark's inks are the dot's own tokens, so the ratio table above already")
    print("measures them: a band has less AREA than a disc, not a different ratio.")
    print()
    print(
        f"  {'mark':<15} {'fill':<8} {'surround':<9} {'motion':<10} {'shape':<8} band"
    )
    for element, states in (("dot", DOT_STATES), ("run", RUN_MARK_STATES)):
        for state, _ink, ch in states:
            tag = "  (editor tab)" if state == DOT_CHAT_ONLY else ""
            band = f"{ch['band']}px" if ch["band"] else "-"
            print(
                f"  {element + ':' + state:<15} {ch['fill']:<8} {ch['surround']:<9} "
                f"{ch['motion']:<10} {ch['shape']:<8} {band}{tag}"
            )
    print()

    print(f"  a RUN's own row presents: {', '.join(RUN_ROW_MEMBERS)}")
    print()

    aliases = {frozenset(p) for p in DOT_ALIASES}

    def cluster_verdict(
        population: list[tuple[str, dict[str, str], bool]], reduce_motion: bool
    ) -> str:
        """One pairwise pass over the marks ONE row can present."""
        resolved = []
        for state, ch, is_run in population:
            eff = dict(ch)
            if reduce_motion and ch["motion"] == "animated":
                eff.update(
                    RUN_REDUCED_MOTION_SUBSTITUTION
                    if is_run
                    else REDUCED_MOTION_SUBSTITUTION
                )
            resolved.append((state, eff))
        collisions, thin = [], []
        for i, (a, ca) in enumerate(resolved):
            for b, cb in resolved[i + 1 :]:
                if frozenset((a, b)) in aliases:
                    continue
                if ca == cb:
                    collisions.append((a, b))
                    continue
                # A band-ONLY difference is a separation a reader may not see, so it is gated on the ratio.
                differing = [k for k in ca if ca[k] != cb[k]]
                if differing != ["band"] or "" in (ca["band"], cb["band"]):
                    continue
                lo, hi = sorted((float(ca["band"]), float(cb["band"])))
                if lo <= 0 or hi / lo < BAND_RATIO_FLOOR:
                    thin.append((a, b, lo, hi))
        if collisions:
            pairs = ", ".join(f"{a}/{b}" for a, b in collisions)
            return f"FAIL  hue is the only separator for: {pairs}"
        if thin:
            pairs = ", ".join(f"{a}/{b} ({lo}px vs {hi}px)" for a, b, lo, hi in thin)
            return (
                f"FAIL  band is the only separator and it is under "
                f"{BAND_RATIO_FLOOR}:1 for: {pairs}"
            )
        return "PASS  every pair differs on a non-colour channel"

    # A CHAT row is ONE population: the two marks share a row, so a cross-element pair is as
    # confusable as two states of one element.
    chat = [(f"dot:{s}", ch, False) for s, _ink, ch in DOT_STATES if s != DOT_CHAT_ONLY]
    chat += [(f"run:{s}", ch, True) for s, _ink, ch in RUN_MARK_STATES]

    # A RUN row is assembled by NAME; the kind-scoped rule squares every state but `failed`.
    channels = {f"dot:{s}": ch for s, _ink, ch in DOT_STATES}
    channels.update({f"run:{s}": ch for s, _ink, ch in RUN_MARK_STATES})
    run_row = []
    for label in RUN_ROW_MEMBERS:
        ch = dict(channels[label])
        if ch["shape"] == "circle":
            ch["shape"] = "square"
        run_row.append((label, ch, label.startswith("run:")))

    for pass_name, reduce_motion in (
        ("motion available", False),
        ("prefers-reduced-motion", True),
    ):
        for row_name, population in (("chat row", chat), ("run row", run_row)):
            label = f"{row_name}, {pass_name}"
            print(f"  {label:<40} {cluster_verdict(population, reduce_motion)}")
    for a, b in DOT_ALIASES:
        print(
            f"  {'aliased on purpose':<24} {a}/{b} share one visual; they differ in the announced name"
        )
    print()

    print("GREYSCALE ORDERING, for reference: what a reader with no hue perception")
    print("sees. It is NOT the guard — the channel matrix above is — but a pair that")
    print("also separates here separates twice.")
    for th in themes:
        rows = []
        for state, ink, _ch in DOT_STATES:
            if state == DOT_CHAT_ONLY:
                continue
            c = th.resolve(as_expr(ink)).over(th.flat("--c-bg-secondary"))
            rows.append((state, c.luminance(), c.hex()))
        rows.sort(key=lambda r: -r[1])
        print(f"  {th.name}:")
        for state, y, hexv in rows:
            print(f"    {state:<10} Y={fmt(y):<8} {hexv}")
    print()

    show_favicon_badge(themes)


def from_hex(value: str) -> Colour:
    text = value.lstrip("#")
    return Colour(
        int(text[0:2], 16) / 255.0,
        int(text[2:4], 16) / 255.0,
        int(text[4:6], 16) / 255.0,
    )


def show_dot_hues(themes: list[Theme]) -> None:
    print("SOURCE VALUES: the dot's inks against web-terminal-kiro's own, which is")
    print("the reference the two apps share. It themes @cplieger/web-terminal-ui's")
    print("--status-* family in its static-src/app.ts, so the library defaults are")
    print("NOT the comparison — aligning to those is what put a blue `working` here")
    print("against a violet one there. The HUE must match to a rounding step; the")
    print("VALUE matches too wherever the source's own value clears this app's floor")
    print("(the sizing report below says which ones did not, and why).")
    print()
    print(
        f"  {'state':<8} {'source token':<18} {'source value':<22} {'hue':<8} "
        f"{'marotte token':<20} dark                 light"
    )
    for state, token, src_token, src_expr in DOT_SOURCE_HUES:
        src = themes[0].resolve(src_expr)
        _, _, src_hue = colour_to_oklch(src)
        cells = []
        for th in themes:
            colour = th.colour(token)
            _, _, hue = colour_to_oklch(colour)
            same = "same value" if colour.hex() == src.hex() else ""
            cells.append(f"{fmt(hue)}deg ({fmt(abs(hue - src_hue))} off) {same:<11}")
        print(
            f"  {state:<8} {src_token:<18} {src_expr:<22} {fmt(src_hue):<8} {token:<20} "
            + " ".join(cells)
        )
    print()


def dot_held_worst(th: Theme, expr: str) -> tuple[str, float]:
    """The worst contrast an ink reads on any HELD tab-row fill.

    One ink across every fill, because that is what the stylesheets paint: no
    on-selected ink family exists. The held set is the two unselected fills
    (DOT_HELD), so a sweep sizes a candidate against the surfaces the floor is
    actually held on.
    """
    held = []
    for col, fill in DOT_FILLS + DOT_SELECTED_FILLS:
        if col not in DOT_HELD:
            continue
        bg = th.flat(fill)
        held.append((col, contrast(th.resolve(expr).over(bg), bg)))
    return min(held, key=lambda kv: kv[1])


def dot_admissible(th: Theme, hue: float) -> list[tuple[int, float, float, float]]:
    """Per lightness at one hue: the in-gamut chroma range that clears the floor.

    Reported as a BAND rather than a ranked winner, because there is no single
    scalar to rank by: the hue is fixed by the source, and L and C then trade
    against each other (a deeper ink clears the floor more easily, a more chromatic
    one keeps the source's colour identity), with the sRGB boundary cutting the
    corner off. The declared token is placed in this band instead.
    """
    label = f"{round(hue, 1):g}"
    lo, hi, step = DOT_SWEEP_CHROMA
    rows = []
    for pct in DOT_SWEEP_LIGHTNESS:
        chromas, worst_seen = [], 0.0
        for i in range(round((hi - lo) / step) + 1):
            chroma = round(lo + i * step, 3)
            expr = f"oklch({pct}% {chroma:g} {label}deg)"
            if th.resolve(expr).clipped:
                continue
            _col, worst = dot_held_worst(th, expr)
            if worst < DOT_FLOOR:
                continue
            chromas.append(chroma)
            worst_seen = max(worst_seen, worst)
        if chromas:
            rows.append((pct, min(chromas), max(chromas), worst_seen))
    return rows


def show_dot_sizing(themes: list[Theme]) -> None:
    print("SIZING, which is the only thing that varies per theme. The hue is the")
    print("source's in both; L and C are this app's, because web-terminal-kiro has")
    print("one theme and a near-black tab chip while these rows sit on")
    print("--c-bg-secondary under the tab family's tinted hover wash. For each")
    print("state this measures")
    print("the SOURCE value on this theme's fills, and when it misses, prints the")
    print("admissible band at the source's own hue — the lightnesses that clear the")
    print("floor and the in-gamut chroma range at each — with the declared token")
    print("placed in it. A band rather than a winner, because L and C trade against")
    print("each other and the sRGB boundary cuts the corner off, so there is no one")
    print("number to rank by.")
    print()
    print("The in-gamut constraint is not tidiness: outside sRGB a browser reduces")
    print("chroma while the favicon generator clamps per channel, so an out-of-gamut")
    print("ink would paint the tab dot and the tab ICON two different colours, which")
    print("is the one thing the attention badge exists to keep in step.")
    print()
    for th in themes:
        print(f"  {th.name}:")
        for state, token, _src_token, src_expr in DOT_SOURCE_HUES:
            declared = th.decls.get(token, "").strip().rstrip(";")
            src = th.resolve(src_expr)
            src_lightness, src_chroma, src_hue = colour_to_oklch(src)
            col, worst = dot_held_worst(th, src_expr)
            ok = worst >= DOT_FLOOR
            mark = "<--" if declared.lower() == src_expr.lower() else "   "
            print(
                f"    {state:<8} {mark} source {src.hex():<9} "
                f"L {src_lightness * 100:5.1f}% C {fmt(src_chroma):<6} "
                f"{col} {fmt(worst)}:1  {'PASS' if ok else 'MISSES the floor'}"
            )
            if ok:
                continue
            band = dot_admissible(th, src_hue)
            if not band:
                print(f"    {'':<8}     nothing in gamut at this hue clears the floor")
                continue
            lightnesses = [pct for pct, _lo, _hi, _w in band]
            print(
                f"    {'':<8}     admissible L {min(lightnesses)}..{max(lightnesses)}%"
                f" at hue {round(src_hue, 1):g}deg"
                f" (source L {src_lightness * 100:.1f}% misses)"
            )
            declared_colour = th.colour(token)
            dec_lightness, dec_chroma, _dec_hue = colour_to_oklch(declared_colour)
            dec_pct = round(dec_lightness * 100)
            for pct, chroma_lo, chroma_hi, _best in band:
                if abs(pct - dec_pct) > 1:
                    continue
                print(
                    f"    {'':<8}     L {pct}%  clears at C {fmt(chroma_lo)}"
                    f"..{fmt(chroma_hi)}"
                )
            dec_col, dec_worst = dot_held_worst(th, f"var({token})")
            print(
                f"    {'':<8} <-- {declared:<30} {declared_colour.hex():<9} "
                f"L {dec_lightness * 100:5.1f}% C {fmt(dec_chroma):<6} "
                f"{dec_col} {fmt(dec_worst)}:1"
            )
        print()


def show_favicon_badge(themes: list[Theme]) -> None:
    print("ATTENTION FAVICON BADGE: the same three cue inks, measured where they")
    print("are actually seen. The badge is composited onto static/favicon.svg,")
    print("whose own artwork is a flat saturated violet, so its backdrop is")
    print("NEITHER theme's surface — which is what decides that ONE icon serving")
    print("both themes is correct.")
    print()
    backdrop = from_hex(favicon_backdrop())
    print(f"  badge sits on the icon's flat fill: {backdrop.hex()}")
    print("  (read from static/favicon.svg; the brand violet, deliberately not")
    print("  a copy of any theme token)")
    print()
    print(
        f"  {'cue':<8} {'token':<12} "
        + " ".join(f"{th.name + ' ink':<18}" for th in themes)
        + " gamut"
    )
    for cue, token in FAVICON_CUES:
        cells, clipped = [], []
        for th in themes:
            c = th.colour(token)
            cells.append(f"{c.hex()} {fmt(contrast(c, backdrop))}:1")
            if c.clipped:
                clipped.append(th.name)
        # An out-of-gamut ink is a real defect here: a browser reduces chroma while the generator
        # clamps per channel, so the tab dot and icon would differ.
        verdict = "in sRGB" if not clipped else f"FAIL clipped in {', '.join(clipped)}"
        print(
            f"  {cue:<8} {token:<12} "
            + " ".join(f"{c:<18}" for c in cells)
            + f" {verdict}"
        )
    print()
    print("  The dark inks are pastels and the light ones are deep, so on a")
    print("  saturated violet only the dark family separates at all. The generator")
    print("  therefore reads the default theme's :root and the light overrides are")
    print("  out of scope by MEASUREMENT rather than by omission.")
    print()


def show_shadow(themes: list[Theme]) -> None:
    print("ELEVATION: each authored shadow layer composited over the surface it")
    print("falls on. A floating surface can sit over any rung, so the figure that")
    print("matters is the WORST rung. Below ~1.05:1 the layer is not a shadow, it")
    print("is a no-op: a black shadow cannot darken a base that is already black.")
    print()
    for th in themes:
        rungs = [
            r
            for r in ("--c-bg-primary", "--c-bg-secondary", "--c-bg-tertiary")
            if r in th.decls
        ]
        print(f"  {th.name}:")
        print(f"    {'site':<24} {'worst rung':<14} {'on':<18} verdict")
        for label, colour in SHADOW_SITES:
            worst, where = None, ""
            for rung in rungs:
                bg = th.flat(rung)
                r = contrast(th.resolve(colour).over(bg), bg)
                if worst is None or r < worst:
                    worst, where = r, rung
            assert worst is not None
            verdict = "ok" if worst >= 1.05 else "FLAT"
            print(f"    {label:<24} {fmt(worst) + ':1':<14} {where:<18} {verdict}")
        print()

    print("AMBIENT LAYER CANDIDATES: a `0 0 0 1px <colour>` ring composited over")
    print("each rung. Derived off the INK, so it lifts on dark and sinks on light")
    print("from one declaration — which is the property a black shadow lacks.")
    print()
    for th in themes:
        print(f"  {th.name}:")
        for tok in AMBIENT_CANDIDATES:
            if tok not in th.decls:
                print(f"    {tok:<26} not declared")
                continue
            c = th.colour(tok)
            cells = []
            for rung in ("--c-bg-primary", "--c-bg-secondary", "--c-bg-tertiary"):
                if rung not in th.decls:
                    continue
                bg = th.flat(rung)
                cells.append(
                    f"{rung.replace('--c-bg-', '')}={fmt(contrast(c.over(bg), bg))}:1"
                )
            print(f"    {tok:<26} {'  '.join(cells)}")
        print()


# The ANSI palette's ONE surface: tool-card.ts and messages-tools.ts write into
# `.tool-output pre`, which paints nothing, so it is `.tool-call`'s opaque
# --c-bg-secondary. The live shell panel paints server-resolved RGB and reads none of
# these tokens.
ANSI_SURFACES = ("--c-bg-secondary",)

# A bare `ESC[41m` brings a fill and no colour, so the container's ink lands on it.
ANSI_INKS = {"--c-text-secondary": "--c-bg-secondary"}

ANSI_FG_FLOOR = 4.5
ANSI_PAIR_FLOOR = 4.5

# The 16 ANSI codes, spelled out: a `.ansi-*-fg` pattern would admit default-colour
# fallbacks as a 17th code. ansi-palette.node.test.ts asserts the count the other way.
ANSI_CODES = (
    "black", "red", "green", "yellow", "blue", "magenta", "cyan", "white",
    "bright-black", "bright-red", "bright-green", "bright-yellow",
    "bright-blue", "bright-magenta", "bright-cyan", "bright-white",
)  # fmt: skip


def parse_ansi_sheet() -> tuple[dict[str, str], dict[str, str]]:
    """Read css/15-ansi.css and return its declared expressions.

    PARSED, not restated. This used to be two hardcoded dicts mirroring the
    stylesheet's 32 literals, and it had already drifted: `black` still read
    `#000` here long after `.ansi-black-bg` moved to a token, so the section
    reported a colour the app had stopped painting. A duplicated palette is the
    defect this whole section exists to measure, so the report may not keep its
    own copy. Only the class NAMES are enumerated (ANSI_CODES), which is a
    different kind of claim from a colour.
    """
    src = re.sub(r"/\*.*?\*/", " ", ANSI_SHEET.read_text(), flags=re.DOTALL)
    fg: dict[str, str] = {}
    bg: dict[str, str] = {}
    codes = "|".join(re.escape(c) for c in ANSI_CODES)
    for m in re.finditer(rf"\.ansi-({codes})-(fg|bg)\s*\{{([^}}]*)\}}", src):
        name, role, body = m.group(1), m.group(2), m.group(3)
        prop = "color" if role == "fg" else "background-color"
        v = re.search(rf"(?<![-\w]){prop}\s*:\s*([^;]+)", body)
        if v:
            (fg if role == "fg" else bg)[name] = v.group(1).strip()
    return fg, bg


def ansi_rows(
    themes: list[Theme],
) -> list[tuple[str, str, str, str, Colour, Colour, float, float]]:
    """Every ANSI measurement as (kind, theme, name, context, fg, bg, ratio, floor).

    A floor of 0 means "recorded, not gated" — see the fill-vs-surface trade.
    """
    fgs, bgs = parse_ansi_sheet()
    rows = []
    for th in themes:
        # A foreground is text on whichever container it landed in.
        for name, expr in fgs.items():
            for s in ANSI_SURFACES:
                surf = th.flat(s)
                rows.append(
                    (
                        "fg-surface",
                        th.name,
                        f"{name}-fg",
                        s,
                        th.resolve(expr),
                        surf,
                        contrast(th.resolve(expr), surf),
                        ANSI_FG_FLOOR,
                    )
                )
        # A foreground ON a fill: what a program setting both actually renders.
        for name, expr in fgs.items():
            for bname, bexpr in bgs.items():
                f, b = th.resolve(expr), th.resolve(bexpr)
                rows.append(
                    (
                        "fg-fill",
                        th.name,
                        f"{name}-fg",
                        f"{bname}-bg",
                        f,
                        b,
                        contrast(f, b),
                        ANSI_PAIR_FLOOR,
                    )
                )
        # A fill with no ANSI foreground: the container's own ink lands on it.
        for ink in ANSI_INKS:
            for bname, bexpr in bgs.items():
                f, b = th.colour(ink), th.resolve(bexpr)
                rows.append(
                    (
                        "ink-fill",
                        th.name,
                        ink,
                        f"{bname}-bg",
                        f,
                        b,
                        contrast(f, b),
                        ANSI_PAIR_FLOOR,
                    )
                )
        # Fill vs surface is RECORDED, NOT GATED: carrying the darkest ink at 4.5:1 pins a fill
        # past the surface's own luminance (1.00:1 to 1.41:1), so a 3:1 gate is unreachable for
        # any palette that keeps its hues.
        for bname, bexpr in bgs.items():
            for s in ANSI_SURFACES:
                b, surf = th.resolve(bexpr), th.flat(s)
                rows.append(
                    (
                        "fill-surface",
                        th.name,
                        f"{bname}-bg",
                        s,
                        b,
                        surf,
                        contrast(b, surf),
                        0.0,
                    )
                )
    return rows


def show_ansi(themes: list[Theme]) -> None:
    fgs, bgs = parse_ansi_sheet()
    print("ANSI PALETTE (css/15-ansi.css, read from the stylesheet itself).")
    print()
    print("The 32 --c-term-* values behind these classes are GENERATED — kitty's")
    print("default palette lifted by web-terminal-engine's own contrast rule. Run")
    print("`css-ansi-palette.py table` for the kitty-vs-shipped audit; this section")
    print("only measures the floors, and does so through the stylesheet, so it")
    print("cannot agree with the generator by construction.")
    print()
    print("Two ramps, because a colour that reads AS text and a colour that CARRIES")
    print("text cannot be the same colour. Three checks are GATED: an ink on the one")
    print("surface it renders on (4.5:1), an ink on every fill (4.5:1 — what")
    print("ESC[34;40m renders), and the container's own ink on every fill (4.5:1 —")
    print("the bare ESC[41m case). A fill against the surface it marks is RECORDED,")
    print("NOT GATED: see the trade at the end.")
    print()
    literals = [n for n, e in {**fgs, **bgs}.items() if "var(" not in e]
    if literals:
        print(
            f"  !! {len(literals)} entries still carry a literal: {', '.join(sorted(literals))}"
        )
        print()

    rows = ansi_rows(themes)
    for kind, title in (
        ("fg-surface", "INK vs THE SURFACE IT RENDERS ON (floor 4.5:1)"),
        ("fg-fill", "INK ON FILL — every pair a program can select (floor 4.5:1)"),
        ("ink-fill", "CONTAINER INK ON FILL — a fill with no ANSI ink (floor 4.5:1)"),
    ):
        group = [r for r in rows if r[0] == kind]
        print(f"  {title}")
        for th in themes:
            mine = [r for r in group if r[1] == th.name]
            fails = [r for r in mine if r[6] < r[7]]
            worst = min(mine, key=lambda r: r[6])
            print(
                f"    {th.name:<6} {len(mine):>3} checks, worst "
                f"{worst[2]} on {worst[3]} = {fmt(worst[6])}:1"
                + (f"   {len(fails)} FAIL" if fails else "   all clear")
            )
            for r in fails:
                print(
                    f"        {r[2]:<26} on {r[3]:<20} {fmt(r[6])}:1  FAIL (want {r[7]})"
                )
        print()

    print("  FILL vs THE SURFACE IT MARKS — recorded, not gated.")
    for th in themes:
        mine = [r for r in rows if r[0] == "fill-surface" and r[1] == th.name]
        lo, hi = min(mine, key=lambda r: r[6]), max(mine, key=lambda r: r[6])
        print(
            f"    {th.name:<6} {fmt(lo[6])}:1 ({lo[2]} on {lo[3]}) .. "
            f"{fmt(hi[6])}:1 ({hi[2]} on {hi[3]})"
        )
    print("    A region marker would want 3:1 and cannot have it: a fill that carries")
    print("    the darkest ink at 4.5:1 is pinned past the surface's own luminance. So")
    print("    a fill WITH text on it is fully legible and a TEXTLESS coloured region")
    print(
        "    is quiet. That is .ansi-black-bg's old trade, now general — and measured"
    )
    print("    against --c-bg-secondary, which is where this palette actually renders.")
    print()


def ansi_check(themes: list[Theme], tsv: bool) -> int:
    """`ansi-check` — every gated ANSI measurement as TSV, exit 1 on any miss.

    Exists so a test can assert these floors against THIS implementation instead
    of a second copy of the colour maths in TypeScript, and so 256 pairs cost one
    process rather than 256.
    """
    rows = ansi_rows(themes)
    failed = 0
    for kind, theme, name, ctx, fg, bg, ratio, floor in rows:
        gated = floor > 0
        miss = gated and ratio < floor
        failed += 1 if miss else 0
        if tsv:
            print(
                f"{kind}\t{theme}\t{name}\t{ctx}\t{fg.hex()}\t{bg.hex()}\t"
                f"{fmt(ratio)}\t{fmt(floor) if gated else '-'}\t"
                f"{'FAIL' if miss else ('ok' if gated else 'recorded')}"
            )
    if not tsv:
        print(f"{len(rows)} measurements, {failed} FAIL")
    return 1 if failed else 0


def main() -> int:
    argv = sys.argv[1:] or ["all"]
    cmd = argv[0]
    dark, light = parse_themes()
    themes = [dark, light]

    ramp = [
        "--c-bg-primary",
        "--c-turn-body",
        "--c-bg-secondary",
        "--c-bg-tertiary",
        "--c-bg-elevated",
        "--c-bg-hover",
    ]
    surfaces = [r for r in ramp if r in dark.decls]

    if cmd in ("ramp", "all"):
        print("SURFACE RAMP")
        print("  page -> card -> box -> band, monotonically away from the page in")
        print("  BOTH themes. --c-turn-body is the transcript's card rung, a half-step")
        print("  between the first two rungs and exempt from the >= 1.25:1 floor.")
        show_ramp(themes, ramp)

    if cmd in ("pairs", "all"):
        print("EDGE / SELECTION / INTERACTION SEPARATION")
        pairs: list[tuple[str, str, str, float | None]] = [
            ("border vs bg-primary", "--c-border", "--c-bg-primary", None),
            ("border vs bg-secondary", "--c-border", "--c-bg-secondary", None),
            ("border vs bg-tertiary", "--c-border", "--c-bg-tertiary", None),
            ("border vs bg-elevated", "--c-border", "--c-bg-elevated", None),
            ("border vs bg-hover (legacy)", "--c-border", "--c-bg-hover", None),
            (
                "accent-subtle vs bg-hover (legacy)",
                "--c-accent-subtle",
                "--c-bg-hover",
                None,
            ),
            (
                "selected-bg vs bg-secondary",
                "--c-selected-bg",
                "--c-bg-secondary",
                1.25,
            ),
            # No `selected-border vs selected-bg` row: the selected treatment has no edge channel
            # (70-selection.css); the resting-edge row below reports what replaced it.
            (
                "border wash on the selected fill",
                "color-mix(in oklch, var(--c-text-primary) 16%, var(--c-selected-bg))",
                "--c-selected-bg",
                None,
            ),
            ("selected-fg on selected-bg", "--c-selected-fg", "--c-selected-bg", 4.5),
            (
                "selected-bg-hover vs selected-bg",
                "--c-selected-bg-hover",
                "--c-selected-bg",
                None,
            ),
            (
                "selected-bg-press vs selected-bg-hover",
                "--c-selected-bg-press",
                "--c-selected-bg-hover",
                None,
            ),
            (
                "tab-active-bg vs bg-secondary (legacy)",
                "--c-tab-active-bg",
                "--c-bg-secondary",
                None,
            ),
            (
                "tab-active-border vs tab-active-bg (legacy)",
                "--c-tab-active-border",
                "--c-tab-active-bg",
                None,
            ),
        ]
        show_pairs(themes, pairs)

        print("WASHES vs EVERY SURFACE RUNG (a wash has no position on the ramp,")
        print(
            "so it must clear each rung — this is what the old opaque border could not do)"
        )
        wash_pairs: list[tuple[str, str, str, float | None]] = []
        for s in surfaces:
            wash_pairs.append(
                (f"border on {s.replace('--c-bg-', '')}", "--c-border", s, 1.3)
            )
        show_pairs(themes, wash_pairs)
        show_well(themes)

        print(
            "TINTED HAIRLINES: the color-mix(status, --c-border) sites, over their surface"
        )
        tinted: list[tuple[str, str, str, float | None]] = []
        for hue in ("--c-teal", "--c-danger", "--c-accent"):
            expr = f"color-mix(in srgb, var({hue}) 45%, var(--c-border))"
            tinted.append(
                (
                    f"{hue.replace('--c-', '')} 45% + border",
                    expr,
                    "--c-bg-secondary",
                    1.3,
                )
            )
        show_pairs(themes, tinted)

        print("HOVER / PRESS WASH, composited over each surface")
        for th in themes:
            print(f"  {th.name}:")
            for base in ("--c-bg-primary", "--c-bg-secondary", "--c-bg-tertiary"):
                if base not in th.decls:
                    continue
                bg = th.flat(base)
                # Both the achromatic and the TINTED ladder, over every rung: 01-tokens.css sizes the
                # tinted alphas against the step.
                for pair in (
                    ("--c-hover", "--c-press"),
                    ("--c-hover-select", "--c-press-select"),
                ):
                    row = [f"    over {base:<18}"]
                    for tok in pair:
                        if tok not in th.decls:
                            continue
                        w = th.colour(tok).over(bg)
                        row.append(f"{tok}={fmt(contrast(w, bg))}:1 ({w.hex()})")
                    if len(row) > 1:
                        print("  ".join(row))
            print()

    if cmd in ("text", "all"):
        print("TEXT ON SURFACE (WCAG 1.4.3: 4.5:1 body, 3:1 large; 1.4.11: 3:1 UI)")
        text_pairs: list[tuple[str, str, str, float | None]] = []
        for t in (
            "--c-text-primary",
            "--c-text-secondary",
            "--c-text-tertiary",
            "--c-text-control",
            "--c-text-aside",
        ):
            for s in surfaces:
                # --c-bg-elevated hosts primary and secondary only, so the hint ink there is a rule
                # violation, asserted over the stylesheets in css-tokens.node.test.ts. Neither gate sees
                # an `opacity`; the same file's "never dims a text ink with opacity" covers that.
                floor = (
                    None if t == "--c-text-tertiary" and s == "--c-bg-elevated" else 4.5
                )
                text_pairs.append(
                    (
                        f"{t.replace('--c-text-', '')} on {s.replace('--c-bg-', '')}",
                        t,
                        s,
                        floor,
                    )
                )
        # A control's label sits on its own HOVER wash more often than on the top rung.
        for t in ("--c-text-control", "--c-text-primary"):
            for s in ("--c-bg-primary", "--c-bg-secondary"):
                expr = f"over(var(--c-hover), var({s}))"
                text_pairs.append(
                    (
                        f"{t.replace('--c-text-', '')} on hover({s.replace('--c-bg-', '')})",
                        t,
                        expr,
                        4.5,
                    )
                )
        # And on the TINTED rung, where every tab-shaped control's label sits.
        for t in ("--c-text-control", "--c-text-primary"):
            text_pairs.append(
                (
                    f"{t.replace('--c-text-', '')} on hover-select(secondary)",
                    t,
                    "over(var(--c-hover-select), var(--c-bg-secondary))",
                    4.5,
                )
            )
        text_pairs += [
            ("accent on bg-primary", "--c-accent", "--c-bg-primary", 4.5),
            ("on-accent on accent", "--c-on-accent", "--c-accent", 4.5),
            ("link on bg-primary", "--c-link", "--c-bg-primary", 4.5),
            ("green on bg-primary", "--c-green", "--c-bg-primary", 4.5),
            ("red on bg-primary", "--c-red", "--c-bg-primary", 4.5),
            ("yellow on bg-primary", "--c-yellow", "--c-bg-primary", 4.5),
            ("danger on bg-primary", "--c-danger", "--c-bg-primary", 4.5),
            # The one status ink that LEAVES the tab strip: 29-turns.css paints `running` with it on
            # --c-bg-tertiary, which no tab row presents. 3.0 because it is an 8px graphic.
            ("dot-working on bg-tertiary", "--c-dot-working", "--c-bg-tertiary", 3.0),
        ]
        # Status hues are ink as often as fill, so each is measured off the page too. 3:1 because
        # most land on a glyph or badge; small body text needs its own 4.5:1 row above.
        for hue in (
            "--c-green",
            "--c-red",
            "--c-yellow",
            "--c-blue",
            "--c-danger",
            "--c-warning",
        ):
            for s in ("--c-bg-secondary", "--c-bg-tertiary", "--c-bg-elevated"):
                # --c-bg-elevated is two-level (only .pill:active, primary ink), as for the hint ink above.
                floor = None if s == "--c-bg-elevated" else 3.0
                text_pairs.append(
                    (
                        f"{hue.replace('--c-', '')} ink on {s.replace('--c-bg-', '')}",
                        hue,
                        s,
                        floor,
                    )
                )
        # The focus ring (always the accent) is a 1.4.11 graphic over every surface it can cross.
        for s in surfaces:
            text_pairs.append(
                (f"focus ring on {s.replace('--c-bg-', '')}", "--c-accent", s, 3.0)
            )
        show_pairs(themes, text_pairs)

    if cmd in ("ink", "all"):
        show_ink_ramp(themes)

    if cmd in ("selected", "all"):
        show_selected(themes)

    if cmd in ("dot", "all"):
        show_dot(themes)

    if cmd in ("shadow", "all"):
        show_shadow(themes)

    if cmd in ("ansi", "all"):
        show_ansi(themes)

    if cmd == "ansi-check":
        return ansi_check(themes, tsv="--tsv" in argv[1:])

    if cmd == "show":
        for tok in argv[1:]:
            print(f"{tok}:")
            for th in themes:
                try:
                    c = th.flat(tok, "--c-bg-primary")
                    print(
                        f"  {th.name:<6} {c!r:<22} Y={fmt(c.luminance())}"
                        + ("  OUT OF GAMUT" if c.clipped else "")
                    )
                except (KeyError, ValueError) as exc:
                    print(f"  {th.name:<6} {exc}")

    if cmd == "pair":
        return show_pair(themes, argv[1:])

    return 0


def show_pair(themes: list[Theme], args: list[str]) -> int:
    """`pair <fg> <bg> [floor]` — one arbitrary pair, both themes, as TSV.

    Every other subcommand answers a question this file already knows to ask.
    This one takes the question from the caller, which is what a new pair needs
    before it has a home here — and what a test needs, so a contrast floor can be
    asserted against the SAME implementation the comments were measured with
    instead of a second copy of the colour maths in another language.

    Output is `theme<TAB>fg_hex<TAB>bg_hex<TAB>ratio` per theme, so it parses
    without a JSON dependency. With a floor, the exit status is 1 when either
    theme misses it.
    """
    if len(args) < 2:
        usage = "usage: css-contrast.py pair <fg-expr> <bg-expr> [floor]"
        print(usage, file=sys.stderr)
        return 2
    fg_expr, bg_expr = args[0], args[1]
    floor = float(args[2]) if len(args) > 2 else None
    failed = False
    for th in themes:
        try:
            fg = th.flat(fg_expr, "--c-bg-primary")
            bg = th.flat(bg_expr, "--c-bg-primary")
        except (KeyError, ValueError) as exc:
            print(f"{th.name}\tERROR\t{exc}", file=sys.stderr)
            return 2
        ratio = contrast(fg, bg)
        print(f"{th.name}\t{fg.hex()}\t{bg.hex()}\t{fmt(ratio)}")
        if floor is not None and ratio < floor:
            failed = True
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
