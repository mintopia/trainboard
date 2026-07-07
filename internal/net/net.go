// Package net owns wlan0: the Connectivity Manager state machine, the
// layered connectivity check, and the AP drivers (wpa_supplicant mode=2,
// hostapd fallback), all driving OS side effects through the Runner seam
// (ADR 0003; M3 design spec). Pure logic is host-testable against
// FakeRunner; nothing here executes commands except ExecRunner.
package net
