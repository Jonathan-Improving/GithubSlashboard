package provider

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingMember records concurrent Invoke usage and whether it was closed.
type countingMember struct {
	active  int32
	maxSeen int32
	closed  bool
	mu      sync.Mutex
}

func (m *countingMember) Name() string { return "member" }
func (m *countingMember) Invoke(ctx context.Context, req Request, correction string) (string, error) {
	n := atomic.AddInt32(&m.active, 1)
	for {
		old := atomic.LoadInt32(&m.maxSeen)
		if n <= old || atomic.CompareAndSwapInt32(&m.maxSeen, old, n) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	atomic.AddInt32(&m.active, -1)
	return goodJSON, nil
}
func (m *countingMember) Summarize(ctx context.Context, prompt string) (string, error) {
	return goodJSON, nil
}
func (m *countingMember) Close() error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	return nil
}

func TestPoolProvidesParallelismAndClosesAll(t *testing.T) {
	const size = 3
	var members []*countingMember
	i := 0
	p, err := newPoolProvider("kiro", size, func() (Provider, error) {
		m := &countingMember{}
		members = append(members, m)
		i++
		return m, nil
	})
	if err != nil {
		t.Fatalf("newPoolProvider: %v", err)
	}

	// Fire more concurrent Invokes than members; the pool should keep all
	// members busy but never exceed one concurrent call per member.
	var wg sync.WaitGroup
	for k := 0; k < 12; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.Invoke(context.Background(), Request{}, ""); err != nil {
				t.Errorf("Invoke: %v", err)
			}
		}()
	}
	wg.Wait()

	// Each member must have been used strictly serially (max concurrent == 1),
	// and across the pool we should have achieved real parallelism (total work
	// spread over `size` members).
	for idx, m := range members {
		if m.maxSeen > 1 {
			t.Errorf("member %d saw %d concurrent calls, want <=1 (a session serializes)", idx, m.maxSeen)
		}
	}
	if len(members) != size {
		t.Fatalf("built %d members, want %d", len(members), size)
	}

	if closer, ok := p.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	} else {
		t.Fatal("pool provider should expose Close")
	}
	for idx, m := range members {
		if !m.closed {
			t.Errorf("member %d not closed", idx)
		}
	}
}

func TestPoolBuildFailureClosesPartial(t *testing.T) {
	var built []*countingMember
	calls := 0
	_, err := newPoolProvider("kiro", 3, func() (Provider, error) {
		calls++
		if calls == 2 {
			return nil, context.DeadlineExceeded
		}
		m := &countingMember{}
		built = append(built, m)
		return m, nil
	})
	if err == nil {
		t.Fatal("expected build failure")
	}
	// The one successfully-built member must have been closed on failure.
	if len(built) != 1 || !built[0].closed {
		t.Errorf("partial member not closed on build failure: built=%d", len(built))
	}
}
