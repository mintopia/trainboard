package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/data"
)

func fetchBoardView(t *testing.T, h http.Handler, cookies ...*http.Cookie) (int, boardView) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var v boardView
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatalf("bad JSON: %v: %s", err, rec.Body.String())
		}
	}
	return rec.Code, v
}

// newTestServer leaves no admin password set, so setupGate would redirect
// this request to /setup (302) before requireAuth ever ran — unlike the
// other API endpoints' unauthenticated-401 tests (e.g.
// TestAPIStatusUnauthenticated401JSON), which all go through
// newTestServerWithSources precisely so setupGate is out of the way and
// requireAuth's 401 JSON path is what's actually being exercised. Follow the
// same pattern here.
func TestAPIBoardRequiresAuth(t *testing.T) {
	srv, _ := newTestServerWithSources(t, Sources{})
	code, _ := fetchBoardView(t, srv.Handler()) // no cookie
	if code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", code)
	}
}

func TestAPIBoardDepartures(t *testing.T) {
	dep := data.Departure{
		ScheduledTime: "21:47",
		Platform:      "2",
		Status:        data.StatusOnTime,
		Operator:      "Great Western Railway",
		Length:        5,
		Destination:   data.Location{Name: "London Paddington", CRS: "PAD"},
		CallingPoints: []data.CallingPoint{
			{Location: data.Location{Name: "Southall"}, ScheduledTime: "22:05"},
		},
	}
	rest := data.Departure{
		ScheduledTime: "21:58",
		Status:        data.Status("Exp 22:04"),
		Destination:   data.Location{Name: "Reading", CRS: "RDG"},
	}
	snap := &board.Snapshot{
		State:     board.StateDepartures,
		FetchedAt: time.Now(),
		Board: &data.Board{
			LocationName: "Thatcham",
			CRS:          "THA",
			Departures:   []data.Departure{dep, rest},
		},
	}
	srv, _ := newTestServerWithSources(t, Sources{Snapshot: func() *board.Snapshot { return snap }})
	cookie, _ := loginAs(t, srv, statusTestPassword)
	code, v := fetchBoardView(t, srv.Handler(), cookie)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if v.State != "departures" || v.Location != "Thatcham" {
		t.Errorf("state/location: %+v", v)
	}
	if v.First == nil || v.First.Destination != "London Paddington" || v.First.Order != 1 ||
		v.First.Scheduled != "21:47" || v.First.Platform != "2" || v.First.Status != "On time" {
		t.Errorf("first: %+v", v.First)
	}
	if v.First.CallingAt == "" || v.First.ServiceInfo == "" {
		t.Errorf("first text lines empty: %+v", v.First)
	}
	if len(v.Remaining) != 1 || v.Remaining[0].Destination != "Reading" || v.Remaining[0].Order != 2 {
		t.Errorf("remaining: %+v", v.Remaining)
	}
}

func TestAPIBoardNilSnapshot(t *testing.T) {
	srv, _ := newTestServerWithSources(t, Sources{Snapshot: func() *board.Snapshot { return nil }})
	cookie, _ := loginAs(t, srv, statusTestPassword)
	code, v := fetchBoardView(t, srv.Handler(), cookie)
	if code != http.StatusOK || v.State != "initialising" {
		t.Errorf("nil snapshot: code %d view %+v", code, v)
	}
}

// StateDepartures with no board data must mirror board.BuildScene's fallback
// (snapshot.go:82-86): the panel draws errorScene(FaultDarwinUnreachable)
// there, so the JSON view must report state "error" too, never a bare
// "departures" with no rows.
func TestAPIBoardDeparturesStateWithNoData(t *testing.T) {
	for name, snap := range map[string]*board.Snapshot{
		"nil board":        {State: board.StateDepartures},
		"empty departures": {State: board.StateDepartures, Board: &data.Board{LocationName: "Thatcham"}},
	} {
		srv, _ := newTestServerWithSources(t, Sources{Snapshot: func() *board.Snapshot { return snap }})
		cookie, _ := loginAs(t, srv, statusTestPassword)
		code, v := fetchBoardView(t, srv.Handler(), cookie)
		if code != http.StatusOK || v.State != "error" || v.Message == "" {
			t.Errorf("%s: code %d view %+v", name, code, v)
		}
	}
}

func TestAPIBoardErrorState(t *testing.T) {
	snap := &board.Snapshot{State: board.StateError, FaultDetail: "darwin unreachable"}
	srv, _ := newTestServerWithSources(t, Sources{Snapshot: func() *board.Snapshot { return snap }})
	cookie, _ := loginAs(t, srv, statusTestPassword)
	code, v := fetchBoardView(t, srv.Handler(), cookie)
	if code != http.StatusOK || v.State != "error" || v.Message == "" {
		t.Errorf("error state: code %d view %+v", code, v)
	}
}
