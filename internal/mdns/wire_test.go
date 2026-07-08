package mdns

import (
	"bytes"
	"net"
	"strings"
	"testing"
)

// --- test-only decode helpers (production code never decodes records; the
// responder only ever decodes incoming queries) ---

// decodeTestAnswers parses a full message's header, questions and answer
// records for round-trip assertions in these tests.
func decodeTestAnswers(t *testing.T, pkt []byte) (header, []question, []record) {
	t.Helper()

	if len(pkt) < 12 {
		t.Fatalf("packet too short: %d bytes", len(pkt))
	}
	h := header{
		ID:      be16(pkt, 0),
		Flags:   be16(pkt, 2),
		QDCount: be16(pkt, 4),
		ANCount: be16(pkt, 6),
		NSCount: be16(pkt, 8),
		ARCount: be16(pkt, 10),
	}

	offset := 12
	qs := make([]question, 0, h.QDCount)
	for i := 0; i < int(h.QDCount); i++ {
		name, next, err := decodeName(pkt, offset)
		if err != nil {
			t.Fatalf("decode question %d name: %v", i, err)
		}
		offset = next
		qs = append(qs, question{Name: name, Type: be16(pkt, offset), Class: be16(pkt, offset+2)})
		offset += 4
	}

	ans := make([]record, 0, h.ANCount)
	for i := 0; i < int(h.ANCount); i++ {
		name, next, err := decodeName(pkt, offset)
		if err != nil {
			t.Fatalf("decode answer %d name: %v", i, err)
		}
		offset = next
		rType := be16(pkt, offset)
		rClass := be16(pkt, offset+2)
		ttl := be32(pkt, offset+4)
		rdlen := int(be16(pkt, offset+8))
		offset += 10
		rdata := pkt[offset : offset+rdlen]
		offset += rdlen

		r := record{Name: name, Type: rType, Class: rClass, TTL: ttl}
		switch rType {
		case TypeA:
			r.Data = rdataA{IP: net.IP(append([]byte(nil), rdata...))}
		case TypeAAAA:
			r.Data = rdataAAAA{IP: net.IP(append([]byte(nil), rdata...))}
		case TypePTR:
			target, _, err := decodeName(rdata, 0)
			if err != nil {
				t.Fatalf("decode PTR target: %v", err)
			}
			r.Data = rdataPTR{Target: target}
		case TypeSRV:
			target, _, err := decodeName(rdata, 6)
			if err != nil {
				t.Fatalf("decode SRV target: %v", err)
			}
			r.Data = rdataSRV{
				Priority: be16(rdata, 0),
				Weight:   be16(rdata, 2),
				Port:     be16(rdata, 4),
				Target:   target,
			}
		case TypeTXT:
			var pairs []string
			p := 0
			for p < len(rdata) {
				n := int(rdata[p])
				p++
				pairs = append(pairs, string(rdata[p:p+n]))
				p += n
			}
			r.Data = rdataTXT{Pairs: pairs}
		default:
			t.Fatalf("unhandled rdata type %d in test decoder", rType)
		}
		ans = append(ans, r)
	}

	return h, qs, ans
}

func be16(b []byte, off int) uint16 {
	return uint16(b[off])<<8 | uint16(b[off+1])
}

func be32(b []byte, off int) uint32 {
	return uint32(b[off])<<24 | uint32(b[off+1])<<16 | uint32(b[off+2])<<8 | uint32(b[off+3])
}

func TestEncodeARecord(t *testing.T) {
	h := header{ID: 0, Flags: 0x8400}
	ans := []record{{
		Name:  "trainboard-ab12.local",
		Type:  TypeA,
		Class: 0x8001,
		TTL:   120,
		Data:  rdataA{IP: net.ParseIP("10.55.0.1")},
	}}

	got, err := encodeMessage(h, nil, ans)
	if err != nil {
		t.Fatalf("encodeMessage: %v", err)
	}

	var want bytes.Buffer
	want.Write([]byte{0x00, 0x00, 0x84, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00})
	want.WriteByte(0x0f)
	want.WriteString("trainboard-ab12")
	want.WriteByte(0x05)
	want.WriteString("local")
	want.WriteByte(0x00)
	want.Write([]byte{0x00, 0x01})             // TypeA
	want.Write([]byte{0x80, 0x01})             // Class cache-flush | IN
	want.Write([]byte{0x00, 0x00, 0x00, 0x78}) // TTL 120
	want.Write([]byte{0x00, 0x04})             // RDLENGTH
	want.Write([]byte{10, 55, 0, 1})

	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("encodeMessage bytes mismatch\n got: % x\nwant: % x", got, want.Bytes())
	}
}

