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
