package sessionpool

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

func poolFactory() (Factory, *atomic.Int64) {
	var creations atomic.Int64
	return func(ctx context.Context, account string) (*session.Session, error) {
		creations.Add(1)
		tr := transport.NewMockTransport()
		return session.New(account, "gw", "pw", tr), nil
	}, &creations
}

func TestPoolSingleFlightCreation(t *testing.T) {
	factory, creations := poolFactory()
	p := New(Options{TTL: time.Minute, CleanupInterval: 0, Shards: 8}, factory)
	defer p.Close()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := p.Get(context.Background(), "20000002")
			if err != nil {
				t.Error(err)
				return
			}
			if s.UserID() != "20000002" {
				t.Errorf("user = %q", s.UserID())
			}
		}()
	}
	wg.Wait()
	if got := creations.Load(); got != 1 {
		t.Fatalf("creations = %d, want 1", got)
	}
	if p.Len() != 1 {
		t.Fatalf("len = %d", p.Len())
	}
}

func TestPoolDistinctAccounts(t *testing.T) {
	factory, creations := poolFactory()
	p := New(Options{TTL: time.Minute, CleanupInterval: 0, Shards: 8}, factory)
	defer p.Close()

	for i := 0; i < 100; i++ {
		if _, err := p.Get(context.Background(), "account-"+string(rune('A'+i%26))+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	if p.Len() != 100 {
		t.Fatalf("len = %d", p.Len())
	}
	if got := creations.Load(); got != 100 {
		t.Fatalf("creations = %d", got)
	}
}

func TestPoolTTLCleanup(t *testing.T) {
	factory, _ := poolFactory()
	p := New(Options{TTL: 10 * time.Millisecond, CleanupInterval: 0, Shards: 4}, factory)
	defer p.Close()

	if _, err := p.Get(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	evicted := p.Cleanup(time.Now())
	if evicted != 1 || p.Len() != 0 {
		t.Fatalf("evicted = %d len = %d", evicted, p.Len())
	}
}

func TestPoolRemoveAndPeek(t *testing.T) {
	factory, _ := poolFactory()
	p := New(Options{TTL: time.Minute, CleanupInterval: 0}, factory)
	defer p.Close()

	if _, err := p.Get(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Peek("a"); !ok {
		t.Fatal("peek must find existing")
	}
	p.Remove("a")
	if _, ok := p.Peek("a"); ok {
		t.Fatal("peek must miss after remove")
	}
}
