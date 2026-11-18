#!/usr/bin/env python3
"""Draw the GPanel mark and pack it for macOS.

The mark is Google's G reduced to one monoline circle: four colour arcs of equal
rhythm, the opening at the top right, and the crossbar answering it at the right
edge. Nothing is drawn twice — the app icon scales the same 22pt grid the menu
bar template uses, so both renderings stay identical in shape.

  app icon   1024x1024 white card, four-colour arcs -> AppIcon.icns
  tray icon  22x22 (+44x44 retina) black-on-alpha template image
  web mark   web/mark.svg, the bare arcs the popover and the dashboard show

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
WEB_MARK = ROOT / "web" / "mark.svg"

# Google brand colours, in the order the eye reads them around the ring.
RED = "#EA4335"
YELLOW = "#FBBC05"
GREEN = "#34A853"
BLUE = "#4285F4"

# The mark lives on a 22pt grid; the app icon is this same grid scaled up.
GRID = 22.0
STROKE = 2.95
RADIUS = 6.35
# OPENING is the sweep of the G's mouth at the top right, measured up from the
# crossbar (0 degrees = right edge, 90 = top). Everything else is closed ring.
OPENING = 40.0
# SEGMENT_GAP is the nick between two colour arcs, in degrees. It survives at
# icon sizes and reads as four arcs; at 22pt it closes up into a solid ring.
SEGMENT_GAP = 8.0
# BAR_INSET is where the crossbar stops, in grid units to the right of centre.
BAR_INSET = 1.1

# Clockwise-from-the-top arcs, matching Google's G: red over the top, yellow on
# the left, green along the bottom, blue up the right side into the crossbar.
ARCS = [
    ("red", RED, OPENING + SEGMENT_GAP / 2, 138.0 - SEGMENT_GAP / 2),
    ("yellow", YELLOW, 138.0 + SEGMENT_GAP / 2, 222.0 - SEGMENT_GAP / 2),
    ("green", GREEN, 222.0 + SEGMENT_GAP / 2, 318.0 - SEGMENT_GAP / 2),
    # The blue arc runs into the crossbar, so its far end gets no nick.
    ("blue", BLUE, 318.0 + SEGMENT_GAP / 2, 360.0),
]

# App icon card, as fractions of the canvas.
CARD_RADIUS = 0.226
MARK_ZOOM = 0.82

ICONSET_SIZES = [16, 32, 32, 64, 128, 256, 256, 512, 512, 1024]


def point(cx: float, cy: float, radius: float, degrees: float) -> tuple[float, float]:
    rad = math.radians(degrees)
    return cx + radius * math.cos(rad), cy - radius * math.sin(rad)


def arc_path(cx: float, cy: float, radius: float, start: float, end: float) -> str:
    """Arc from start to end degrees, counter-clockwise, y-up convention."""
    x0, y0 = point(cx, cy, radius, start)
    x1, y1 = point(cx, cy, radius, end)
    large = 1 if (end - start) % 360 > 180 else 0
    return f"M{x0:.3f} {y0:.3f}A{radius:.3f} {radius:.3f} 0 {large} 0 {x1:.3f} {y1:.3f}"


def mark(size: float, scale: float = 1.0) -> tuple[str, list[tuple[str, str]]]:
    """Return (stroke_width, [(path, colour_key)]) for a size-px canvas."""
    s = size / GRID * scale
    cx = cy = size / 2
    radius, stroke = RADIUS * s, STROKE * s
    parts = []
    for key, _colour, start, end in ARCS:
        d = arc_path(cx, cy, radius, start, end)
        # The blue arc ends level with the centre, where the crossbar continues
        # inwards. One path, so the join is a corner and not two stacked caps.
        if end >= 360:
            d += f"H{cx + BAR_INSET * s:.3f}"
        parts.append((d, key))
    return stroke, parts


def app_svg(size: int = 1024) -> str:
    """The app icon: a white card carrying the four colour arcs."""
    s = float(size)
    stroke, parts = mark(s, MARK_ZOOM)
    colours = {key: colour for key, colour, _a0, _a1 in ARCS}
    paths = "\n".join(f'  <path d="{d}" stroke="{colours[key]}"/>' for d, key in parts)
    return f"""<svg xmlns="http://www.w3.org/2000/svg" width="{size}" height="{size}" viewBox="0 0 {size} {size}">
  <rect width="{s:.2f}" height="{s:.2f}" rx="{s * CARD_RADIUS:.2f}" fill="#FFFFFF"/>
  <rect x="1" y="1" width="{s - 2:.2f}" height="{s - 2:.2f}" rx="{s * CARD_RADIUS - 1:.2f}"
        fill="none" stroke="#000000" stroke-opacity="0.07" stroke-width="2"/>
  <g fill="none" stroke-width="{stroke:.3f}" stroke-linecap="round" stroke-linejoin="round">
{paths}
  </g>
</svg>
"""


def mark_svg(size: int = 32) -> str:
    """The bare mark: no card, for the header of the web UIs."""
    stroke, parts = mark(float(size))
    colours = {key: colour for key, colour, _a0, _a1 in ARCS}
    paths = "\n".join(f'  <path d="{d}" stroke="{colours[key]}"/>' for d, key in parts)
    return f"""<svg xmlns="http://www.w3.org/2000/svg" width="{size}" height="{size}" viewBox="0 0 {size} {size}">
  <g fill="none" stroke-width="{stroke:.3f}" stroke-linecap="round" stroke-linejoin="round">
{paths}
  </g>
</svg>
"""


def tray_svg(px: int) -> str:
    """The menu-bar template: black arcs, alpha only, macOS recolours it."""
    stroke, parts = mark(float(px))
    paths = "\n".join(f'  <path d="{d}"/>' for d, _key in parts)
    return f"""<svg xmlns="http://www.w3.org/2000/svg" width="{px}" height="{px}" viewBox="0 0 {px} {px}">
  <g fill="none" stroke="#000000" stroke-width="{stroke:.3f}" stroke-linecap="round" stroke-linejoin="round">
{paths}
  </g>
</svg>
"""


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


def build_app_icon() -> Path:
    BUILD.mkdir(parents=True, exist_ok=True)
    svg_path = BUILD / "AppIcon.svg"
    svg_path.write_text(app_svg(), encoding="utf-8")

    rasterize(svg_path, BUILD / "AppIcon.png", 1024, 1024)

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


def build_tray_icons() -> None:
    ASSETS.mkdir(parents=True, exist_ok=True)
    for px, name in ((22, "tray-template.png"), (44, "tray-template@2x.png")):
        svg_path = BUILD / f"tray-{px}.svg"
        svg_path.write_text(tray_svg(px), encoding="utf-8")
        rasterize(svg_path, ASSETS / name, px, px)


def build_web_mark() -> None:
    WEB_MARK.write_text(mark_svg(32), encoding="utf-8")


def main() -> None:
    icns = build_app_icon()
    build_tray_icons()
    build_web_mark()
    print(f"icons: {icns.relative_to(ROOT)}")
    for name in ("tray-template.png", "tray-template@2x.png"):
        print(f"icons: {(ASSETS / name).relative_to(ROOT)}")
    print(f"icons: {WEB_MARK.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
