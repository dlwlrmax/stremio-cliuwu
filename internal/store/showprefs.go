package store

import (
	"stremio-cliuwu/internal/jsonio"
	"sync"
)

// showPrefs is one per-show preference map backed by a JSON file: show id to
// whatever is remembered for it. The subtitle and audio preferences are each
// one of these.
//
// It is loaded once at startup and kept in memory; every set/delete rewrites
// the file. The mutex is needed because the player's read loop and the TUI
// both reach for it.
type showPrefs[T comparable] struct {
	file func() string
	mu   sync.RWMutex
	m    map[string]T
	// write orders saves. A save snapshots the map and then writes it, and
	// two of those interleaving could leave the older snapshot on disk. Held
	// for the whole update, but not by readers, who only take mu.
	write sync.Mutex
}

func newShowPrefs[T comparable](file func() string) *showPrefs[T] {
	return &showPrefs[T]{file: file, m: map[string]T{}}
}

// load reads the file into memory. A missing or corrupt file is an empty map,
// never an error: a bad preference shouldn't stop playback.
func (s *showPrefs[T]) load() {
	m := map[string]T{}
	if err := jsonio.Read(s.file(), &m); err != nil || m == nil {
		m = map[string]T{}
	}
	s.mu.Lock()
	s.m = m
	s.mu.Unlock()
}

func (s *showPrefs[T]) get(showID string) (T, bool) {
	var zero T
	if showID == "" {
		return zero, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.m[showID]
	return p, ok
}

// set remembers a show's choice, overwriting any previous one.
func (s *showPrefs[T]) set(showID string, pref T) {
	if showID == "" {
		return
	}
	s.write.Lock()
	defer s.write.Unlock()

	s.mu.Lock()
	if cur, ok := s.m[showID]; ok && cur == pref {
		s.mu.Unlock()
		return
	}
	s.m[showID] = pref
	snapshot := s.clone()
	s.mu.Unlock()

	jsonio.Write(s.file(), snapshot)
}

// del forgets a show, sending it back to the global default.
func (s *showPrefs[T]) del(showID string) {
	if showID == "" {
		return
	}
	s.write.Lock()
	defer s.write.Unlock()

	s.mu.Lock()
	if _, ok := s.m[showID]; !ok {
		s.mu.Unlock()
		return
	}
	delete(s.m, showID)
	snapshot := s.clone()
	s.mu.Unlock()

	jsonio.Write(s.file(), snapshot)
}

// clone copies the map so the file can be written without holding the lock
// across the disk write. Called with mu held.
func (s *showPrefs[T]) clone() map[string]T {
	out := make(map[string]T, len(s.m))
	for k, v := range s.m {
		out[k] = v
	}
	return out
}
