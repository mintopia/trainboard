// Package mdns implements a minimal, announce-only mDNS responder for the
// board (RFC 6762/6763 subset: no probing, no conflict rename).
package mdns

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Record types this responder emits or matches queries against. Values are
// the standard DNS RR type numbers.
const (
	TypeA    uint16 = 1
	TypeAAAA uint16 = 28
	TypePTR  uint16 = 12
	TypeSRV  uint16 = 33
	TypeTXT  uint16 = 16
)

// classUnicastResponseBit is set by queriers on a question to request a
// unicast reply (RFC 6762 §5.4); this announce-only responder never honors
// it, but the bit must be masked off before comparing against ClassINET.
const classUnicastResponseBit uint16 = 0x8000

// maxNameLength is the wire-format limit for an encoded name (labels +
// length bytes + terminating root byte), per RFC 1035 §3.1.
const maxNameLength = 255

// maxPointerJumps bounds compression-pointer chains during decode so a
// hostile/looping packet cannot hang the parser.
const maxPointerJumps = 16

// header is the fixed 12-byte DNS message header.
type header struct {
	ID      uint16
	Flags   uint16 // response bit 0x8000 | authoritative 0x0400 for answers
	QDCount uint16
	ANCount uint16
	NSCount uint16
	ARCount uint16
}

// question is one entry of a message's question section.
type question struct {
	Name  string // fully qualified, lowercase, trailing dot stripped: "trainboard-ab12.local"
	Type  uint16 // TypeA etc.
	Class uint16 // 0x0001 (IN); top bit (unicast-response) masked off by parser
}

// record is one resource record, either an answer we emit or (unused today)
// an authority/additional record.
type record struct {
	Name  string
	Type  uint16 // TypeA=1, TypeAAAA=28, TypePTR=12, TypeSRV=33, TypeTXT=16
	Class uint16 // 0x0001, or 0x8001 (cache-flush) for our authoritative answers
	TTL   uint32 // 120 for A/AAAA, 4500 for PTR/SRV/TXT, 0 for goodbye
	Data  rdata
}

// rdata encodes a record's type-specific payload into the wire buffer.
type rdata interface{ encode(buf *bytes.Buffer) error }

// rdataA is the RDATA for a TypeA record: 4 bytes, an IPv4 address.
type rdataA struct{ IP net.IP }

// rdataAAAA is the RDATA for a TypeAAAA record: 16 bytes, an IPv6 address.
type rdataAAAA struct{ IP net.IP }

// rdataPTR is the RDATA for a TypePTR record: a single name.
type rdataPTR struct{ Target string }

// rdataSRV is the RDATA for a TypeSRV record.
type rdataSRV struct {
	Priority, Weight, Port uint16
	Target                 string
}

// rdataTXT is the RDATA for a TypeTXT record: a sequence of length-prefixed
// strings ("key=value" pairs by convention).
type rdataTXT struct{ Pairs []string }

func (d rdataA) encode(buf *bytes.Buffer) error {
	ip4 := d.IP.To4()
	if ip4 == nil {
		return fmt.Errorf("mdns: %v is not a valid IPv4 address", d.IP)
	}
	buf.Write(ip4)
	return nil
}

func (d rdataAAAA) encode(buf *bytes.Buffer) error {
	if d.IP.To4() != nil {
		return fmt.Errorf("mdns: %v is an IPv4 address, not IPv6", d.IP)
	}
	ip16 := d.IP.To16()
	if ip16 == nil {
		return fmt.Errorf("mdns: %v is not a valid IPv6 address", d.IP)
	}
	buf.Write(ip16)
	return nil
}

func (d rdataPTR) encode(buf *bytes.Buffer) error {
	return encodeName(buf, d.Target)
}

func (d rdataSRV) encode(buf *bytes.Buffer) error {
	putUint16(buf, d.Priority)
	putUint16(buf, d.Weight)
	putUint16(buf, d.Port)
	return encodeName(buf, d.Target)
}

func (d rdataTXT) encode(buf *bytes.Buffer) error {
	if len(d.Pairs) == 0 {
		// RFC 6763 §6.1: a TXT record must have at least one string, even
		// if empty.
		buf.WriteByte(0)
		return nil
	}
	for _, p := range d.Pairs {
		if len(p) > 255 {
			return fmt.Errorf("mdns: TXT entry exceeds 255 bytes: %q", p)
		}
		buf.WriteByte(byte(len(p)))
		buf.WriteString(p)
	}
	return nil
}

func putUint16(buf *bytes.Buffer, v uint16) {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	buf.Write(b[:])
}

func putUint32(buf *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	buf.Write(b[:])
}

// encodeName writes name in plain (uncompressed) wire format: a sequence of
// length-prefixed labels terminated by a zero-length root label. It never
// emits compression pointers.
func encodeName(buf *bytes.Buffer, name string) error {
	if name == "" {
		return buf.WriteByte(0)
	}

	labels := strings.Split(name, ".")
	total := 1 // terminating root byte
	for _, l := range labels {
		if l == "" {
			return fmt.Errorf("mdns: name %q has an empty label", name)
		}
		if len(l) > 63 {
			return fmt.Errorf("mdns: label %q in name %q exceeds 63 bytes", l, name)
		}
		total += len(l) + 1
	}
	if total > maxNameLength {
		return fmt.Errorf("mdns: encoded name %q exceeds %d bytes", name, maxNameLength)
	}

	for _, l := range labels {
		buf.WriteByte(byte(len(l)))
		buf.WriteString(l)
	}
	return buf.WriteByte(0)
}

// encodeQuestion writes one question-section entry.
func encodeQuestion(buf *bytes.Buffer, q question) error {
	if err := encodeName(buf, q.Name); err != nil {
		return err
	}
	putUint16(buf, q.Type)
	putUint16(buf, q.Class)
	return nil
}

