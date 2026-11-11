package store

import "stremio-cliuwu/internal/paths"

// Per-show subtitle preferences.
//
// The global SubtitleLang is the default, but a series is a long-running
// commitment and one show's subtitles often come from somewhere specific.
// This remembers the language that was picked by hand, keyed by show id.
// Autoload then looks for that language inside the file first and asks the
// plugins second, whichever way it was first picked. Movies never get an
// entry; they always use the global path.

// SubOff is the Lang of a preference meaning "this show plays without
// subtitles". It is recorded when subtitles are switched off mid-episode, and
// replaced by any hand pick from the subtitle list.
const SubOff = "off"

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

var subPrefs = newShowPrefs[ShowSubPref](paths.SubsPrefsFile)

// LoadSubPrefs reads sub_prefs.json into memory.
func LoadSubPrefs() { subPrefs.load() }

// GetSubPref looks up a show's preference.
func GetSubPref(showID string) (ShowSubPref, bool) { return subPrefs.get(showID) }

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
	if pref.Lang == "" {
		return
	}
	subPrefs.set(showID, pref)
}

// DeleteSubPref forgets a show, sending it back to the global default.
func DeleteSubPref(showID string) { subPrefs.del(showID) }
