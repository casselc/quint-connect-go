package kv

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
)

// Load drives the server at base with concurrent random operations:
// clients goroutines of ops operations each over keys, mixing puts,
// compare-and-swaps with the version each client last saw (often stale),
// deletes and reads. Seeded, but the interleaving is up to the scheduler.
func Load(ctx context.Context, base string, clients, ops int, keys []string, seed uint64) error {
	var wg sync.WaitGroup
	errs := make(chan error, clients)
	for c := 0; c < clients; c++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			if err := client(ctx, &Client{Base: base}, ops, keys, rand.New(rand.NewPCG(seed, uint64(c)))); err != nil {
				errs <- fmt.Errorf("client %d: %w", c, err)
			}
		}(c)
	}
	wg.Wait()
	close(errs)
	return <-errs
}

func client(ctx context.Context, cl *Client, ops int, keys []string, r *rand.Rand) error {
	seen := map[string]int64{} // the version this client last saw per key
	values := []string{"x", "y", "z"}
	for i := 0; i < ops; i++ {
		key := keys[r.IntN(len(keys))]
		var err error
		var res Result
		switch op := r.IntN(10); {
		case op < 3:
			res, err = cl.Put(ctx, key, values[r.IntN(3)])
			if err == nil {
				seen[key] = res.Revision
			}
		case op < 7:
			res, err = cl.CAS(ctx, key, seen[key], values[r.IntN(3)])
			if err == nil {
				seen[key] = res.Revision
			}
		case op < 8:
			_, err = cl.Delete(ctx, key)
		default:
			res, err = cl.Get(ctx, key)
			if err == nil {
				seen[key] = res.Entry.Version
			}
		}
		if err != nil && !IsConflict(err) && !IsNotFound(err) {
			return err
		}
	}
	return nil
}