// encodeRecord writes one resource-record entry, computing RDLENGTH from
// the encoded RDATA.
func encodeRecord(buf *bytes.Buffer, r record) error {
	if err := encodeName(buf, r.Name); err != nil {
		return err
	}
	putUint16(buf, r.Type)
	putUint16(buf, r.Class)
	putUint32(buf, r.TTL)

	var rbuf bytes.Buffer
	if r.Data != nil {
		if err := r.Data.encode(&rbuf); err != nil {
			return err
		}
	}
	if rbuf.Len() > 0xFFFF {
		return fmt.Errorf("mdns: rdata for %q exceeds 65535 bytes", r.Name)
	}
	putUint16(buf, uint16(rbuf.Len()))
	buf.Write(rbuf.Bytes())
	return nil
}

// encodeMessage builds a complete DNS/mDNS message. QDCount/ANCount in h are
// overwritten with the actual lengths of qs/ans; NSCount/ARCount pass
// through unchanged (this responder never emits authority/additional
// records). Encoding never emits name compression.
func encodeMessage(h header, qs []question, ans []record) ([]byte, error) {
	if len(qs) > 0xFFFF || len(ans) > 0xFFFF {
		return nil, errors.New("mdns: too many questions or answers to encode")
	}
	h.QDCount = uint16(len(qs))
	h.ANCount = uint16(len(ans))

	var buf bytes.Buffer
	putUint16(&buf, h.ID)
	putUint16(&buf, h.Flags)
	putUint16(&buf, h.QDCount)
	putUint16(&buf, h.ANCount)
	putUint16(&buf, h.NSCount)
	putUint16(&buf, h.ARCount)

	for i, q := range qs {
		if err := encodeQuestion(&buf, q); err != nil {
			return nil, fmt.Errorf("mdns: encode question %d: %w", i, err)
		}
	}
	for i, r := range ans {
		if err := encodeRecord(&buf, r); err != nil {
			return nil, fmt.Errorf("mdns: encode answer %d: %w", i, err)
		}
	}
	return buf.Bytes(), nil
}

// decodeName reads a (possibly compressed) name starting at offset in pkt.
// It returns the decoded, dot-joined name and the offset immediately after
// the name's own representation in the packet (i.e. after the first
// terminating zero byte or compression pointer encountered — NOT after any
// pointer target, per RFC 1035 §4.1.4). Compression pointers must point
// strictly backward (reject forward/self pointers, guarding against
// cycles) and are followed at most maxPointerJumps times.
func decodeName(pkt []byte, offset int) (string, int, error) {
	var labels []string
	pos := offset
	end := -1 // offset to resume normal parsing after this name, set once
	jumps := 0
	totalLen := 0

	for {
		if pos < 0 || pos >= len(pkt) {
			return "", 0, errors.New("mdns: name decode out of bounds")
		}
		b := pkt[pos]

		switch {
		case b == 0:
			pos++
			if end == -1 {
				end = pos
			}
			return strings.Join(labels, "."), end, nil

		case b&0xC0 == 0xC0:
			if pos+1 >= len(pkt) {
				return "", 0, errors.New("mdns: truncated compression pointer")
			}
			ptr := int(b&0x3F)<<8 | int(pkt[pos+1])
			if end == -1 {
				end = pos + 2
			}
			if ptr >= pos {
				return "", 0, errors.New("mdns: invalid forward or self-referencing compression pointer")
			}
			jumps++
			if jumps > maxPointerJumps {
				return "", 0, errors.New("mdns: too many compression pointer jumps")
			}
			pos = ptr

		case b&0xC0 != 0:
			return "", 0, errors.New("mdns: reserved label-length bits set")

		default:
			length := int(b)
			pos++
			if pos+length > len(pkt) {
				return "", 0, errors.New("mdns: truncated label")
			}
			totalLen += length + 1
			if totalLen > maxNameLength {
				return "", 0, errors.New("mdns: decoded name exceeds maximum length")
			}
			labels = append(labels, string(pkt[pos:pos+length]))
			pos += length
		}
	}
}

// decodeQuery parses a query message's header and question section only.
// Any records present (queriers may attach known-answer-suppression
// records) are ignored — this responder does not implement known-answer
// suppression, which is acceptable for an announce-only implementation.
func decodeQuery(pkt []byte) (header, []question, error) {
	if len(pkt) < 12 {
		return header{}, nil, errors.New("mdns: packet too short for header")
	}

	h := header{
		ID:      binary.BigEndian.Uint16(pkt[0:2]),
		Flags:   binary.BigEndian.Uint16(pkt[2:4]),
		QDCount: binary.BigEndian.Uint16(pkt[4:6]),
		ANCount: binary.BigEndian.Uint16(pkt[6:8]),
		NSCount: binary.BigEndian.Uint16(pkt[8:10]),
		ARCount: binary.BigEndian.Uint16(pkt[10:12]),
	}

	offset := 12
	qs := make([]question, 0, h.QDCount)
	for i := 0; i < int(h.QDCount); i++ {
		name, next, err := decodeName(pkt, offset)
		if err != nil {
			return header{}, nil, fmt.Errorf("mdns: decode question %d name: %w", i, err)
		}
		offset = next
		if offset+4 > len(pkt) {
			return header{}, nil, fmt.Errorf("mdns: truncated type/class for question %d", i)
		}
		qType := binary.BigEndian.Uint16(pkt[offset : offset+2])
		qClass := binary.BigEndian.Uint16(pkt[offset+2 : offset+4])
		offset += 4
		qs = append(qs, question{
			Name:  name,
			Type:  qType,
			Class: qClass &^ classUnicastResponseBit,
		})
	}

	return h, qs, nil
}
