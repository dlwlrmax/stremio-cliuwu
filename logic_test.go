package main

import (
	"testing"
)

// A hand pick in mpv's own menu is only worth remembering when it is a track
// inside the file: external ones are what our sub-add produced, and a track
// with no language tag could never be matched on the next episode.
func TestEmbeddedSubAt(t *testing.T) {
	tracks := []any{
		map[string]any{"type": "video", "id": float64(1)},
		map[string]any{"type": "sub", "id": float64(1), "lang": "eng", "title": "Full"},
		map[string]any{"type": "sub", "id": float64(2), "lang": "fre"},
		map[string]any{"type": "sub", "id": float64(3), "external": true, "lang": "vie"},
		map[string]any{"type": "sub", "id": float64(4)},
	}
	cases := []struct {
		sid         int
		lang, title string
		ok          bool
	}{
		{1, "English", "Full", true},
		{2, "French", "", true},
		{3, "", "", false}, // external: our own sub-add
		{4, "", "", false}, // no language tag
		{9, "", "", false}, // not in the list
	}
	for _, c := range cases {
		lang, title, ok := embeddedSubAt(tracks, c.sid)
		if lang != c.lang || title != c.title || ok != c.ok {
			t.Errorf("sid %d: got (%q, %q, %v), want (%q, %q, %v)", c.sid, lang, title, ok, c.lang, c.title, c.ok)
		}
	}
}

// A file often has several tracks in one language (full, SDH, signs only), so
// the remembered title picks between them, and an external track with the same
// language is never an "in-file" match.
func TestPickEmbeddedSub(t *testing.T) {
	tracks := []any{
		map[string]any{"type": "sub", "id": float64(1), "lang": "eng", "title": "Signs & Songs"},
		map[string]any{"type": "sub", "id": float64(2), "lang": "eng", "title": "Full", "selected": true},
		map[string]any{"type": "sub", "id": float64(3), "lang": "eng", "external": true},
		map[string]any{"type": "sub", "id": float64(4), "lang": "fre"},
	}
	cases := []struct {
		name, lang, title string
		id                int
		selected, ok      bool
	}{
		{"title picks among same language", "English", "Signs & Songs", 1, false, true},
		{"already active is reported", "English", "Full", 2, true, true},
		{"unknown title falls back to first", "English", "Missing", 1, false, true},
		{"no title takes first", "English", "", 1, false, true},
		{"other language", "French", "", 4, false, true},
		{"absent language", "Spanish", "", 0, false, false},
	}
	for _, c := range cases {
		id, sel, ok := pickEmbeddedSub(tracks, c.lang, c.title)
		if id != c.id || sel != c.selected || ok != c.ok {
			t.Errorf("%s: got (%d, %v, %v), want (%d, %v, %v)", c.name, id, sel, ok, c.id, c.selected, c.ok)
		}
	}

	// Only an external track has the language: nothing in the file.
	only := []any{map[string]any{"type": "sub", "id": float64(3), "lang": "eng", "external": true}}
	if _, _, ok := pickEmbeddedSub(only, "English", ""); ok {
		t.Error("an external track must not count as in-file")
	}
}