func TestEncodeSRVAndTXT(t *testing.T) {
	h := header{Flags: 0x8400}
	ans := []record{
		{
			Name:  "Trainboard AB12._http._tcp.local",
			Type:  TypeSRV,
			Class: 0x8001,
			TTL:   4500,
			Data: rdataSRV{
				Priority: 0,
				Weight:   0,
				Port:     80,
				Target:   "trainboard-ab12.local",
			},
		},
		{
			Name:  "Trainboard AB12._http._tcp.local",
			Type:  TypeTXT,
			Class: 0x8001,
			TTL:   4500,
			Data:  rdataTXT{Pairs: []string{"path=/"}},
		},
	}

	pkt, err := encodeMessage(h, nil, ans)
	if err != nil {
		t.Fatalf("encodeMessage: %v", err)
	}

	_, _, decoded := decodeTestAnswers(t, pkt)
	if len(decoded) != 2 {
		t.Fatalf("got %d answers, want 2", len(decoded))
	}

	srv, ok := decoded[0].Data.(rdataSRV)
	if !ok {
		t.Fatalf("answer 0 Data is %T, want rdataSRV", decoded[0].Data)
	}
	if srv.Priority != 0 || srv.Weight != 0 || srv.Port != 80 || srv.Target != "trainboard-ab12.local" {
		t.Fatalf("SRV mismatch: %+v", srv)
	}
	if decoded[0].TTL != 4500 {
		t.Fatalf("SRV TTL = %d, want 4500", decoded[0].TTL)
	}

	txt, ok := decoded[1].Data.(rdataTXT)
	if !ok {
		t.Fatalf("answer 1 Data is %T, want rdataTXT", decoded[1].Data)
	}
	if len(txt.Pairs) != 1 || txt.Pairs[0] != "path=/" {
		t.Fatalf("TXT mismatch: %+v", txt)
	}

	// Sanity-check the exact TXT wire bytes (\x06path=/) land somewhere in
	// the encoded packet, per the brief's literal expectation.
	if !bytes.Contains(pkt, []byte("\x06path=/")) {
		t.Fatalf("encoded packet missing literal TXT bytes: % x", pkt)
	}
}

func buildPlainQuery(t *testing.T, id uint16, name string, qtype, qclass uint16) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{byte(id >> 8), byte(id)})
	buf.Write([]byte{0x00, 0x00}) // flags
	buf.Write([]byte{0x00, 0x01}) // QDCount = 1
	buf.Write([]byte{0x00, 0x00}) // ANCount
	buf.Write([]byte{0x00, 0x00}) // NSCount
	buf.Write([]byte{0x00, 0x00}) // ARCount
	if err := encodeName(&buf, name); err != nil {
		t.Fatalf("encodeName: %v", err)
	}
	buf.Write([]byte{byte(qtype >> 8), byte(qtype)})
	buf.Write([]byte{byte(qclass >> 8), byte(qclass)})
	return buf.Bytes()
}

func TestDecodeQueryPlain(t *testing.T) {
	pkt := buildPlainQuery(t, 0x1234, "_http._tcp.local", TypePTR, 0x8001)

	h, qs, err := decodeQuery(pkt)
	if err != nil {
		t.Fatalf("decodeQuery: %v", err)
	}
	if h.ID != 0x1234 {
		t.Fatalf("ID = %#x, want 0x1234", h.ID)
	}
	if h.QDCount != 1 {
		t.Fatalf("QDCount = %d, want 1", h.QDCount)
	}
	if len(qs) != 1 {
		t.Fatalf("got %d questions, want 1", len(qs))
	}
	if qs[0].Name != "_http._tcp.local" {
		t.Fatalf("Name = %q, want _http._tcp.local", qs[0].Name)
	}
	if qs[0].Type != TypePTR {
		t.Fatalf("Type = %d, want TypePTR", qs[0].Type)
	}
	if qs[0].Class != 0x0001 {
		t.Fatalf("Class = %#x, want 0x0001 (unicast-response bit masked off)", qs[0].Class)
	}
}

