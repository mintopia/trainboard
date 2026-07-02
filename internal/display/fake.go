package display

// OpKind identifies a recorded transport operation.
type OpKind int

// Kinds of recorded transport operation.
const (
	OpReset OpKind = iota
	OpCommand
	OpData
)

// Op is a single recorded transport operation.
type Op struct {
	Kind  OpKind
	Bytes []byte
}

// FakeTransport is an in-memory Transport that records every operation for
// golden-byte assertions in tests.
type FakeTransport struct {
	Ops    []Op
	Closed bool
}

var _ Transport = (*FakeTransport)(nil)

// NewFake returns an empty FakeTransport.
func NewFake() *FakeTransport { return &FakeTransport{} }

// Command records an opcode plus its argument bytes.
func (f *FakeTransport) Command(cmd byte, args ...byte) error {
	b := append([]byte{cmd}, args...)
	f.Ops = append(f.Ops, Op{Kind: OpCommand, Bytes: b})
	return nil
}

// Data records a copy of the payload (callers may reuse their buffer).
func (f *FakeTransport) Data(p []byte) error {
	b := make([]byte, len(p))
	copy(b, p)
	f.Ops = append(f.Ops, Op{Kind: OpData, Bytes: b})
	return nil
}

// Reset records a reset pulse.
func (f *FakeTransport) Reset() error {
	f.Ops = append(f.Ops, Op{Kind: OpReset})
	return nil
}

// Close marks the transport closed.
func (f *FakeTransport) Close() error { f.Closed = true; return nil }
