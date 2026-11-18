#!/usr/bin/env python3
"""Draw the Gauge marks and pack them for macOS.

Two marks share one renderer:

  gauge    the default. A lowercase g for Gauge, drawn like the classic G:
           the bowl is a ring open at the top right, and an orange dot in the
           opening is the reading. Black or white plus that one orange.
  classic  the original four-colour G, turned into a meter: the colour arcs
           are butt-cut segments of a dial, the open mouth is the spent part,
           and the crossbar runs to a pivot at the centre, so it is a needle.

Each mark is a list of primitives on a 22pt grid. Every output scales that same
grid, so the menu bar template, the web mark and the app icon cannot drift.
Coordinates are chosen so straight edges and outer stroke edges land on whole
pixels at 22px and 44px.

  app icon   1024 canvas, 824 rounded card on Apple's grid -> AppIcon.icns (gauge,
             white g on a black card)
             plus a PNG of each mark's card for previews
  tray icon  22x22 (+44x44 retina) black-on-alpha template per mark
  web mark   web/mark.svg (gauge, on its own small black card) and
             web/mark-classic.svg (bare arcs)

SVG is generated here so the geometry lives in one place; nothing binary is
hand-edited. Run `make icons` after changing the numbers below.
"""

from __future__ import annotations

import math
import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
BUILD = ROOT / "build" / "icon"
ASSETS = ROOT / "cmd" / "gauge" / "assets"
WEB = ROOT / "web"

GRID = 22.0

# ---------------------------------------------------------------- primitives
#
# arc:  (kind, colour_key, cx, cy, radius, start_deg, end_deg, width, cap)
#       degrees are counter-clockwise from the right edge, y up; the arc is
#       drawn counter-clockwise from start to end.
# dot:  (kind, colour_key, cx, cy, radius)
# path: (kind, colour_key, d, width, cap), d in grid units, scaled on output


# --- gauge -----------------------------------------------------------------
# A lowercase g for Gauge, read as a meter, the way the classic G is. The bowl
# is a ring open at the top right; the orange dot in the opening is the
# reading. The tail hooks back left and up, which keeps it from reading as a 9.
#
# Stroke 3 on half-pixel centrelines, so every outer edge is a whole pixel:
# bowl 5..17 x 2..14, stem 14..17, tail bottom 20. The glyph spans 2..20.
G_W = 3.0
G_CX, G_CY, G_R = 11.0, 8.0, 4.5
# The bowl runs clockwise from OPEN degrees (12 o'clock = 0) round to the top;
# the gap between them is the spent part.
G_OPEN = 70.0
G_STEM_X = G_CX + G_R
G_STEM_END = 14.5
G_TAIL_R = 4.0
G_TAIL_END = 235.0  # clockwise from 12 o'clock, round the tail's own centre
G_DOT_AT = 35.0
G_DOT_R = 1.5


def _clock(cx: float, cy: float, r: float, deg: float) -> tuple[float, float]:
    """Point at deg clockwise from 12 o'clock, y down."""
    t = math.radians(deg)
    return cx + r * math.sin(t), cy - r * math.cos(t)


def gauge_parts() -> list[tuple]:
    x0, y0 = _clock(G_CX, G_CY, G_R, G_OPEN)
    bowl = f"M{x0:.3f} {y0:.3f}A{G_R} {G_R} 0 1 1 {G_CX} {G_CY - G_R}"
    tx, ty = _clock(G_STEM_X - G_TAIL_R, G_STEM_END, G_TAIL_R, G_TAIL_END)
    tail = f"M{G_STEM_X} {G_CY}V{G_STEM_END}A{G_TAIL_R} {G_TAIL_R} 0 0 1 {tx:.3f} {ty:.3f}"
    dx, dy = _clock(G_CX, G_CY, G_R, G_DOT_AT)
    return [
        ("path", "ink", bowl, G_W, "butt"),
        ("path", "ink", tail, G_W, "round"),
        ("dot", "reading", dx, dy, G_DOT_R),
    ]


