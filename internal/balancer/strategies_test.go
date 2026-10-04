package balancer

import "testing"

func backends() []Backend {
	return []Backend{{Name: "a", Addr: "a:80"}, {Name: "b", Addr: "b:80"}, {Name: "c", Addr: "c:80"}}
}

func TestRoundRobin_Cycles(t *testing.T) {
	rr := NewRoundRobin()
	var seq []string
	for i := 0; i < 6; i++ {
		seq = append(seq, rr.Pick(backends()).Name)
	}
	want := []string{"a", "b", "c", "a", "b", "c"}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("seq %v", seq)
		}
	}
}

func TestLeastConnections_PicksLowest(t *testing.T) {
	lc := NewLeastConnections()
	b := backends()
	ca := lc.Track("a:80")
	cb := lc.Track("b:80")
	cc := lc.Track("c:80")
	ca.Add(3)
	cb.Add(1) // b has the fewest
	cc.Add(2)
	for i := 0; i < 5; i++ {
		if got := lc.Pick(b).Name; got != "b" {
			t.Fatalf("pick %d: %s", i, got)
		}
	}
	cb.Add(5)
	cc.Add(5) // a=3, b=6, c=7 -> a is lowest
	if got := lc.Pick(b).Name; got != "a" {
		t.Fatalf("after shift: %s", got)
	}
}

func TestWeighted_SmoothDistribution(t *testing.T) {
	w := NewWeighted([]WeightedBackend{
		{Backend{Name: "a", Addr: "a"}, 3},
		{Backend{Name: "b", Addr: "b"}, 1},
	})
	counts := map[string]int{}
	for i := 0; i < 40; i++ {
		counts[w.Pick(nil).Name]++
	}
	// 3:1 over 40 picks = 30/10 with generous tolerance
	if counts["a"] < 24 || counts["a"] > 36 {
		t.Fatalf("a=%d (want ~30)", counts["a"])
	}
	if counts["b"] < 4 || counts["b"] > 16 {
		t.Fatalf("b=%d (want ~10)", counts["b"])
	}
}