func TestDecodeQueryCompressed(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{0x00, 0x00}) // ID
	buf.Write([]byte{0x00, 0x00}) // flags
	buf.Write([]byte{0x00, 0x02}) // QDCount = 2
	buf.Write([]byte{0x00, 0x00})
	buf.Write([]byte{0x00, 0x00})
	buf.Write([]byte{0x00, 0x00})

	firstNameOffset := buf.Len()
	if err := encodeName(&buf, "_http._tcp.local"); err != nil {
		t.Fatalf("encodeName: %v", err)
	}
	buf.Write([]byte{0x00, 0x0c}) // TypePTR
	buf.Write([]byte{0x00, 0x01}) // Class IN

	// Second question: name is a compression pointer back to the first.
	if firstNameOffset > 0x3FFF {
		t.Fatalf("test setup: offset too large for a pointer")
	}
	ptr := uint16(0xC000) | uint16(firstNameOffset)
	buf.Write([]byte{byte(ptr >> 8), byte(ptr)})
	buf.Write([]byte{0x00, 0x01}) // TypeA
	buf.Write([]byte{0x00, 0x01}) // Class IN

	h, qs, err := decodeQuery(buf.Bytes())
	if err != nil {
		t.Fatalf("decodeQuery: %v", err)
	}
	if h.QDCount != 2 {
		t.Fatalf("QDCount = %d, want 2", h.QDCount)
	}
	if len(qs) != 2 {
		t.Fatalf("got %d questions, want 2", len(qs))
	}
	if qs[0].Name != "_http._tcp.local" {
		t.Fatalf("question 0 Name = %q, want _http._tcp.local", qs[0].Name)
	}
	if qs[1].Name != "_http._tcp.local" {
		t.Fatalf("question 1 (compressed) Name = %q, want _http._tcp.local", qs[1].Name)
	}
	if qs[1].Type != TypeA {
		t.Fatalf("question 1 Type = %d, want TypeA", qs[1].Type)
	}
}

func TestDecodeTruncatedSafe(t *testing.T) {
	full := buildPlainQuery(t, 0xABCD, "_http._tcp.local", TypePTR, 0x0001)

	for n := 0; n < len(full); n++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("decodeQuery panicked at length %d: %v", n, r)
				}
			}()
			_, _, err := decodeQuery(full[:n])
			if err == nil {
				t.Fatalf("decodeQuery(prefix of length %d) succeeded, want error", n)
			}
		}()
	}

	// The full, untruncated packet must still decode cleanly.
	if _, _, err := decodeQuery(full); err != nil {
		t.Fatalf("decodeQuery(full) = %v, want nil", err)
	}
}

// TestDecodeQueryHugeQDCountNoBody: a bare 12-byte header claiming a
// maximal QDCount (65535) with no question bytes following it must error
// out (there's no body to decode a single question from) rather than
// pre-allocate a slice sized off the attacker-controlled QDCount. The
// allocation bound itself is structural (capped against the packet's
// remaining length in decodeQuery), so this test only asserts the
// resulting error — it cannot directly observe the allocation size.
func TestDecodeQueryHugeQDCountNoBody(t *testing.T) {
	pkt := []byte{
		0x00, 0x00, // ID
		0x00, 0x00, // flags
		0xFF, 0xFF, // QDCount = 65535
		0x00, 0x00, // ANCount
		0x00, 0x00, // NSCount
		0x00, 0x00, // ARCount
	}

	if _, _, err := decodeQuery(pkt); err == nil {
		t.Fatalf("decodeQuery(header-only, QDCount=65535) succeeded, want error")
	}
}

func TestEncodeNameTooLong(t *testing.T) {
	t.Run("label exceeds 63 bytes", func(t *testing.T) {
		name := strings.Repeat("a", 64) + ".local"
		h := header{}
		ans := []record{{Name: name, Type: TypeA, Class: 0x0001, TTL: 120, Data: rdataA{IP: net.ParseIP("10.0.0.1")}}}
		if _, err := encodeMessage(h, nil, ans); err == nil {
			t.Fatalf("encodeMessage with 64-byte label succeeded, want error")
		}
	})

	t.Run("name exceeds 255 bytes", func(t *testing.T) {
		labels := []string{
			strings.Repeat("a", 60),
			strings.Repeat("b", 60),
			strings.Repeat("c", 60),
			strings.Repeat("d", 60),
			strings.Repeat("e", 60),
		}
		name := strings.Join(labels, ".")
		h := header{}
		ans := []record{{Name: name, Type: TypeA, Class: 0x0001, TTL: 120, Data: rdataA{IP: net.ParseIP("10.0.0.1")}}}
		if _, err := encodeMessage(h, nil, ans); err == nil {
			t.Fatalf("encodeMessage with >255 byte name succeeded, want error")
		}
	})
}
