package provider

import (
	"context"
	"fmt"
	"sync"
)

// poolProvider runs several session providers concurrently and dispatches each
// Invoke to a free one. A single session serializes its own calls (one harness,
// one turn at a time), so a lone session bottlenecks classify's fan-out; a pool
// of N sessions gives up to N-way parallelism, cutting a large serial classify
// roughly N-fold at the cost of N harness processes. Each member is a fully
// independent SessionProvider (its own tmux session and verdict sink), so there
// is no shared harness state to coordinate.
type poolProvider struct {
	name    string
	members []Provider
	free    chan Provider
}

// newPoolProvider builds a pool of size n by calling build n times. On any
// build failure it closes what it already made and returns the error.
func newPoolProvider(name string, n int, build func() (Provider, error)) (Provider, error) {
	if n < 1 {
		n = 1
	}
	p := &poolProvider{name: name, free: make(chan Provider, n)}
	for i := 0; i < n; i++ {
		m, err := build()
		if err != nil {
			_ = p.Close()
			return nil, fmt.Errorf("pool member %d: %w", i, err)
		}
		p.members = append(p.members, m)
		p.free <- m
	}
	return p, nil
}

// Name identifies the provider for logging.
func (p *poolProvider) Name() string { return p.name }

// Invoke checks out a free session, runs the request on it, and returns it to
// the pool. It blocks until a session is free or ctx is done.
func (p *poolProvider) Invoke(ctx context.Context, req Request, correction string) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case m := <-p.free:
		defer func() { p.free <- m }()
		return m.Invoke(ctx, req, correction)
	}
}

// Summarize checks out a free session, runs the summary prompt on it, and
// returns it to the pool, mirroring Invoke (TDD 6.9).
func (p *poolProvider) Summarize(ctx context.Context, prompt string) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case m := <-p.free:
		defer func() { p.free <- m }()
		return m.Summarize(ctx, prompt)
	}
}

// Close tears down every member, returning the first error.
func (p *poolProvider) Close() error {
	var firstErr error
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, m := range p.members {
		if closer, ok := m.(interface{ Close() error }); ok {
			wg.Add(1)
			go func(c interface{ Close() error }) {
				defer wg.Done()
				if err := c.Close(); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
				}
			}(closer)
		}
	}
	wg.Wait()
	return firstErr
}
