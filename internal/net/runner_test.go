package net

import (
	"context"
	"testing"
)

func TestExecRunnerRunsCommand(t *testing.T) {
	r := NewExecRunner()
	out, err := r.Run(context.Background(), "echo", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello\n" {
		t.Fatalf("out = %q, want %q", out, "hello\n")
	}
}

func TestExecRunnerReturnsErrorWithOutput(t *testing.T) {
	r := NewExecRunner()
	out, err := r.Run(context.Background(), "sh", "-c", "echo oops >&2; exit 3")
	if err == nil {
		t.Fatal("want error for exit 3")
	}
	if out != "oops\n" {
		t.Fatalf("combined output = %q, want stderr captured", out)
	}
}

func TestFakeRunnerScriptsByLongestPrefix(t *testing.T) {
	f := NewFakeRunner()
	f.Script("wpa_cli -i wlan0 status", "wpa_state=COMPLETED\n", nil)
	f.Script("wpa_cli", "OK\n", nil)

	out, err := f.Run(context.Background(), "wpa_cli", "-i", "wlan0", "status")
	if err != nil || out != "wpa_state=COMPLETED\n" {
		t.Fatalf("longest prefix should win: %q %v", out, err)
	}
	out, _ = f.Run(context.Background(), "wpa_cli", "-i", "wlan0", "select_network", "1")
	if out != "OK\n" {
		t.Fatalf("fallback prefix: %q", out)
	}
	if calls := f.Calls(); len(calls) != 2 || calls[1] != "wpa_cli -i wlan0 select_network 1" {
		t.Fatalf("calls recorded wrong: %v", calls)
	}
}

func TestFakeRunnerUnscriptedIsError(t *testing.T) {
	f := NewFakeRunner()
	if _, err := f.Run(context.Background(), "rm", "-rf", "/"); err == nil {
		t.Fatal("unscripted command must error, not silently succeed")
	}
}
