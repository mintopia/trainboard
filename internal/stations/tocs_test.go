package stations

import "testing"

func TestTOCName(t *testing.T) {
	name, ok := TOCName("gw")
	if !ok || name != "Great Western Railway" {
		t.Fatalf("TOCName(gw) = %q,%v", name, ok)
	}
	if _, ok := TOCName("ZZ"); ok {
		t.Fatalf("TOCName(ZZ) unexpectedly found")
	}
}

func TestTOCSearchByName(t *testing.T) {
	got := TOCSearch("eliza", 8)
	if len(got) != 1 || got[0].Code != "XR" {
		t.Fatalf("TOCSearch(eliza) = %+v, want XR", got)
	}
}

func TestTOCSearchByCode(t *testing.T) {
	got := TOCSearch("XC", 8)
	if len(got) == 0 || got[0].Code != "XC" {
		t.Fatalf("TOCSearch(XC) = %+v, want XC first", got)
	}
}

func TestTOCSearchEmptyReturnsAll(t *testing.T) {
	got := TOCSearch("", 100)
	if len(got) < 30 {
		t.Fatalf("TOCSearch(\"\") = %d rows, want the full table", len(got))
	}
}
