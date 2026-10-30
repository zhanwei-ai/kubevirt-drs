package controller

import (
	"testing"
	"time"

	"github.com/zhanwei-ai/kubevirt-drs/internal/planner"
)

func TestBlacklistEntryRoundTrip(t *testing.T) {
	until := time.Date(2026, 10, 24, 15, 16, 20, 0, time.UTC)
	e := blacklistEntry(planner.Pair{Source: "node-a", Target: "no-such-node"}, until)
	if e != "node-a->no-such-node until 2026-10-24T15:16:20Z" {
		t.Fatalf("format changed: %q", e)
	}
	p, got, ok := parseBlacklistEntry(e)
	if !ok || p.Source != "node-a" || p.Target != "no-such-node" || !got.Equal(until) {
		t.Fatalf("parse: %+v %v %v", p, got, ok)
	}
	for _, bad := range []string{"", "garbage", "a->b until yesterday", "ab until 2026-10-24T15:16:20Z"} {
		if _, _, ok := parseBlacklistEntry(bad); ok {
			t.Fatalf("%q must not parse", bad)
		}
	}
}

func TestRestoreBlacklistSkipsExpired(t *testing.T) {
	now := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	c := &Cycle{}
	n := c.restoreBlacklist([]string{
		blacklistEntry(planner.Pair{Source: "a", Target: "b"}, now.Add(30*time.Minute)),
		blacklistEntry(planner.Pair{Source: "c", Target: "d"}, now.Add(-time.Minute)),
		"not an entry",
	}, now)
	if n != 1 {
		t.Fatalf("want 1 live pair restored, got %d", n)
	}
	if _, ok := c.blacklistCopy(now)[planner.Pair{Source: "a", Target: "b"}]; !ok {
		t.Fatal("a->b should be blacklisted")
	}
}

func TestCostBandwidth(t *testing.T) {
	if got := costBandwidth(64<<20, 0); got != 64<<20 {
		t.Fatalf("cap only: %v", got)
	}
	if got := costBandwidth(64<<20, 40<<20); got != 40<<20 {
		t.Fatalf("observed slower than cap wins: %v", got)
	}
	if got := costBandwidth(64<<20, 200<<20); got != 64<<20 {
		t.Fatalf("observed faster than cap: cap still bounds: %v", got)
	}
	if got := costBandwidth(0, 0); got != 0 {
		t.Fatalf("unknown stays 0 (planner default applies): %v", got)
	}
}
