#!/usr/bin/env python3
"""generate-font-glyphs.py - mede o .woff2 do agent para o teste de ligaduras (achado R7).

POR QUE EXISTE: o teste precisa saber se cada nome de glifo do catalogo realmente
DESENHA. Existem DUAS medicoes possiveis, e elas respondem perguntas diferentes:

  1. getGlyphOrder()  -> "existe um GLIFO chamado X na fonte?"
  2. shaper (HarfBuzz) -> "o TEXTO X vira UM glifo?"  <-- esta e a pergunta certa

Este arquivo emite AS DUAS de proposito. A diferenca e uma armadilha real:
nesta fonte o mecanismo e LIGADURA (o texto e substituido por outro glifo), entao
'location_on', 'payment' e 'phone' NAO tem glifo com esse nome, mas o shaper os
resolve normalmente. Usar getGlyphOrder() como criterio "acha" 3 defeitos que nao
existem. Quem for mexer aqui: use a medicao do shaper para decidir.

Uso:
    python -m pip install fonttools brotli uharfbuzz
    python generate-font-glyphs.py [woff2] [saida.json]

Saida: fixtures/font-glyphs.json
    {
      "source": "...",
      "glyph_count": N,
      "glyphs": [...],          # getGlyphOrder() - medicao ingenua
      "shaped": { "<texto>": ["glifo", ...] }   # shaper - medicao correta
    }
O arquivo E VERSIONADO: o teste node roda sem depender de Python.
"""
import io
import json
import os
import re
import sys

from fontTools.ttLib import TTFont

try:
    import uharfbuzz as hb
except ImportError:  # pragma: no cover
    hb = None

HERE = os.path.dirname(os.path.abspath(__file__))
DEFAULT_WOFF2 = os.path.normpath(
    os.path.join(HERE, "..", "..", "..", "frontend", "assets", "fonts",
                 "material-symbols-rounded.woff2")
)
DEFAULT_OUT = os.path.join(HERE, "fixtures", "font-glyphs.json")

# Nomes do catalogo vendorado (ICON_NAMES) em snake_case - o que o app passa ao shaper.
CATALOG_SNAKE = [
    "account_circle", "add", "arrow_back", "arrow_forward", "attach_file",
    "calendar_today", "call", "camera", "check", "close", "delete", "download",
    "edit", "event", "error", "fast_forward", "favorite", "favorite_off",
    "folder", "help", "home", "info", "location_on", "lock", "lock_open",
    "mail", "menu", "more_vert", "more_horiz", "notifications_off",
    "notifications", "pause", "payment", "person", "phone", "photo", "play",
    "print", "refresh", "rewind", "search", "send", "settings", "share",
    "shopping_cart", "skip_next", "skip_previous", "star", "star_half",
    "star_off", "stop", "upload", "visibility", "visibility_off", "volume_down",
    "volume_mute", "volume_off", "volume_up", "warning",
]


def main():
    woff2 = sys.argv[1] if len(sys.argv) > 1 else DEFAULT_WOFF2
    out = sys.argv[2] if len(sys.argv) > 2 else DEFAULT_OUT

    font = TTFont(woff2)
    glyph_order = font.getGlyphOrder()

    shaped = {}
    if hb is not None:
        # HarfBuzz nao le woff2: converte para ttf em memoria.
        font.flavor = None
        buf = io.BytesIO()
        font.save(buf)
        face = hb.Face(buf.getvalue())
        hbf = hb.Font(face)

        def shape(text):
            b = hb.Buffer()
            b.add_str(text)
            b.guess_segment_properties()
            hb.shape(hbf, b, {"liga": True, "rlig": True, "rclt": True, "clig": True})
            return [
                glyph_order[g.codepoint] if g.codepoint < len(glyph_order) else f"gid{g.codepoint}"
                for g in b.glyph_infos
            ]

        # Alem dos nomes do catalogo, medimos os ALVOS dos aliases e os nomes
        # CRUS que falham: o teste precisa dos dois lados para provar que o alias
        # e necessario E que ele resolve.
        alvos = [
            "favorite", "star", "play_arrow", "fast_rewind",
            "favorite_border", "star_border", "location_on", "payment", "phone",
            "favorite_off", "star_off", "play", "rewind",
        ]
        for name in sorted(set(CATALOG_SNAKE) | set(alvos)):
            shaped[name] = shape(name)

    os.makedirs(os.path.dirname(out), exist_ok=True)
    with open(out, "w", encoding="utf-8") as fh:
        json.dump(
            {
                "source": os.path.basename(woff2),
                "glyph_count": len(glyph_order),
                "glyphs": sorted(glyph_order),
                "shaped": shaped,
            },
            fh,
            ensure_ascii=False,
            indent=0,
        )
    print("glifos:", len(glyph_order), "| shapeados:", len(shaped), "->", out)


if __name__ == "__main__":
    main()
