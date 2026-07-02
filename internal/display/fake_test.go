package display

import (
	"reflect"
	"testing"
)

func TestFakeTransportRecordsOps(t *testing.T) {
	f := NewFake()
	if err := f.Reset(); err != nil {
		t.Fatal(err)
	}
	if err := f.Command(0x15, 0x1C, 0x5B); err != nil {
		t.Fatal(err)
	}
	if err := f.Data([]byte{0x0A, 0x0B}); err != nil {
		t.Fatal(err)
	}
	want := []Op{
		{Kind: OpReset},
		{Kind: OpCommand, Bytes: []byte{0x15, 0x1C, 0x5B}},
		{Kind: OpData, Bytes: []byte{0x0A, 0x0B}},
	}
	if !reflect.DeepEqual(f.Ops, want) {
		t.Fatalf("Ops = %#v, want %#v", f.Ops, want)
	}
}

func TestFakeTransportCopiesData(t *testing.T) {
	f := NewFake()
	buf := []byte{1, 2, 3}
	if err := f.Data(buf); err != nil {
		t.Fatal(err)
	}
	buf[0] = 99 // mutate caller's slice after the call
	if f.Ops[0].Bytes[0] != 1 {
		t.Fatalf("FakeTransport did not copy data; got %d", f.Ops[0].Bytes[0])
	}
}
