package weaveapp

import "sync"

type st struct{ mu sync.RWMutex }

type guarded struct {
	sync.Mutex                           // want `embedded sync.Mutex: calls promoted through the struct are not woven`
	inner      struct{ *sync.WaitGroup } // want `embedded sync.WaitGroup`
}

func (s *st) acquire() func() {
	s.mu.RLock()
	go func() {}() // want `go statement not marked //qenv:go`
	//qenv:go
	go func() {}()
	go func() {}() //qenv:ok
	defer func() { s.mu.RUnlock() }()
	return s.mu.RUnlock // want `method value RUnlock is not woven`
}
