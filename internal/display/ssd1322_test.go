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

func TestFlushRegionWindowMath(t *testing.T) {
	f := NewFake()
	d := New(f)
	// A 12-row-tall band at y=20, full width.
	rowData := make([]byte, 12*(256/2))
	if err := d.FlushRegion(rowData, 0, 20, 256, 12); err != nil {
		t.Fatal(err)
	}
	wantCmds := [][]byte{
		{0x15, 0x1C, 0x5B},         // full-width columns
		{0x75, byte(20), byte(31)}, // rows 20..31
		{0x5C},
	}
	if got := cmds(f.Ops); !reflect.DeepEqual(got, wantCmds) {
		t.Fatalf("region cmds = %#v, want %#v", got, wantCmds)
	}
}

func TestFlushRegionOffsetColumns(t *testing.T) {
	f := NewFake()
	d := New(f)
	// x=8 (2 columns in), w=8 (2 columns wide): col start 0x1C+2, end +3.
	if err := d.FlushRegion(make([]byte, 4*(8/2)), 8, 0, 8, 4); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x15, 0x1C + 2, 0x1C + 3}
	if got := cmds(f.Ops)[0]; !reflect.DeepEqual(got, want) {
		t.Fatalf("col window = % X, want % X", got, want)
	}
}

func TestFlushRegionAlignment(t *testing.T) {
	d := New(NewFake())
	if err := d.FlushRegion(make([]byte, 10), 1, 0, 8, 1); err == nil {
		t.Fatal("expected error for x not 4-aligned")
	}
	if err := d.FlushRegion(make([]byte, 10), 0, 0, 6, 1); err == nil {
		t.Fatal("expected error for w not 4-aligned")
	}
}

func TestFlushRegionDataLenCheck(t *testing.T) {
	d := New(NewFake())
	if err := d.FlushRegion(make([]byte, 5), 0, 0, 8, 2); err == nil {
		t.Fatal("expected error for wrong rowData length")
	}
}