# --- classic ---------------------------------------------------------------
# The original G, read as a meter: the four colour arcs are the segments of a
# dial, the open mouth at the top right is the spent part, and the crossbar now
# runs to a pivot at the centre, so the G's own bar is the needle.
#
# Centre on a half pixel so the 3pt crossbar spans 10..13 and the ring (radius
# 6, width 3) spans 4..19 on both axes.
C_CX, C_CY = 11.5, 11.5
C_R = 6.0
C_W = 3.0
# OPENING is the G's mouth at the top right, measured up from the crossbar.
C_OPENING = 42.0
# Segment ends are butt-cut and 14 degrees apart (about 1.5pt at the ring), so
# the nicks between colours stay open in the 22pt template.
C_SEGMENT_GAP = 14.0
C_HUB = 1.5  # the bar's own half-width: the bar simply ends in a round pivot

# Clockwise-from-the-top arcs, matching Google's G: red over the top, yellow on
# the left, green along the bottom, blue up the right side into the crossbar.
C_ARCS = [
    ("red", C_OPENING, 138.0 - C_SEGMENT_GAP / 2),
    ("yellow", 138.0 + C_SEGMENT_GAP / 2, 222.0 - C_SEGMENT_GAP / 2),
    ("green", 222.0 + C_SEGMENT_GAP / 2, 318.0 - C_SEGMENT_GAP / 2),
]
C_BLUE_START = 318.0 + C_SEGMENT_GAP / 2


def classic_parts() -> list[tuple]:
    parts: list[tuple] = []
    for key, a0, a1 in C_ARCS:
        parts.append(("arc", key, C_CX, C_CY, C_R, a0, a1, C_W, "butt"))
    # Blue runs up the right side and turns into the crossbar, which runs on
    # to the pivot.
    parts.append(("arcbar", "blue", C_CX, C_CY, C_R, C_BLUE_START, C_W))
    parts.append(("dot", "blue", C_CX, C_CY, C_HUB))
    return parts


MARKS = {
    "gauge": {
        "parts": gauge_parts,
        # Black and white only, plus one signal orange for the reading.
        "colours": {"ink": "#FFFFFF", "reading": "#FF5A1F"},
        "opacity": {},
        # The web mark carries its own black card, so the white g reads on
        # light and dark pages alike, whatever theme the page forces.
        "web_colours": None,
        "web_card": "#000000",
        "card": "black",
    },
    "classic": {
        "parts": classic_parts,
        "colours": {
            "red": "#EA4335",
            "yellow": "#FBBC05",
            "green": "#34A853",
            "blue": "#4285F4",
        },
        "opacity": {},
        "web_colours": None,
        "card": "light",
    },
}

# ---------------------------------------------------------------- rendering


def _pt(cx: float, cy: float, r: float, deg: float, s: float) -> tuple[float, float]:
    rad = math.radians(deg)
    return (cx + r * math.cos(rad)) * s, (cy - r * math.sin(rad)) * s


def _arc_d(cx, cy, r, a0, a1, s) -> str:
    x0, y0 = _pt(cx, cy, r, a0, s)
    x1, y1 = _pt(cx, cy, r, a1, s)
    large = 1 if (a1 - a0) % 360 > 180 else 0
    return f"M{x0:.3f} {y0:.3f}A{r * s:.3f} {r * s:.3f} 0 {large} 0 {x1:.3f} {y1:.3f}"


def _elements(name: str, s: float, ox: float, oy: float, colour_of, opacity_of) -> str:
    """SVG elements for a mark scaled by s (px per grid unit), offset by ox, oy."""
    out = []
    for p in MARKS[name]["parts"]():
        kind, key = p[0], p[1]
        colour, op = colour_of(key), opacity_of(key)
        extra = f' stroke-opacity="{op}"' if op < 1 else ""
        if kind == "arc":
            _k, _c, cx, cy, r, a0, a1, w, cap = p
            d = _arc_d(cx, cy, r, a0, a1, s)
            out.append(f'<path d="{d}" fill="none" stroke="{colour}" stroke-width="{w * s:.3f}" stroke-linecap="{cap}"{extra}/>')
        elif kind == "arcbar":
            _k, _c, cx, cy, r, a0, w = p
            d = _arc_d(cx, cy, r, a0, 360.0, s) + f"H{cx * s:.3f}"
            out.append(f'<path d="{d}" fill="none" stroke="{colour}" stroke-width="{w * s:.3f}" stroke-linecap="butt" stroke-linejoin="round"{extra}/>')
        elif kind == "path":
            _k, _c, d, w, cap = p
            out.append(
                f'<path transform="scale({s:.5f})" d="{d}" fill="none" stroke="{colour}" stroke-width="{w}" '
                f'stroke-linecap="{cap}" stroke-linejoin="round"{extra}/>'
            )
        elif kind == "dot":
            _k, _c, cx, cy, r = p
            fill_op = f' fill-opacity="{op}"' if op < 1 else ""
            out.append(f'<circle cx="{cx * s:.3f}" cy="{cy * s:.3f}" r="{r * s:.3f}" fill="{colour}"{fill_op}/>')
    body = "\n    ".join(out)
    if ox or oy:
        return f'<g transform="translate({ox:.3f} {oy:.3f})">\n    {body}\n  </g>'
    return f"<g>\n    {body}\n  </g>"


