package main

import (
	"stremio-cliuwu/internal/jsonio"
	"stremio-cliuwu/internal/paths"
	"sync"
)

// Per-show subtitle preferences.
//
// The global SubtitleLang is the default, but a series is a long-running
// commitment and one show's subtitles often come from somewhere specific.
// This remembers the language that was picked by hand, keyed by show id.
// Autoload then looks for that language inside the file first and asks the
// plugins second, whichever way it was first picked. Movies never get an
// entry; they always use the global path.

// subOff is the Lang of a preference meaning "this show plays without
// subtitles". It is recorded when subtitles are switched off mid-episode, and
// replaced by any hand pick from the subtitle list.
const subOff = "off"

// ShowSubPref is one show's remembered choice.
type ShowSubPref struct {
	Lang string `json:"lang"`
	// Src is where the track came from: "embedded" (shipped with the stream /
	// already in the file) or "plugin" (fetched from a subtitle addon). It is
	// informational only and does not change the order autoload tries them in.
	Src string `json:"src"`
	// Track is the title of the in-file track that was picked, for telling
	// apart several tracks in one language (full, SDH, signs only). Empty when
	// the pick had no title or did not come from inside the file.
	Track string `json:"track,omitempty"`
}

// subPrefs is loaded once at startup and kept in memory; every Set/Delete
// rewrites the file. The mutex is needed because the player's read loop and
// the TUI both reach for it.
var subPrefs = struct {
	sync.RWMutex
	m map[string]ShowSubPref
}{m: map[string]ShowSubPref{}}

// subPrefsWrite orders saves. A save snapshots the map and then writes it, and
// two of those interleaving could leave the older snapshot on disk. Held for
// the whole update, but not by readers, who only take subPrefs.RWMutex.
var subPrefsWrite sync.Mutex

// LoadSubPrefs reads sub_prefs.json into memory. A missing or corrupt file is
// an empty map, never an error: a bad preference shouldn't stop playback.
func LoadSubPrefs() {
	m := map[string]ShowSubPref{}
	if err := jsonio.Read(paths.SubsPrefsFile(), &m); err != nil || m == nil {
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
	setSubPref(showID, ShowSubPref{Lang: lang, Src: src})
}

// SetEmbeddedPref remembers an in-file track: its language, and its title so a
// sibling track in the same language is not taken for it on the next episode.
func SetEmbeddedPref(showID, lang, track string) {
	setSubPref(showID, ShowSubPref{Lang: lang, Src: "embedded", Track: track})
}

func setSubPref(showID string, pref ShowSubPref) {
	if showID == "" || pref.Lang == "" {
		return
	}
	subPrefsWrite.Lock()
	defer subPrefsWrite.Unlock()

	subPrefs.Lock()
	if cur, ok := subPrefs.m[showID]; ok && cur == pref {
		subPrefs.Unlock()
		return
	}
	subPrefs.m[showID] = pref
	snapshot := cloneSubPrefs(subPrefs.m)
	subPrefs.Unlock()

	jsonio.Write(paths.SubsPrefsFile(), snapshot)
}

// DeleteSubPref forgets a show, sending it back to the global default.
func DeleteSubPref(showID string) {
	if showID == "" {
		return
	}
	subPrefsWrite.Lock()
	defer subPrefsWrite.Unlock()

	subPrefs.Lock()
	if _, ok := subPrefs.m[showID]; !ok {
		subPrefs.Unlock()
		return
	}
	delete(subPrefs.m, showID)
	snapshot := cloneSubPrefs(subPrefs.m)
	subPrefs.Unlock()

	jsonio.Write(paths.SubsPrefsFile(), snapshot)
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
