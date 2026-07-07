package main

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/mintopia/trainboard/internal/obs"
)

// testLog is a *slog.Logger discarding output, for tests that don't assert
// on log content.
func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeFlusher records every Flush/SetContrast call and can be made to fail.
type fakeFlusher struct {
	flushCalls    int
	contrastCalls int
	flushErr      error
	contrastErr   error
	lastPacked    []byte
	lastContrast  byte
}

func (f *fakeFlusher) Flush(packed []byte) error {
	f.flushCalls++
	f.lastPacked = packed
	return f.flushErr
}

func (f *fakeFlusher) SetContrast(level byte) error {
	f.contrastCalls++
	f.lastContrast = level
	return f.contrastErr
}

func TestTeeFlusherCallsBothFlush(t *testing.T) {
	a := &fakeFlusher{}
	b := &fakeFlusher{}
	tee := newTeeFlusher(a, b, testLog())

	if err := tee.Flush([]byte{1, 2, 3}); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if a.flushCalls != 1 || b.flushCalls != 1 {
		t.Fatalf("flushCalls a=%d b=%d, want 1/1", a.flushCalls, b.flushCalls)
	}
}

func TestTeeFlusherCallsBothSetContrast(t *testing.T) {
	a := &fakeFlusher{}
	b := &fakeFlusher{}
	tee := newTeeFlusher(a, b, testLog())

	if err := tee.SetContrast(42); err != nil {
		t.Fatalf("SetContrast: %v", err)
	}
	if a.contrastCalls != 1 || b.contrastCalls != 1 {
		t.Fatalf("contrastCalls a=%d b=%d, want 1/1", a.contrastCalls, b.contrastCalls)
	}
	if a.lastContrast != 42 || b.lastContrast != 42 {
		t.Fatalf("lastContrast a=%d b=%d, want 42/42", a.lastContrast, b.lastContrast)
	}
}

func TestTeeFlusherFlushPropagatesAErrorButStillCallsB(t *testing.T) {
	wantErr := errors.New("panel down")
	a := &fakeFlusher{flushErr: wantErr}
	b := &fakeFlusher{}
	tee := newTeeFlusher(a, b, testLog())

	err := tee.Flush([]byte{9})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Flush err = %v, want %v", err, wantErr)
	}
	if b.flushCalls != 1 {
		t.Fatalf("b.flushCalls = %d, want 1 (preview must still run when panel errors)", b.flushCalls)
	}
}

func TestTeeFlusherFlushSwallowsBErrorWhenANil(t *testing.T) {
	a := &fakeFlusher{}
	b := &fakeFlusher{flushErr: errors.New("preview disk full")}
	tee := newTeeFlusher(a, b, testLog())

	err := tee.Flush([]byte{9})
	if err != nil {
		t.Fatalf("Flush err = %v, want nil (b's error must be swallowed)", err)
	}
	if a.flushCalls != 1 || b.flushCalls != 1 {
		t.Fatalf("flushCalls a=%d b=%d, want 1/1", a.flushCalls, b.flushCalls)
	}
}

func TestTeeFlusherSetContrastPropagatesAErrorButStillCallsB(t *testing.T) {
	wantErr := errors.New("panel contrast fault")
	a := &fakeFlusher{contrastErr: wantErr}
	b := &fakeFlusher{}
	tee := newTeeFlusher(a, b, testLog())

	err := tee.SetContrast(10)
	if !errors.Is(err, wantErr) {
		t.Fatalf("SetContrast err = %v, want %v", err, wantErr)
	}
	if b.contrastCalls != 1 {
		t.Fatalf("b.contrastCalls = %d, want 1", b.contrastCalls)
	}
}

func TestTeeFlusherSetContrastSwallowsBErrorWhenANil(t *testing.T) {
	a := &fakeFlusher{}
	b := &fakeFlusher{contrastErr: errors.New("preview contrast noop failed")}
	tee := newTeeFlusher(a, b, testLog())

	err := tee.SetContrast(10)
	if err != nil {
		t.Fatalf("SetContrast err = %v, want nil", err)
	}
	if a.contrastCalls != 1 || b.contrastCalls != 1 {
		t.Fatalf("contrastCalls a=%d b=%d, want 1/1", a.contrastCalls, b.contrastCalls)
	}
}

// TestTeeFlusherFlushLogsBErrorThroughProvidedLogger guards the T10
// implementer concern: b's swallowed Flush error must reach the caller's
// logger (in production, the obs logger so it surfaces via /events), not
// slog.Default().
func TestTeeFlusherFlushLogsBErrorThroughProvidedLogger(t *testing.T) {
	ring := obs.NewRing(obs.DefaultRingCapacity)
	log := obs.NewLogger(io.Discard, ring, slog.LevelWarn)
	a := &fakeFlusher{}
	b := &fakeFlusher{flushErr: errors.New("preview disk full")}
	tee := newTeeFlusher(a, b, log)

	if err := tee.Flush([]byte{9}); err != nil {
		t.Fatalf("Flush err = %v, want nil", err)
	}

	var found bool
	for _, e := range ring.Events() {
		if e.Msg == "preview flush failed" {
			found = true
			if e.Attrs["err"] != "preview disk full" {
				t.Fatalf("unexpected attrs: %+v", e.Attrs)
			}
		}
	}
	if !found {
		t.Fatal("expected a \"preview flush failed\" event in the obs ring")
	}
}

// TestTeeFlusherSetContrastLogsBErrorThroughProvidedLogger is the
// SetContrast counterpart of TestTeeFlusherFlushLogsBErrorThroughProvidedLogger.
func TestTeeFlusherSetContrastLogsBErrorThroughProvidedLogger(t *testing.T) {
	ring := obs.NewRing(obs.DefaultRingCapacity)
	log := obs.NewLogger(io.Discard, ring, slog.LevelWarn)
	a := &fakeFlusher{}
	b := &fakeFlusher{contrastErr: errors.New("preview contrast noop failed")}
	tee := newTeeFlusher(a, b, log)

	if err := tee.SetContrast(10); err != nil {
		t.Fatalf("SetContrast err = %v, want nil", err)
	}

	var found bool
	for _, e := range ring.Events() {
		if e.Msg == "preview set contrast failed" {
			found = true
			if e.Attrs["err"] != "preview contrast noop failed" {
				t.Fatalf("unexpected attrs: %+v", e.Attrs)
			}
		}
	}
	if !found {
		t.Fatal("expected a \"preview set contrast failed\" event in the obs ring")
	}
}
