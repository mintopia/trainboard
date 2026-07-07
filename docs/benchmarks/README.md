# SSD1322 flush benchmarks

Run on the target Pi Zero 2 W + SSD1322 to gate the render architecture.

## How to run

    GOOS=linux GOARCH=arm64 go build -o bench ./cmd/bench
    ssh root@trainboard 'cat > /tmp/bench && chmod +x /tmp/bench' < bench
    ssh root@trainboard /tmp/bench --frames 300 --hz 16000000

(DietPi ships Dropbear — no `scp`/sftp on the device, so pipe over plain
ssh. User is `root`, matching docs/deploy.md. Stop `trainboard.service`
first so the bench has the SPI bus to itself.)

## Results (fill in)

| SPI Hz | full-frame ms | full-frame fps | 256x12 ms | 256x24 ms | date | notes |
|--------|---------------|----------------|-----------|-----------|------|-------|
| 16 MHz | 4.497 | 222.4 | 0.935 | 1.746 | 2026-07-07 | Pi Zero 2 W, DietPi Bookworm 6.12.93, 300 frames |
| 10 MHz | 6.782 | 147.4 | 1.369 | 2.606 | 2026-07-07 | same rig; conservative clock still ~6x over target |

## Decision

- [x] Full-frame flush clears ≥25fps ⇒ **keep full-frame baseline, do NOT build dirty-region tracking** (delete the complexity from the render loop plan). Measured 222.4fps at 16 MHz — 8.9x the 25fps gate; even the conservative 10 MHz clock gives 147.4fps. In-service journal timings agree (`flush_us≈4500`).
- [ ] ~~Full-frame flush is below target ⇒ record the ceiling and design dirty-region flush in Plan C using `FlushRegion`.~~ Not needed.

Update ADR 0002 with the measured numbers and the decision.
