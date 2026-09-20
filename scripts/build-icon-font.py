"""Regenerate the bundled status-bar icon: pip install fonttools==4.59.1.

Run from any directory with Python 3. Normal extension builds use the committed
WOFF and do not require Python or fontTools.
"""

from pathlib import Path
from xml.etree import ElementTree

from fontTools.fontBuilder import FontBuilder
from fontTools.pens.cu2quPen import Cu2QuPen
from fontTools.pens.transformPen import TransformPen
from fontTools.pens.ttGlyphPen import TTGlyphPen
from fontTools.svgLib.path import parse_path


media = Path(__file__).resolve().parents[1] / "vscode" / "media"
glyph_pen = TTGlyphPen(None)
pen = TransformPen(Cu2QuPen(glyph_pen, max_err=1), (4, 0, 0, -4, 0, 896))
for element in ElementTree.parse(media / "environments.svg").iter("{http://www.w3.org/2000/svg}path"):
    parse_path(element.attrib["d"], pen)

font = FontBuilder(1024, isTTF=True)
font.setupGlyphOrder([".notdef", "logo"])
font.setupCharacterMap({0xE001: "logo"})
font.setupGlyf({".notdef": TTGlyphPen(None).glyph(), "logo": glyph_pen.glyph()})
font.setupHorizontalMetrics({".notdef": (1024, 0), "logo": (1024, 84)})
font.setupHorizontalHeader(ascent=896, descent=-128)
font.setupNameTable({"familyName": "Lua DevTools Icons", "styleName": "Regular",
                    "uniqueFontIdentifier": "LuaDevToolsIcons-Regular-1.0",
                    "fullName": "Lua DevTools Icons", "psName": "LuaDevToolsIcons-Regular",
                    "version": "Version 1.0"})
font.setupOS2(sTypoAscender=896, sTypoDescender=-128, usWinAscent=896, usWinDescent=128)
font.setupPost()
# Fixed timestamps keep regeneration deterministic.
font.font["head"].created = font.font["head"].modified = 2082844800
font.font.recalcTimestamp = False
font.font.flavor = "woff"
font.save(media / "lua-devtools.woff")
