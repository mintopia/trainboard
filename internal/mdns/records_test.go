package mdns

import (
	"net"
	"reflect"
	"testing"
)

// testZone builds a Zone with both v4 and v6 addresses present, suffix
// "AB12" (matches the brief's worked examples).
func testZone() *Zone {
	return NewZone("AB12", net.ParseIP("10.55.0.1"), net.ParseIP("fd00::1"))
}

func TestZoneAnswers(t *testing.T) {
	z := testZone()

	tests := []struct {
		name string
		q    question
		want []record
	}{
		{
			name: "A for canonical host",
			q:    question{Name: "trainboard-ab12.local", Type: TypeA, Class: 0x0001},
			want: []record{{
				Name: "trainboard-ab12.local", Type: TypeA, Class: 0x8001, TTL: 120,
				Data: rdataA{IP: net.ParseIP("10.55.0.1")},
			}},
		},
		{
			name: "A for alias",
			q:    question{Name: "trainboard.local", Type: TypeA, Class: 0x0001},
			want: []record{{
				Name: "trainboard.local", Type: TypeA, Class: 0x8001, TTL: 120,
				Data: rdataA{IP: net.ParseIP("10.55.0.1")},
			}},
		},
		{
			name: "A for alias is case-insensitive",
			q:    question{Name: "TRAINBOARD.LOCAL", Type: TypeA, Class: 0x0001},
			want: []record{{
				Name: "trainboard.local", Type: TypeA, Class: 0x8001, TTL: 120,
				Data: rdataA{IP: net.ParseIP("10.55.0.1")},
			}},
		},
		{
			name: "AAAA for canonical host",
			q:    question{Name: "trainboard-ab12.local", Type: TypeAAAA, Class: 0x0001},
			want: []record{{
				Name: "trainboard-ab12.local", Type: TypeAAAA, Class: 0x8001, TTL: 120,
				Data: rdataAAAA{IP: net.ParseIP("fd00::1")},
			}},
		},
		{
			name: "AAAA for alias",
			q:    question{Name: "trainboard.local", Type: TypeAAAA, Class: 0x0001},
			want: []record{{
				Name: "trainboard.local", Type: TypeAAAA, Class: 0x8001, TTL: 120,
				Data: rdataAAAA{IP: net.ParseIP("fd00::1")},
			}},
		},
		{
			name: "PTR for _http._tcp.local",
			q:    question{Name: "_http._tcp.local", Type: TypePTR, Class: 0x0001},
			want: []record{{
				Name: "_http._tcp.local", Type: TypePTR, Class: 0x0001, TTL: 4500,
				Data: rdataPTR{Target: "Trainboard AB12._http._tcp.local"},
			}},
		},
		{
			name: "PTR name match is case-insensitive",
			q:    question{Name: "_HTTP._TCP.LOCAL", Type: TypePTR, Class: 0x0001},
			want: []record{{
				Name: "_http._tcp.local", Type: TypePTR, Class: 0x0001, TTL: 4500,
				Data: rdataPTR{Target: "Trainboard AB12._http._tcp.local"},
			}},
		},
		{
			name: "SRV for service instance",
			q:    question{Name: "Trainboard AB12._http._tcp.local", Type: TypeSRV, Class: 0x0001},
			want: []record{{
				Name: "Trainboard AB12._http._tcp.local", Type: TypeSRV, Class: 0x8001, TTL: 4500,
				Data: rdataSRV{Priority: 0, Weight: 0, Port: 80, Target: "trainboard-ab12.local"},
			}},
		},
		{
			name: "TXT for service instance",
			q:    question{Name: "Trainboard AB12._http._tcp.local", Type: TypeTXT, Class: 0x0001},
			want: []record{{
				Name: "Trainboard AB12._http._tcp.local", Type: TypeTXT, Class: 0x8001, TTL: 4500,
				Data: rdataTXT{Pairs: []string{"path=/"}},
			}},
		},
		{
			name: "service-type enumeration PTR",
			q:    question{Name: "_services._dns-sd._udp.local", Type: TypePTR, Class: 0x0001},
			want: []record{{
				Name: "_services._dns-sd._udp.local", Type: TypePTR, Class: 0x0001, TTL: 4500,
				Data: rdataPTR{Target: "_http._tcp.local"},
			}},
		},
		{
			name: "unknown name returns nil",
			q:    question{Name: "somethingelse.local", Type: TypeA, Class: 0x0001},
			want: nil,
		},
		{
			name: "known name, wrong type returns nil",
			q:    question{Name: "trainboard-ab12.local", Type: TypeSRV, Class: 0x0001},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := z.Answers(tt.q)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Answers(%+v) = %+v, want %+v", tt.q, got, tt.want)
			}
		})
	}
}

