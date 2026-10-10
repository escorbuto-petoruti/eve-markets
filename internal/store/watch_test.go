package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func seedToken(t *testing.T, s *Store, char int64) {
	t.Helper()
	ctx := context.Background()
	if err := s.UpsertUser(ctx, char, "U", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveToken(ctx, Token{CharacterID: char, UserID: char, CharacterName: "C", RefreshToken: "r", Scopes: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
}

func TestWatchAddListRemove(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedToken(t, s, 1)
	at := time.Unix(1000, 0)
	if err := s.AddWatch(ctx, 1, 500, "Keepstar", at); err != nil {
		t.Fatal(err)
	}
	// Adding again keeps one row and refreshes the name.
	if err := s.AddWatch(ctx, 1, 500, "Keepstar II", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListWatched(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListWatched = %+v, %v", got, err)
	}
	w := got[0]
	if w.CharacterID != 1 || w.StructureID != 500 || w.Name != "Keepstar II" || !w.CreatedAt.Equal(at) {
		t.Errorf("watched = %+v", w)
	}
	if !w.LastRunAt.IsZero() || w.LastStatus != "" {
		t.Errorf("fresh watch has run state: %+v", w)
	}
	if err := s.RemoveWatch(ctx, 1, 500); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListWatched(ctx); len(got) != 0 {
		t.Errorf("after remove = %+v", got)
	}
}

func TestWatchUnknownCharacter(t *testing.T) {
	s := openTemp(t)
	err := s.AddWatch(context.Background(), 99, 500, "", time.Unix(1, 0))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("AddWatch unknown character = %v, want ErrNotFound", err)
	}
}

func TestWatchCascadesWithToken(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedToken(t, s, 1)
	if err := s.AddWatch(ctx, 1, 500, "", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteToken(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListWatched(ctx); len(got) != 0 {
		t.Errorf("watch survived token delete: %+v", got)
	}
}

func TestRecordRunAndListForUser(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedToken(t, s, 1)
	seedToken(t, s, 2)
	_ = s.AddWatch(ctx, 1, 500, "A", time.Unix(1, 0))
	_ = s.AddWatch(ctx, 2, 600, "B", time.Unix(1, 0))
	at := time.Unix(5000, 0)
	if err := s.RecordRun(ctx, 1, 500, at, "error", "boom"); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyOrderDiff(ctx, 500, at, []StoredOrder{{OrderID: 1, TypeID: 34, IsBuy: true, Price: 1, VolumeRemain: 5, VolumeTotal: 5, Issued: "x", Duration: 90, FirstSeen: at, LastSeen: at}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListWatchedForUser(ctx, 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListWatchedForUser = %+v, %v", got, err)
	}
	w := got[0]
	if w.StructureID != 500 || w.LastStatus != "error" || w.LastError != "boom" || !w.LastRunAt.Equal(at) || w.OrderCount != 1 {
		t.Errorf("watched = %+v", w)
	}
	if err := s.RecordRun(ctx, 1, 500, at, "ok", ""); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListWatchedForUser(ctx, 1)
	if got[0].LastError != "" || got[0].LastStatus != "ok" {
		t.Errorf("error not cleared: %+v", got[0])
	}
}

func TestApplyOrderDiffIsTransactional(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	at := time.Unix(100, 0)
	o1 := StoredOrder{OrderID: 1, TypeID: 34, IsBuy: true, Price: 5, VolumeRemain: 10, VolumeTotal: 10, Issued: "t", Duration: 90, FirstSeen: at, LastSeen: at}
	o2 := o1
	o2.OrderID = 2
	ev := []OrderEvent{{OrderID: 1, TypeID: 34, IsBuy: true, At: at, Kind: "new", Price: 5, VolumeRemain: 10, DeltaVolume: 10}}
	if err := s.ApplyOrderDiff(ctx, 500, at, []StoredOrder{o1, o2}, nil, ev); err != nil {
		t.Fatal(err)
	}
	cur, err := s.CurrentOrders(ctx, 500)
	if err != nil || len(cur) != 2 {
		t.Fatalf("CurrentOrders = %+v, %v", cur, err)
	}
	if other, _ := s.CurrentOrders(ctx, 501); len(other) != 0 {
		t.Errorf("other structure orders = %+v", other)
	}
	// Update + delete.
	o1.VolumeRemain = 4
	later := at.Add(time.Minute)
	if err := s.ApplyOrderDiff(ctx, 500, later, []StoredOrder{o1}, []int64{2}, []OrderEvent{
		{OrderID: 1, TypeID: 34, IsBuy: true, At: later, Kind: "fill", Price: 5, VolumeRemain: 4, DeltaVolume: 6},
		{OrderID: 2, TypeID: 34, IsBuy: true, At: later, Kind: "gone", Price: 5, VolumeRemain: 10, DeltaVolume: 10},
	}); err != nil {
		t.Fatal(err)
	}
	cur, _ = s.CurrentOrders(ctx, 500)
	if len(cur) != 1 || cur[0].OrderID != 1 || cur[0].VolumeRemain != 4 || !cur[0].FirstSeen.Equal(at) {
		t.Errorf("after diff = %+v", cur)
	}
	if n := count(t, s, `SELECT count(*) FROM order_events`); n != 3 {
		t.Errorf("events = %d, want 3", n)
	}
	// A bad event (invalid kind) rolls the whole diff back.
	bad := []OrderEvent{{OrderID: 1, TypeID: 34, At: later, Kind: "bogus"}}
	if err := s.ApplyOrderDiff(ctx, 500, later, nil, []int64{1}, bad); err == nil {
		t.Fatal("expected error for invalid kind")
	}
	cur, _ = s.CurrentOrders(ctx, 500)
	if len(cur) != 1 {
		t.Errorf("rollback failed, orders = %+v", cur)
	}
}

func TestMigration3KeepsExistingData(t *testing.T) {
	path := t.TempDir() + "/m.db"
	makeDBAtVersion(t, path, 2)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n := count(t, s, `SELECT count(*) FROM marker`); n != 1 {
		t.Errorf("marker rows = %d, want 1", n)
	}
	for _, table := range []string{"watched_structures", "orders_current", "order_events"} {
		if n := count(t, s, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table); n != 1 {
			t.Errorf("table %s missing", table)
		}
	}
	if len(migrations) < 3 {
		t.Errorf("migrations = %d, want >= 3", len(migrations))
	}
}