# Apple's macOS icon grid: on a 1024 canvas the card is 824 wide, inset 100,
# with a ~185 corner radius and a soft drop shadow below it.
CARD_INSET = 100.0
CARD_SIZE = 824.0
CARD_RADIUS = 185.4
# Mark size on the card: the 22pt grid scaled so the dial is ~62% of the card.
MARK_GRID_PX = 600.0

ICONSET_SIZES = [16, 32, 32, 64, 128, 256, 256, 512, 512, 1024]


def app_svg(name: str = "gauge", size: int = 1024) -> str:
    """The app icon: a rounded card on Apple's grid carrying the mark."""
    m = MARKS[name]
    k = size / 1024.0
    inset, card, radius = CARD_INSET * k, CARD_SIZE * k, CARD_RADIUS * k
    s = MARK_GRID_PX * k / GRID
    off = (size - MARK_GRID_PX * k) / 2
    if m["card"] == "black":
        top, bottom, rim, rim_op = "#000000", "#000000", "#FFFFFF", 0.12
    else:
        top, bottom, rim, rim_op = "#FFFFFF", "#F1F2F4", "#000000", 0.08
    marks = _elements(name, s, off, off, lambda c: m["colours"][c], lambda c: m["opacity"].get(c, 1.0))
    return f"""<svg xmlns="http://www.w3.org/2000/svg" width="{size}" height="{size}" viewBox="0 0 {size} {size}">
  <defs>
    <linearGradient id="card" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="{top}"/>
      <stop offset="1" stop-color="{bottom}"/>
    </linearGradient>
    <filter id="shadow" x="-20%" y="-20%" width="140%" height="140%">
      <feGaussianBlur in="SourceAlpha" stdDeviation="{12 * k:.2f}"/>
      <feOffset dy="{10 * k:.2f}"/>
      <feComponentTransfer><feFuncA type="linear" slope="0.30"/></feComponentTransfer>
      <feMerge><feMergeNode/><feMergeNode in="SourceGraphic"/></feMerge>
    </filter>
  </defs>
  <rect x="{inset:.2f}" y="{inset:.2f}" width="{card:.2f}" height="{card:.2f}" rx="{radius:.2f}" fill="url(#card)" filter="url(#shadow)"/>
  <rect x="{inset + 1:.2f}" y="{inset + 1:.2f}" width="{card - 2:.2f}" height="{card - 2:.2f}" rx="{radius - 1:.2f}"
        fill="none" stroke="{rim}" stroke-opacity="{rim_op}" stroke-width="2"/>
  {marks}
</svg>
"""


def mark_svg(name: str = "gauge", size: int = 32) -> str:
    """The bare mark: no card, for the header of the web UIs and the favicon."""
    m = MARKS[name]
    colours = m["web_colours"] or m["colours"]
    card = m.get("web_card")
    if not card:
        marks = _elements(name, size / GRID, 0, 0, lambda c: colours[c], lambda c: 1.0)
        return f"""<svg xmlns="http://www.w3.org/2000/svg" width="{size}" height="{size}" viewBox="0 0 {size} {size}">
  {marks}
</svg>
"""
    # A small app-icon card: the mark at 72% of the card, the card's corner
    # in the same proportion as the app icon's.
    inner = size * 0.72
    off = (size - inner) / 2
    marks = _elements(name, inner / GRID, off, off, lambda c: colours[c], lambda c: 1.0)
    return f"""<svg xmlns="http://www.w3.org/2000/svg" width="{size}" height="{size}" viewBox="0 0 {size} {size}">
  <rect width="{size}" height="{size}" rx="{size * CARD_RADIUS / CARD_SIZE:.2f}" fill="{card}"/>
  {marks}
</svg>
"""


