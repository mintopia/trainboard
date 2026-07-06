package main

import (
	"errors"
	"testing"
)

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
	tee := newTeeFlusher(a, b)

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
	tee := newTeeFlusher(a, b)

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
	tee := newTeeFlusher(a, b)

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
	tee := newTeeFlusher(a, b)

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
	tee := newTeeFlusher(a, b)

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
	tee := newTeeFlusher(a, b)

	err := tee.SetContrast(10)
	if err != nil {
		t.Fatalf("SetContrast err = %v, want nil", err)
	}
	if a.contrastCalls != 1 || b.contrastCalls != 1 {
		t.Fatalf("contrastCalls a=%d b=%d, want 1/1", a.contrastCalls, b.contrastCalls)
	}
}
