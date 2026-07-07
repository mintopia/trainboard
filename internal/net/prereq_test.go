package net

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestCheckPrereqsAllClear(t *testing.T) {
	// Unblocked + GB → nil, no calls beyond reads
	r := NewFakeRunner()
	r.Script("iw reg get", "country GB: (80, 88)\n", nil)

	var readCalls []string
	readFile := func(path string) ([]byte, error) {
		readCalls = append(readCalls, path)
		switch path {
		case "/sys/class/rfkill/rfkill0/type":
			return []byte("wlan"), nil
		case "/sys/class/rfkill/rfkill0/soft":
			return []byte("0"), nil
		default:
			return nil, fmt.Errorf("unexpected read: %s", path)
		}
	}

	globCalls := 0
	glob := func(pattern string) ([]string, error) {
		globCalls++
		if pattern == "/sys/class/rfkill/rfkill*/type" {
			return []string{"/sys/class/rfkill/rfkill0/type"}, nil
		}
		return nil, fmt.Errorf("unexpected glob: %s", pattern)
	}

	writeFile := func(_ string, _ []byte) error {
		t.Fatal("unexpected write")
		return nil
	}

	err := CheckPrereqs(context.Background(), r, readFile, writeFile, glob)
	if err != nil {
		t.Fatalf("CheckPrereqs() = %v, want nil", err)
	}

	// Verify no writes occurred
	if r.Calls() != nil && len(r.Calls()) > 0 {
		for _, call := range r.Calls() {
			if strings.HasPrefix(call, "pkill") || strings.HasPrefix(call, "iw reg set") {
				t.Fatalf("unexpected command: %s", call)
			}
		}
	}
}

func TestCheckPrereqsSoftBlockedOnce(t *testing.T) {
	// Soft-blocked once → writes "0", re-reads, passes
	r := NewFakeRunner()
	r.Script("iw reg get", "country GB: (80, 88)\n", nil)

	readState := 0 // Track state to simulate write
	var readCalls []string
	readFile := func(path string) ([]byte, error) {
		readCalls = append(readCalls, path)
		switch path {
		case "/sys/class/rfkill/rfkill0/type":
			return []byte("wlan"), nil
		case "/sys/class/rfkill/rfkill0/soft":
			// First read returns "1" (blocked), subsequent read after write returns "0"
			if readState == 0 {
				readState = 1
				return []byte("1"), nil
			}
			return []byte("0"), nil
		default:
			return nil, fmt.Errorf("unexpected read: %s", path)
		}
	}

	glob := func(pattern string) ([]string, error) {
		if pattern == "/sys/class/rfkill/rfkill*/type" {
			return []string{"/sys/class/rfkill/rfkill0/type"}, nil
		}
		return nil, fmt.Errorf("unexpected glob: %s", pattern)
	}

	writeFile := func(path string, data []byte) error {
		if path != "/sys/class/rfkill/rfkill0/soft" {
			t.Fatalf("unexpected write to %s", path)
		}
		if string(data) != "0" {
			t.Fatalf("wrote %q, want %q", string(data), "0")
		}
		return nil
	}

	err := CheckPrereqs(context.Background(), r, readFile, writeFile, glob)
	if err != nil {
		t.Fatalf("CheckPrereqs() = %v, want nil", err)
	}
}

func TestCheckPrereqsSoftBlockedPersistent(t *testing.T) {
	// Persistent block → E05-able error
	r := NewFakeRunner()
	r.Script("iw reg get", "country GB: (80, 88)\n", nil)

	readFile := func(path string) ([]byte, error) {
		switch path {
		case "/sys/class/rfkill/rfkill0/type":
			return []byte("wlan"), nil
		case "/sys/class/rfkill/rfkill0/soft":
			// Always returns "1" (blocked)
			return []byte("1"), nil
		default:
			return nil, fmt.Errorf("unexpected read: %s", path)
		}
	}

	glob := func(pattern string) ([]string, error) {
		if pattern == "/sys/class/rfkill/rfkill*/type" {
			return []string{"/sys/class/rfkill/rfkill0/type"}, nil
		}
		return nil, fmt.Errorf("unexpected glob: %s", pattern)
	}

	writeFile := func(_ string, _ []byte) error {
		// Simulate: write succeeds but soft-block persists
		return nil
	}

	err := CheckPrereqs(context.Background(), r, readFile, writeFile, glob)
	if err == nil {
		t.Fatalf("CheckPrereqs() = nil, want error for persistent block")
	}
	if !strings.Contains(err.Error(), "rfkill") || !strings.Contains(err.Error(), "soft-block") {
		t.Fatalf("error message missing context: %v", err)
	}
}

