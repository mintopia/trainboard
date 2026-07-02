# 2. Greyscale rendering via a native Go SSD1322 driver

Date: 2026-07-02
Status: Accepted

## Context

The old board rendered in 1-bit (hard pixels) via Python + luma.oled, repacking and
pushing the whole framebuffer each frame. The rewrite wants animation that is "smooth
and looks good" while running well on a Pi Zero W 2 (Cortex-A53, 512 MB).

The SSD1322 is natively 4-bit (16 grey levels); 1-bit content is still written as 4-bit
nibbles. Options considered: preserve crisp 1-bit glyphs (authentic dot-matrix look);
full 4-bit greyscale (anti-aliased, smooth); or a hybrid.

## Decision

Render in **4-bit greyscale** using **anti-aliased glyph edges**, driven by a **native Go
SSD1322 SPI driver** (via periph.io for SPI/GPIO transport only) that performs
**dirty-region partial updates** (column-addressed, nibble-packed). Keep the Dot Matrix
font. Brightness/powersaving use the panel's `contrast` command.

**Scrolling is integer-pixel** (faithful to the ~25fps Python reference); greyscale is
for glyph edges, not motion. Blended sub-pixel scroll is adopted only if a golden-frame
test proves it does not smear dot-matrix text.

**Frame rate is benchmark-gated, not assumed.** Target **25–30fps (reference parity)**;
60fps is aspirational and only pursued if an early-M1 hardware benchmark (worst-case
256×12 / 256×24 / full-frame flush timings on the Zero 2 W) supports it. SSD1322 partial
writes are not free — scrolling can dirty a wide strip per frame.

## Consequences

- **Positive:** Smooth-looking motion and clean edges without betraying the dot-matrix
  glyph shapes (integer-pixel scroll keeps text crisp in motion).
- **Positive / performance:** Glyph rasterization is cached per text change (not per
  frame); the animation loop blits cached buffers. Partial SPI writes reduce per-frame
  traffic, but the actual ceiling is measured on hardware before the render architecture
  is committed.
- **Negative:** Softer glyph edges than strict 1-bit; accepted for smoothness.
- **Negative:** We own the SSD1322 driver and the greyscale rasterization pipeline
  (`golang.org/x/image/font` + truetype) rather than reusing luma.

_Revised after Codex Act 2: dropped the "sub-pixel scroll / 60fps comfortable / few
hundred bytes" claims in favour of integer-pixel scroll and a benchmark-gated frame rate._

_Revised after Fable review: **full-frame flush every frame is the benchmarked baseline**;
dirty-region tracking is built only if the M1 benchmark proves it necessary (likely
deletable — the reference scenes dirty most of the panel per tick). Use
`x/image/font/opentype` (sfnt), not the frozen `freetype/truetype`; derive layout
constants from captured reference frames (Go metrics ≠ PIL FreeType, sfnt is unhinted).
Encode `spidev` 4096 B `bufsiz` chunking and the 256-wide panel's **column offset 0x1C**
+ 4-pixel alignment in the golden-byte tests._
