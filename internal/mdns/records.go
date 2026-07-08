package mdns

import (
	"net"
	"strings"
)

// TTLs per the design spec: short-lived address records, longer-lived
// service records (RFC 6762 §10 recommends longer TTLs for records that
// change less often).
const (
	ttlAddress = 120
	ttlService = 4500
)

// Answer-record classes: classFlush marks a record as authoritative and
// replaces any cached RRset of the same name/type (RFC 6762 §10.2, the
// cache-flush bit); classPlain is used for the two PTR records, per RFC
// 6762 §10.2's guidance that shared records (PTRs can have multiple owners)
// should not set the flush bit.
const (
	classPlain uint16 = 0x0001
	classFlush uint16 = 0x8001
)

// serviceName and enumName are fixed per RFC 6763: the board only ever
// advertises one service type, so these never vary with the suffix.
const (
	serviceName = "_http._tcp.local"
	enumName    = "_services._dns-sd._udp.local"
)

// Zone is the immutable set of names/records this responder serves for ONE
// interface snapshot (addresses baked in at construction).
type Zone struct {
	hostName     string // "trainboard-<suffix-lowercased>.local"
	aliasName    string // "trainboard.local"
	instanceName string // "Trainboard <SUFFIX>._http._tcp.local"
	v4           net.IP
	v6           net.IP
}

// NewZone builds the zone. suffix = macTail output ("AB12"); host names are
// lowercased: "trainboard-ab12.local" + "trainboard.local". v4/v6 = the
// interface's usable unicast addresses (either may be nil).
func NewZone(suffix string, v4, v6 net.IP) *Zone {
	lower := strings.ToLower(suffix)
	return &Zone{
		hostName:     "trainboard-" + lower + ".local",
		aliasName:    "trainboard.local",
		instanceName: "Trainboard " + suffix + "." + serviceName,
		v4:           v4,
		v6:           v6,
	}
}

// Answers returns authoritative answer records for a question, nil if the
// question isn't ours. Matching is case-insensitive per RFC 6762 §16.
// Handles: A/AAAA for both hostnames; PTR for _http._tcp.local; SRV/TXT for
// the service instance; PTR for _services._dns-sd._udp.local (service-type
// enumeration → _http._tcp.local).
//
//nolint:revive // record is intentionally unexported (package-private wire
// type); this method's only consumer is the Task 3 responder loop in the
// same package, so exporting record itself would widen the surface for no
// benefit.
func (z *Zone) Answers(q question) []record {
	switch q.Type {
	case TypeA:
		if strings.EqualFold(q.Name, z.hostName) {
			return z.aRecord(z.hostName)
		}
		if strings.EqualFold(q.Name, z.aliasName) {
			return z.aRecord(z.aliasName)
		}
	case TypeAAAA:
		if strings.EqualFold(q.Name, z.hostName) {
			return z.aaaaRecord(z.hostName)
		}
		if strings.EqualFold(q.Name, z.aliasName) {
			return z.aaaaRecord(z.aliasName)
		}
	case TypePTR:
		if strings.EqualFold(q.Name, serviceName) {
			return []record{z.servicePTR()}
		}
		if strings.EqualFold(q.Name, enumName) {
			return []record{z.enumPTR()}
		}
	case TypeSRV:
		if strings.EqualFold(q.Name, z.instanceName) {
			return []record{z.srv()}
		}
	case TypeTXT:
		if strings.EqualFold(q.Name, z.instanceName) {
			return []record{z.txt()}
		}
	}
	return nil
}

// Announcement returns the full unsolicited announce set (all records,
// cache-flush class on A/AAAA/SRV/TXT, plain class on the two PTRs).
//
//nolint:revive // see Answers: record is intentionally package-private.
func (z *Zone) Announcement() []record {
	var out []record
	out = append(out, z.aRecord(z.hostName)...)
	out = append(out, z.aaaaRecord(z.hostName)...)
	out = append(out, z.aRecord(z.aliasName)...)
	out = append(out, z.aaaaRecord(z.aliasName)...)
	out = append(out, z.servicePTR())
	out = append(out, z.srv())
	out = append(out, z.txt())
	out = append(out, z.enumPTR())
	return out
}

// Goodbye returns Announcement() with every TTL forced to 0.
//
//nolint:revive // see Answers: record is intentionally package-private.
func (z *Zone) Goodbye() []record {
	ann := z.Announcement()
	out := make([]record, len(ann))
	for i, r := range ann {
		r.TTL = 0
		out[i] = r
	}
	return out
}

// aRecord returns a single-element A answer for name, or nil if this zone
// has no IPv4 address (a v6-only interface).
func (z *Zone) aRecord(name string) []record {
	if z.v4 == nil {
		return nil
	}
	return []record{{
		Name:  name,
		Type:  TypeA,
		Class: classFlush,
		TTL:   ttlAddress,
		Data:  rdataA{IP: z.v4},
	}}
}

// aaaaRecord returns a single-element AAAA answer for name, or nil if this
// zone has no IPv6 address (a v4-only interface).
func (z *Zone) aaaaRecord(name string) []record {
	if z.v6 == nil {
		return nil
	}
	return []record{{
		Name:  name,
		Type:  TypeAAAA,
		Class: classFlush,
		TTL:   ttlAddress,
		Data:  rdataAAAA{IP: z.v6},
	}}
}

// servicePTR is the "who provides _http._tcp.local" answer, pointing at
// this board's service instance.
func (z *Zone) servicePTR() record {
	return record{
		Name:  serviceName,
		Type:  TypePTR,
		Class: classPlain,
		TTL:   ttlService,
		Data:  rdataPTR{Target: z.instanceName},
	}
}

// enumPTR is the service-type-enumeration answer (RFC 6763 §9): a query for
// _services._dns-sd._udp.local always names _http._tcp.local as one of the
// service types this board advertises.
func (z *Zone) enumPTR() record {
	return record{
		Name:  enumName,
		Type:  TypePTR,
		Class: classPlain,
		TTL:   ttlService,
		Data:  rdataPTR{Target: serviceName},
	}
}

// srv is this board's SRV record for its _http._tcp service instance.
func (z *Zone) srv() record {
	return record{
		Name:  z.instanceName,
		Type:  TypeSRV,
		Class: classFlush,
		TTL:   ttlService,
		Data:  rdataSRV{Priority: 0, Weight: 0, Port: 80, Target: z.hostName},
	}
}

// txt is this board's TXT record for its _http._tcp service instance.
func (z *Zone) txt() record {
	return record{
		Name:  z.instanceName,
		Type:  TypeTXT,
		Class: classFlush,
		TTL:   ttlService,
		Data:  rdataTXT{Pairs: []string{"path=/"}},
	}
}
