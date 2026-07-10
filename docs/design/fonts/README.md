# Rail Alphabet clone fonts

`britrdn.ttf` (BritishRailDarkNormal) and `britrln.ttf` (BritishRailLightNormal) are
freeware Rail Alphabet clones (Fontographer, 2001-07-19) with no license metadata in the
files. Shipping them in the web UI was accepted by the project owner on 2026-07-09
(docs/design/2026-07-09-web-ui-wayfinding-brief.md §8). If a rights issue ever surfaces,
delete `internal/web/static/fonts/` and the `@font-face` block in style.css — the UI
falls back to the metrically-close Helvetica stack with no other change.

Subset woff2 files in internal/web/static/fonts/ are generated from these with:
  pyftsubset <src>.ttf --unicodes="U+0020-007E,U+00A3,U+2013,U+2014,U+2018,U+2019,U+201C,U+201D,U+2026" --flavor=woff2 --output-file=<dst>.woff2

## Dot Matrix (board preview)

The web board preview uses the same Dot Matrix Regular/Bold/Bold Tall TTFs the panel
renders with (embedded at `internal/render/fonts/`), subset to woff2 with:

  pyftsubset "internal/render/fonts/Dot Matrix <Weight>.ttf" \
    --unicodes="U+0020-007E,U+00A3,U+00B7" --flavor=woff2 \
    --output-file=internal/web/static/fonts/dotmatrix-<weight>.woff2

Same shipping posture as the panel itself: the fonts are already distributed inside
every release binary; the web subsets add no new rights exposure.