def tray_svg(px: int, name: str = "gauge") -> str:
    """The menu-bar template: black on alpha, macOS recolours it."""
    marks = _elements(name, px / GRID, 0, 0, lambda c: "#000000", lambda c: 1.0)
    return f"""<svg xmlns="http://www.w3.org/2000/svg" width="{px}" height="{px}" viewBox="0 0 {px} {px}">
  {marks}
</svg>
"""


def tray_names(name: str) -> tuple[str, str]:
    stem = "tray-template" if name == "gauge" else f"tray-{name}-template"
    return f"{stem}.png", f"{stem}@2x.png"


def web_mark_path(name: str) -> Path:
    return WEB / ("mark.svg" if name == "gauge" else f"mark-{name}.svg")


def have(cmd: str) -> bool:
    return shutil.which(cmd) is not None


def rasterize(svg_path: Path, out_path: Path, width: int, height: int) -> None:
    """Render SVG to PNG with whichever converter is installed."""
    if have("rsvg-convert"):
        cmd = ["rsvg-convert", "-w", str(width), "-h", str(height), "-o", str(out_path), str(svg_path)]
    elif have("magick"):
        cmd = ["magick", "-background", "none", str(svg_path), "-resize", f"{width}x{height}!", str(out_path)]
    elif have("cairosvg"):
        cmd = ["cairosvg", str(svg_path), "-o", str(out_path), "-W", str(width), "-H", str(height)]
    else:
        sys.exit(
            "make_icons: no SVG rasteriser found. Install one of: librsvg (rsvg-convert), "
            "imagemagick (magick), cairosvg."
        )
    subprocess.run(cmd, check=True)


def build_app_previews() -> None:
    """A 1024 PNG of each mark's card, for screenshots and the iconset."""
    BUILD.mkdir(parents=True, exist_ok=True)
    for name in MARKS:
        stem = "AppIcon" if name == "gauge" else f"AppIcon-{name}"
        svg_path = BUILD / f"{stem}.svg"
        svg_path.write_text(app_svg(name), encoding="utf-8")
        rasterize(svg_path, BUILD / f"{stem}.png", 1024, 1024)


def build_app_icon() -> Path:
    build_app_previews()
    svg_path = BUILD / "AppIcon.svg"
    iconset = BUILD / "AppIcon.iconset"
    if iconset.exists():
        shutil.rmtree(iconset)
    iconset.mkdir(parents=True)
    for size, target in zip(ICONSET_SIZES, ICONSET_SIZES[1:]):
        rasterize(svg_path, iconset / f"icon_{size}x{size}.png", size, size)
        rasterize(svg_path, iconset / f"icon_{size}x{size}@2x.png", target, target)

    if not have("iconutil"):
        sys.exit("make_icons: iconutil not found (it ships with macOS).")
    subprocess.run(
        ["iconutil", "-c", "icns", str(iconset), "-o", str(BUILD / "AppIcon.icns")],
        check=True,
    )
    return BUILD / "AppIcon.icns"


def build_tray_icons() -> list[Path]:
    ASSETS.mkdir(parents=True, exist_ok=True)
    out = []
    for name in MARKS:
        for px, file in zip((22, 44), tray_names(name)):
            svg_path = BUILD / f"tray-{name}-{px}.svg"
            svg_path.write_text(tray_svg(px, name), encoding="utf-8")
            rasterize(svg_path, ASSETS / file, px, px)
            out.append(ASSETS / file)
    return out


def build_web_marks() -> list[Path]:
    out = []
    for name in MARKS:
        path = web_mark_path(name)
        path.write_text(mark_svg(name, 32), encoding="utf-8")
        out.append(path)
    return out


def main() -> None:
    icns = build_app_icon()
    print(f"icons: {icns.relative_to(ROOT)}")
    for path in build_tray_icons() + build_web_marks():
        print(f"icons: {path.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
