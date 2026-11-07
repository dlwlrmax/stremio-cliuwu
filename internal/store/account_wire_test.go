package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// Stremio keeps the episode pointer under state.video_id. A different spelling
// is dropped by the server, which then stores "" — and a second machine, with
// no pointer to read, can't tell which episode the progress belongs to.
func TestLibraryItemUsesVideoIDWireName(t *testing.T) {
	series := LocalLibraryEntry{ID: "tt1", Name: "Show", Season: 1, Episode: 2, Position: 5, Duration: 10, MTime: 100}
	movie := LocalLibraryEntry{ID: "tt2", Name: "Film", MTime: 100}

	for _, tc := range []struct {
		e    LocalLibraryEntry
		want string
	}{
		{series, `"video_id":"tt1:1:2"`},
		{movie, `"video_id":"tt2"`},
	} {
		b, err := json.Marshal(tc.e.toLibraryItem())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), tc.want) {
			t.Errorf("%s: want %s on the wire, got %s", tc.e.ID, tc.want, b)
		}
		if strings.Contains(string(b), `"videoId"`) {
			t.Errorf("%s: wire still carries videoId: %s", tc.e.ID, b)
		}
	}
}

func TestPullReadsVideoID(t *testing.T) {
	var it libraryItem
	if err := json.Unmarshal([]byte(`{"_id":"tt1","state":{"video_id":"tt1:1:2"}}`), &it); err != nil {
		t.Fatal(err)
	}
	if it.State.VideoID != "tt1:1:2" {
		t.Errorf("VideoID = %q, want the server's video_id", it.State.VideoID)
	}
}

func TestSelectPush(t *testing.T) {
	local := []LocalLibraryEntry{
		{ID: "new", Name: "n", MTime: 200},    // server has it older
		{ID: "same", Name: "s", MTime: 100},   // server has it, with a pointer
		{ID: "blank", Name: "b", MTime: 100},  // server has it as new as ours, no pointer
		{ID: "ahead", Name: "a", MTime: 100},  // server is newer and has no pointer
		{ID: "absent", Name: "x", MTime: 100}, // server never saw it
	}
	remote := map[string]int64{"new": 100, "same": 100, "blank": 100, "ahead": 300}
	blank := map[string]bool{"blank": true, "ahead": true}

	got := map[string]bool{}
	for _, it := range selectPush(local, remote, blank) {
		got[it.ID] = true
	}
	want := map[string]bool{"new": true, "blank": true, "absent": true}
	for id := range want {
		if !got[id] {
			t.Errorf("%s was not pushed", id)
		}
	}
	for id := range got {
		if !want[id] {
			t.Errorf("%s was pushed; the server's copy is already as good or newer", id)
		}
	}
}
