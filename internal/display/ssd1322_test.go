package display

import (
	"reflect"
	"testing"
)

func cmds(ops []Op) [][]byte {
	var out [][]byte
	for _, op := range ops {
		if op.Kind == OpCommand {
			out = append(out, op.Bytes)
		}
	}
	return out
}

func TestInitSequence(t *testing.T) {
	f := NewFake()
	d := New(f)
	if err := d.Init(); err != nil {
		t.Fatal(err)
	}
	if f.Ops[0].Kind != OpReset {
		t.Fatalf("Init must begin with a reset, got %v", f.Ops[0].Kind)
	}
	want := [][]byte{
		{0xFD, 0x12},
		{0xAE},
		{0xB3, 0x91},
		{0xCA, 0x3F},
		{0xA2, 0x00},
		{0xA1, 0x00},
		{0xA0, 0x14, 0x11},
		{0xB5, 0x00},
		{0xAB, 0x01},
		{0xB4, 0xA0, 0xFD},
		{0xC1, 0x9F},
		{0xC7, 0x0F},
		{0xB1, 0xE2},
		{0xD1, 0xA2, 0x20},
		{0xBB, 0x1F},
		{0xB6, 0x08},
		{0xBE, 0x07},
		{0xA6},
		{0xA9},
		{0xAF},
	}
	if got := cmds(f.Ops); !reflect.DeepEqual(got, want) {
		t.Fatalf("init commands mismatch\n got=%#v\nwant=%#v", got, want)
	}
}

func TestSetContrast(t *testing.T) {
	f := NewFake()
	d := New(f)
	if err := d.SetContrast(0x7F); err != nil {
		t.Fatal(err)
	}
	got := cmds(f.Ops)
	want := [][]byte{{0xC1, 0x7F}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SetContrast cmds = %#v, want %#v", got, want)
	}
}

func TestFlushWindowAndChunks(t *testing.T) {
	f := NewFake()
	d := New(f)
	if err := d.Flush(make([]byte, 8192)); err != nil {
		t.Fatal(err)
	}
	// First three ops set column window, row window, and Write-RAM.
	wantCmds := [][]byte{
		{0x15, 0x1C, 0x5B},
		{0x75, 0x00, 0x3F},
		{0x5C},
	}
	if got := cmds(f.Ops); !reflect.DeepEqual(got, wantCmds) {
		t.Fatalf("flush cmds = %#v, want %#v", got, wantCmds)
	}
	// Data must arrive as two 4096-byte chunks after the commands.
	var dataLens []int
	for _, op := range f.Ops {
		if op.Kind == OpData {
			dataLens = append(dataLens, len(op.Bytes))
		}
	}
	if len(dataLens) != 2 || dataLens[0] != 4096 || dataLens[1] != 4096 {
		t.Fatalf("data chunks = %v, want [4096 4096]", dataLens)
	}
}

func TestFlushRejectsWrongSize(t *testing.T) {
	d := New(NewFake())
	if err := d.Flush(make([]byte, 100)); err == nil {
		t.Fatal("expected error for wrong-size frame")
	}
}
