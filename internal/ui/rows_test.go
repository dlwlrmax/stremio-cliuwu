package ui

import (
	"stremio-cliuwu/internal/store"
	"strings"
	"testing"
)

func TestIsFilmEntry(t *testing.T) {
	if !isFilmEntry(store.HistoryEntry{}) {
		t.Error("no season, no episode is a film")
	}
	if isFilmEntry(store.HistoryEntry{Season: 0, Episode: 2}) {
		t.Error("season 0 episode 2 is a special, not a film")
	}
	if isFilmEntry(store.HistoryEntry{Season: 1, Episode: 1}) {
		t.Error("an ordinary episode is not a film")
	}
}

func TestFavItemUsesGivenHistory(t *testing.T) {
	recent := []store.HistoryEntry{{ID: "x", Season: 2, Episode: 3}}
	it := FavItem(store.Favourite{ID: "x", Name: "X", Type: "series"}, recent)
	if !strings.Contains(it.Badge, "S02E03") {
		t.Errorf("badge %q should show the last episode", it.Badge)
	}
}
