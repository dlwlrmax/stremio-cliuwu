package main

import "sync"

// Per-show subtitle preferences.
//
// The global SubtitleLang is the default, but a series is a long-running
// commitment and one show's subtitles often come from somewhere specific —
// an embedded track, or a plugin release the user trusts. This remembers the
// language and the source (embedded vs plugin) that was picked by hand, keyed
// by show id. Movies never get an entry; they always use the global path.

// ShowSubPref is one show's remembered choice.
type ShowSubPref struct {
	Lang string `json:"lang"`
	// Src is where the track came from: "embedded" (shipped with the stream /
	// already in the file) or "plugin" (fetched from a subtitle addon).
	Src string `json:"src"`
}

// subPrefs is loaded once at startup and kept in memory; every Set/Delete
// rewrites the file. The mutex is needed because the player's read loop and
// the TUI both reach for it.
var subPrefs = struct {
	sync.RWMutex
	m map[string]ShowSubPref
}{m: map[string]ShowSubPref{}}

// LoadSubPrefs reads sub_prefs.json into memory. A missing or corrupt file is
// an empty map, never an error: a bad preference shouldn't stop playback.
func LoadSubPrefs() {
	m := map[string]ShowSubPref{}
	if err := readJSON(subsPrefsFile(), &m); err != nil || m == nil {
		m = map[string]ShowSubPref{}
	}
	subPrefs.Lock()
	subPrefs.m = m
	subPrefs.Unlock()
}

// GetSubPref looks up a show's preference.
func GetSubPref(showID string) (ShowSubPref, bool) {
	if showID == "" {
		return ShowSubPref{}, false
	}
	subPrefs.RLock()
	defer subPrefs.RUnlock()
	p, ok := subPrefs.m[showID]
	return p, ok
}

// SetSubPref remembers a show's language and source, overwriting any previous
// choice for that show.
func SetSubPref(showID, lang, src string) {
	if showID == "" || lang == "" {
		return
	}
	subPrefs.Lock()
	subPrefs.m[showID] = ShowSubPref{Lang: lang, Src: src}
	snapshot := cloneSubPrefs(subPrefs.m)
	subPrefs.Unlock()

	writeJSON(subsPrefsFile(), snapshot)
}

// DeleteSubPref forgets a show, sending it back to the global default.
func DeleteSubPref(showID string) {
	if showID == "" {
		return
	}
	subPrefs.Lock()
	if _, ok := subPrefs.m[showID]; !ok {
		subPrefs.Unlock()
		return
	}
	delete(subPrefs.m, showID)
	snapshot := cloneSubPrefs(subPrefs.m)
	subPrefs.Unlock()

	writeJSON(subsPrefsFile(), snapshot)
}

// cloneSubPrefs copies the map so the file can be written without holding the
// lock across the disk write.
func cloneSubPrefs(m map[string]ShowSubPref) map[string]ShowSubPref {
	out := make(map[string]ShowSubPref, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
