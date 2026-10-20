// Command kvload drives a kvserver with concurrent random operations:
//
//	kvload -addr 127.0.0.1:8080 -clients 8 -ops 200 -keys a,b
//
// Each client mixes puts, compare-and-swaps (with the version it last saw,
// often stale), deletes and reads on a few keys, so concurrent CAS races
// on the same key are common.
package main

import (
	"context"
	"flag"
	"log"
	"strings"

	"github.com/casselc/quint-connect-go/examples/kvstore/kv"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "server address")
	clients := flag.Int("clients", 8, "concurrent clients")
	ops := flag.Int("ops", 200, "operations per client")
	keys := flag.String("keys", "a,b", "keys to use")
	seed := flag.Uint64("seed", 1, "random seed")
	flag.Parse()
	if err := kv.Load(context.Background(), "http://"+*addr, *clients, *ops, strings.Split(*keys, ","), *seed); err != nil {
		log.Fatal(err)
	}
}
