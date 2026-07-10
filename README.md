# Trainboard

A UK railway departure board for a 256×64 SSD1322 OLED panel, driven by a
Raspberry Pi Zero 2 W — the kind of thing that used to hang above a platform,
now on your desk. It pulls live departures from National Rail's Darwin feed,
is configured entirely from a phone-friendly web UI, sets itself up over its
own WiFi hotspot the first time it boots, answers to `trainboard.local` on
the LAN, and updates itself over the air once it's signed and released. An
optional column of train headcodes (from the next release onward), sourced from
the RealTime Trains API, can be switched on for anyone who wants that extra bit
of railway detail.

<!-- TODO: swap in a real photo of a running board once one exists. -->
![Trainboard OLED panel showing a departures screen](docs/images/board.jpg)

## Hardware

| Part | Notes |
|---|---|
| Raspberry Pi Zero 2 W | the image is arm64 — the original Pi Zero W is **not supported** |
| SSD1322 256×64 SPI OLED panel | |
| microSD card, 4GB or larger | |

Wiring (BCM numbering): MOSI/SCLK/CS on SPI0 (GPIO10/GPIO11/CE0), D/C on
GPIO24, RST on GPIO25.

## Install

1. Download the latest SD card image:
   https://github-files.mintopia.net/trainboard/trainboard-latest.img.xz
2. Flash it with [Raspberry Pi Imager](https://www.raspberrypi.com/software/)
   or [balenaEtcher](https://www.balena.io/etcher/) — pick "Use custom
   image"; no OS customisation is needed, the image configures itself on
   first boot.
3. Insert the card into the Pi Zero 2 W and power it on. First boot takes a
   couple of minutes.
4. The board comes up as its own WiFi hotspot and shows the network name and
   a setup address on the panel. Join that network from your phone or laptop
   and open the address in a browser.
5. The setup page walks you through your WiFi network, an admin password,
   your home station, and a Darwin token. Submit it and the board restarts
   onto your network showing live departures.

You'll need a free Darwin (OpenLDBWS) token to fetch departures — register
for one at
[realtime.nationalrail.co.uk/OpenLDBWSRegistration](https://realtime.nationalrail.co.uk/OpenLDBWSRegistration/).
If you also want the optional headcode column (from the next release onward), sign up
for a free [RealTime Trains API](https://api.rtt.io/) account and add its credentials
on the board's Network settings page — everything else works without it.

## Manual install, updating, and troubleshooting

For flashing a stock DietPi image by hand, the A/B slot self-update system,
on-panel fault codes, and the USB gadget lifeline for recovering a board
that's fallen off the network, see the full [operator deploy
guide](docs/deploy.md). Once a board is running, updates are applied from
its own web UI (or fully automatically overnight, if you enable that).

## Development

Requires Go 1.26 or newer. `make check` runs `go vet`, the linter, and the
test suite. `cmd/trainboard --fixture <path>` runs the board against a JSON
departures fixture instead of live Darwin data, so you can iterate on
rendering without any hardware attached (see the flags table in
[docs/deploy.md](docs/deploy.md) §5). Design docs and ADRs recording the
project's architectural decisions live under [docs/](docs/).

## License

MIT — see [LICENSE](LICENSE).
