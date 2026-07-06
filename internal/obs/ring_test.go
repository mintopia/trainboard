package obs

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func evt(i int) Event {
	return Event{Time: time.Unix(int64(i), 0), Msg: fmt.Sprintf("e%d", i)}
}

func TestRingKeepsInsertionOrder(t *testing.T) {
	r := NewRing(4)
	for i := 0; i < 3; i++ {
		r.Add(evt(i))
	}
	got := r.Events()
	if len(got) != 3 || r.Len() != 3 {
		t.Fatalf("len = %d/%d, want 3", len(got), r.Len())
	}
	for i, e := range got {
		if e.Msg != fmt.Sprintf("e%d", i) {
			t.Fatalf("got[%d].Msg = %q", i, e.Msg)
		}
	}
}

func TestRingEvictsOldest(t *testing.T) {
	r := NewRing(4)
	for i := 0; i < 10; i++ {
		r.Add(evt(i))
	}
	got := r.Events()
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	for i, e := range got {
		if want := fmt.Sprintf("e%d", i+6); e.Msg != want {
			t.Fatalf("got[%d].Msg = %q, want %q", i, e.Msg, want)
		}
	}
}

func TestRingEventsReturnsCopy(t *testing.T) {
	r := NewRing(2)
	r.Add(evt(1))
	got := r.Events()
	got[0].Msg = "mutated"
	if r.Events()[0].Msg != "e1" {
		t.Fatal("Events() must return a copy")
	}
}

func TestRingConcurrentAddAndRead(t *testing.T) {
	r := NewRing(DefaultRingCapacity)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				r.Add(evt(w*1000 + i))
				_ = r.Events()
			}
		}(w)
	}
	wg.Wait()
	if r.Len() != DefaultRingCapacity {
		t.Fatalf("Len = %d, want %d", r.Len(), DefaultRingCapacity)
	}
}

func TestRingZeroCapacityPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewRing(0) must panic")
		}
	}()
	NewRing(0)
}
