package main

import "sync"

// Per-show binge group: the release you last played for a series, so the
// next episode's stream list can lead with the same one. Same shape as the
// subtitle preferences — loaded once, rewritten on every change.

var bingePrefs = struct {
	sync.RWMutex
	m map[string]string
}{m: map[string]string{}}

// LoadBingePrefs reads binge.json. Missing or corrupt is just empty: losing
// it costs one episode's worth of picking by hand.
func LoadBingePrefs() {
	m := map[string]string{}
	if err := readJSON(bingeFile(), &m); err != nil || m == nil {
		m = map[string]string{}
	}
	bingePrefs.Lock()
	bingePrefs.m = m
	bingePrefs.Unlock()
}

func GetBinge(showID string) string {
	bingePrefs.RLock()
	defer bingePrefs.RUnlock()
	return bingePrefs.m[showID]
}

// SetBinge remembers a show's release. A stream with no group says nothing
// about the release, so it leaves the old choice alone.
func SetBinge(showID, group string) {
	if showID == "" || group == "" {
		return
	}
	bingePrefs.Lock()
	if bingePrefs.m[showID] == group {
		bingePrefs.Unlock()
		return
	}
	bingePrefs.m[showID] = group
	snapshot := make(map[string]string, len(bingePrefs.m))
	for k, v := range bingePrefs.m {
		snapshot[k] = v
	}
	bingePrefs.Unlock()

	writeJSON(bingeFile(), snapshot)
}
