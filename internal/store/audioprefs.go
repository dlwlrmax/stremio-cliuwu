package store

import "stremio-cliuwu/internal/paths"

// Per-show audio preferences.
//
// The global AudioLang ranks a file's audio tracks on every load, but a show
// watched dubbed is a choice made once, not once per episode. This remembers
// the track the user switched to in mpv, keyed by show id, and the autoload
// ranks that language ahead of the global list for the show's next episode.
// Movies never get an entry; they always use the global path.

// ShowAudioPref is one show's remembered audio track.
type ShowAudioPref struct {
	Lang string `json:"lang"`
	// Track is the title of the picked track, for telling apart several in one
	// language (stereo and surround, commentary). Empty when it had none.
	Track string `json:"track,omitempty"`
}

var audioPrefs = newShowPrefs[ShowAudioPref](paths.AudioPrefsFile)

// LoadAudioPrefs reads audio_prefs.json into memory.
func LoadAudioPrefs() { audioPrefs.load() }

// GetAudioPref looks up a show's preference.
func GetAudioPref(showID string) (ShowAudioPref, bool) { return audioPrefs.get(showID) }

// SetAudioPref remembers the audio track a show was switched to.
func SetAudioPref(showID, lang, track string) {
	if lang == "" {
		return
	}
	audioPrefs.set(showID, ShowAudioPref{Lang: lang, Track: track})
}

// DeleteAudioPref forgets a show, sending it back to the global default.
func DeleteAudioPref(showID string) { audioPrefs.del(showID) }
