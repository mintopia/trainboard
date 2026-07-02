# SSD1322 flush benchmarks

Run on the target Pi Zero 2 W + SSD1322 to gate the render architecture.

## How to run

    GOOS=linux GOARCH=arm64 go build -o bench ./cmd/bench
    scp bench pi@trainboard:/tmp/ && ssh pi@trainboard /tmp/bench --frames 300 --hz 16000000

## Results (fill in)

| SPI Hz | full-frame ms | full-frame fps | 256x12 ms | 256x24 ms | date | notes |
|--------|---------------|----------------|-----------|-----------|------|-------|
|        |               |                |           |           |      |       |

## Decision

- [ ] Full-frame flush clears ≥25fps ⇒ **keep full-frame baseline, do NOT build dirty-region tracking** (delete the complexity from the render loop plan).
- [ ] Full-frame flush is below target ⇒ record the ceiling and design dirty-region flush in Plan C using `FlushRegion`.

Update ADR 0002 with the measured numbers and the decision.