// TestZoneAnswersNilV6 confirms a missing IPv6 address produces an empty
// AAAA answer set rather than a nil-pointer panic.
func TestZoneAnswersNilV6(t *testing.T) {
	z := NewZone("AB12", net.ParseIP("10.55.0.1"), nil)

	got := z.Answers(question{Name: "trainboard-ab12.local", Type: TypeAAAA, Class: 0x0001})
	if got != nil {
		t.Fatalf("Answers(AAAA) with nil v6 = %+v, want nil", got)
	}

	gotAlias := z.Answers(question{Name: "trainboard.local", Type: TypeAAAA, Class: 0x0001})
	if gotAlias != nil {
		t.Fatalf("Answers(AAAA) for alias with nil v6 = %+v, want nil", gotAlias)
	}
}

// TestZoneAnswersNilV4 mirrors the nil-v6 case for a v4-less zone (e.g. a
// v6-only interface), guarding the same nil-pointer class of bug.
func TestZoneAnswersNilV4(t *testing.T) {
	z := NewZone("AB12", nil, net.ParseIP("fd00::1"))

	got := z.Answers(question{Name: "trainboard-ab12.local", Type: TypeA, Class: 0x0001})
	if got != nil {
		t.Fatalf("Answers(A) with nil v4 = %+v, want nil", got)
	}
}

func TestZoneAnnouncement(t *testing.T) {
	z := testZone()
	ann := z.Announcement()

	// 4 address records (A+AAAA for host and alias) + SRV + TXT + 2 PTRs.
	const wantCount = 8
	if len(ann) != wantCount {
		t.Fatalf("Announcement() returned %d records, want %d: %+v", len(ann), wantCount, ann)
	}

	var cacheFlush, plain int
	for _, r := range ann {
		switch r.Class {
		case 0x8001:
			cacheFlush++
		case 0x0001:
			plain++
		default:
			t.Fatalf("record %+v has unexpected class %#x", r, r.Class)
		}
		if r.TTL == 0 {
			t.Fatalf("Announcement() record %+v has TTL 0, want nonzero", r)
		}
	}
	if cacheFlush != 6 {
		t.Fatalf("Announcement() cache-flush record count = %d, want 6 (A host, AAAA host, A alias, AAAA alias, SRV, TXT)", cacheFlush)
	}
	if plain != 2 {
		t.Fatalf("Announcement() plain-class record count = %d, want 2 (the two PTRs)", plain)
	}
}

func TestZoneGoodbye(t *testing.T) {
	z := testZone()
	ann := z.Announcement()
	gb := z.Goodbye()

	if len(gb) != len(ann) {
		t.Fatalf("Goodbye() returned %d records, want %d (same as Announcement())", len(gb), len(ann))
	}
	for i, r := range gb {
		if r.TTL != 0 {
			t.Fatalf("Goodbye() record %d = %+v, want TTL 0", i, r)
		}
		// Everything except TTL must match Announcement()'s record.
		want := ann[i]
		want.TTL = 0
		if !reflect.DeepEqual(r, want) {
			t.Fatalf("Goodbye() record %d = %+v, want %+v", i, r, want)
		}
	}
}
