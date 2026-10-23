package adapter

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

func TestFullAffinityTableRetainsSuccessfulAccountMigration(t *testing.T) {
	p := NewPool("test", nil)
	p.SetMaxConcurrencyPerAccount(1)
	p.Add("busy", &fakeAd{name: "test"})
	p.Add("idle", &fakeAd{name: "test"})
	for i := 0; i < affinityMaxKeys; i++ {
		p.affinity.set(fmt.Sprintf("session-%d", i), "busy")
	}
	_, _, release, err := p.reserve(context.Background(), "m", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	req := llm.RequestMessages{Model: "m", SessionKey: "session-0"}
	migrated, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStream(migrated)
	if AccountOf(migrated) != "idle" {
		t.Fatalf("full account should migrate to idle capacity: %s", AccountOf(migrated))
	}
	closeStream(migrated)
	release()
	next, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStream(next)
	if AccountOf(next) != "idle" {
		t.Fatalf("next turn returned to the stale account binding: %s", AccountOf(next))
	}
	if len(p.affinity.m) != affinityMaxKeys {
		t.Fatal("updating a session changed the table capacity")
	}
}

func BenchmarkPoolWeightedSelection(b *testing.B) {
	for _, count := range []int{8, 64, 512} {
		b.Run(fmt.Sprintf("accounts-%d", count), func(b *testing.B) {
			p := NewPool("benchmark", nil)
			p.SetMaxConcurrencyPerAccount(2)
			for i := 0; i < count; i++ {
				p.Add(fmt.Sprint(i), nil)
				acc := p.accounts[i]
				acc.observe(i%3 != 0, float64(i+1)*100)
				if i%2 == 0 {
					acc.inFlight = 2
				}
			}
			now := time.Now()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p.mu.Lock()
				_, _ = p.pickByHealthLocked(now, "m", nil)
				p.mu.Unlock()
			}
		})
	}
}

func BenchmarkFullAffinityUpdate(b *testing.B) {
	table := newAffinityTable()
	for i := 0; i < affinityMaxKeys; i++ {
		table.set(fmt.Sprintf("session-%d", i), "account")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		table.set("session-0", "account")
	}
}