func TestCheckPrereqsCountryUnset(t *testing.T) {
	// country 00 → iw reg set GB issued, re-checks
	readFile := func(path string) ([]byte, error) {
		switch path {
		case "/sys/class/rfkill/rfkill0/type":
			return []byte("wlan"), nil
		case "/sys/class/rfkill/rfkill0/soft":
			return []byte("0"), nil
		default:
			return nil, fmt.Errorf("unexpected read: %s", path)
		}
	}

	glob := func(pattern string) ([]string, error) {
		if pattern == "/sys/class/rfkill/rfkill*/type" {
			return []string{"/sys/class/rfkill/rfkill0/type"}, nil
		}
		return nil, fmt.Errorf("unexpected glob: %s", pattern)
	}

	writeFile := func(_ string, _ []byte) error {
		return fmt.Errorf("unexpected write")
	}

	// Create a stateful runner
	getCallCount := 0
	r := &statefulRunner{
		calls: make([]string, 0),
		run: func(_ context.Context, argv ...string) (string, error) {
			cmd := strings.Join(argv, " ")
			if cmd == "iw reg get" {
				getCallCount++
				if getCallCount == 1 {
					return "country 00:\n", nil
				}
				return "country GB: (80, 88)\n", nil
			}
			if cmd == "iw reg set GB" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected command: %s", cmd)
		},
	}

	err := CheckPrereqs(context.Background(), r, readFile, writeFile, glob)
	if err != nil {
		t.Fatalf("CheckPrereqs() = %v, want nil", err)
	}

	// Verify iw reg set was called
	found := false
	for _, call := range r.calls {
		if strings.HasPrefix(call, "iw reg set GB") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("iw reg set GB not called; calls: %v", r.calls)
	}
}

// statefulRunner is a test helper for tracking calls with custom behavior
type statefulRunner struct {
	calls []string
	run   func(context.Context, ...string) (string, error)
}

func (s *statefulRunner) Run(ctx context.Context, argv ...string) (string, error) {
	s.calls = append(s.calls, strings.Join(argv, " "))
	return s.run(ctx, argv...)
}

func TestCheckPrereqsCountryUnsetPersistent(t *testing.T) {
	// country still 00 after set → error
	readFile := func(path string) ([]byte, error) {
		switch path {
		case "/sys/class/rfkill/rfkill0/type":
			return []byte("wlan"), nil
		case "/sys/class/rfkill/rfkill0/soft":
			return []byte("0"), nil
		default:
			return nil, fmt.Errorf("unexpected read: %s", path)
		}
	}

	glob := func(pattern string) ([]string, error) {
		if pattern == "/sys/class/rfkill/rfkill*/type" {
			return []string{"/sys/class/rfkill/rfkill0/type"}, nil
		}
		return nil, fmt.Errorf("unexpected glob: %s", pattern)
	}

	writeFile := func(_ string, _ []byte) error {
		return fmt.Errorf("unexpected write")
	}

	// Always returns country 00 (even after set attempt)
	r := &statefulRunner{
		calls: make([]string, 0),
		run: func(_ context.Context, argv ...string) (string, error) {
			cmd := strings.Join(argv, " ")
			if cmd == "iw reg get" {
				return "country 00:\n", nil
			}
			if cmd == "iw reg set GB" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected command: %s", cmd)
		},
	}

	err := CheckPrereqs(context.Background(), r, readFile, writeFile, glob)
	if err == nil {
		t.Fatalf("CheckPrereqs() = nil, want error for persistent country 00")
	}
	if !strings.Contains(err.Error(), "regulatory") {
		t.Fatalf("error message should mention regulatory; got: %v", err)
	}
}

func TestCheckPrereqsNoRfkillDevices(t *testing.T) {
	// No rfkill devices found → should pass
	r := NewFakeRunner()
	r.Script("iw reg get", "country GB: (80, 88)\n", nil)

	readFile := func(path string) ([]byte, error) {
		return nil, fmt.Errorf("unexpected read: %s", path)
	}

	glob := func(pattern string) ([]string, error) {
		if pattern == "/sys/class/rfkill/rfkill*/type" {
			return []string{}, nil
		}
		return nil, fmt.Errorf("unexpected glob: %s", pattern)
	}

	writeFile := func(_ string, _ []byte) error {
		return fmt.Errorf("unexpected write")
	}

	err := CheckPrereqs(context.Background(), r, readFile, writeFile, glob)
	if err != nil {
		t.Fatalf("CheckPrereqs() = %v, want nil", err)
	}
}

func TestCheckPrereqsIgnoresNonWlanRfkill(t *testing.T) {
	// Ignores rfkill devices whose type is not "wlan"
	r := NewFakeRunner()
	r.Script("iw reg get", "country GB: (80, 88)\n", nil)

	readFile := func(path string) ([]byte, error) {
		switch path {
		case "/sys/class/rfkill/rfkill0/type":
			return []byte("bluetooth"), nil
		case "/sys/class/rfkill/rfkill1/type":
			return []byte("wlan"), nil
		case "/sys/class/rfkill/rfkill1/soft":
			return []byte("0"), nil
		default:
			return nil, fmt.Errorf("unexpected read: %s", path)
		}
	}

	glob := func(pattern string) ([]string, error) {
		if pattern == "/sys/class/rfkill/rfkill*/type" {
			return []string{
				"/sys/class/rfkill/rfkill0/type",
				"/sys/class/rfkill/rfkill1/type",
			}, nil
		}
		return nil, fmt.Errorf("unexpected glob: %s", pattern)
	}

	writeFile := func(_ string, _ []byte) error {
		return fmt.Errorf("unexpected write")
	}

	err := CheckPrereqs(context.Background(), r, readFile, writeFile, glob)
	if err != nil {
		t.Fatalf("CheckPrereqs() = %v, want nil", err)
	}
}
