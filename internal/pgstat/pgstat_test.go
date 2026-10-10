package pgstat

import (
	"testing"
	"time"
)

func TestIsHotQuery(t *testing.T) {
	cases := map[string]bool{
		"SELECT * FROM users":                  true,
		"  insert into t values (1)":           true,
		"UPDATE t SET x = 1":                   true,
		"DELETE FROM t":                        true,
		"WITH x AS (SELECT 1) SELECT * FROM x": true,
		"ALTER TABLE users ADD COLUMN y int":   false,
		"":                                     false,
	}
	for q, want := range cases {
		if got := IsHotQuery(q); got != want {
			t.Errorf("IsHotQuery(%q) = %v, want %v", q, got, want)
		}
	}
}

func TestCompactQuery(t *testing.T) {
	long := "SELECT    a,   b\nFROM   t WHERE x = 1"
	got := CompactQuery(long, 100)
	want := "SELECT a, b FROM t WHERE x = 1"
	if got != want {
		t.Fatalf("CompactQuery = %q, want %q", got, want)
	}
	if len(CompactQuery(long, 10)) != 13 {
		t.Fatalf("expected truncation with ellipsis")
	}
}

func TestDownstreamTransitive(t *testing.T) {
	now := time.Now()
	snap := Snapshot{
		At: now,
		Backends: map[int32]Backend{
			100: {PID: 100, State: "idle in transaction"},
			200: {PID: 200, Blockers: []int32{100}},
			300: {PID: 300, Blockers: []int32{200}},
			400: {PID: 400, Blockers: []int32{200}},
			500: {PID: 500, Blockers: []int32{100}},
		},
	}
	got := snap.Downstream(100)
	if len(got) != 4 {
		t.Fatalf("downstream = %v, want 4 members", got)
	}
	if got[0] != 200 || got[1] != 300 || got[2] != 400 || got[3] != 500 {
		t.Fatalf("downstream order/content = %v", got)
	}
}

func TestWaitersPerBlocker(t *testing.T) {
	snap := Snapshot{
		Backends: map[int32]Backend{
			100: {PID: 100},
			200: {PID: 200, Blockers: []int32{100}},
			300: {PID: 300, Blockers: []int32{100}},
		},
	}
	idx := snap.WaitersPerBlocker()
	if idx[100] != 2 {
		t.Fatalf("waiter index for 100 = %d, want 2", idx[100])
	}
}

func TestOldestWaitSeconds(t *testing.T) {
	now := time.Now()
	old := now.Add(-12 * time.Second)
	recent := now.Add(-3 * time.Second)
	snap := Snapshot{
		Backends: map[int32]Backend{
			200: {PID: 200, QueryStart: &old, Blockers: []int32{100}},
			300: {PID: 300, QueryStart: &recent, Blockers: []int32{100}},
		},
	}
	if got := snap.OldestWaitSeconds(now); got < 11.9 || got > 12.1 {
		t.Fatalf("oldest wait = %v, want about 12", got)
	}
}
