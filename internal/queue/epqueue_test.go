package queue

import (
	"testing"

	"stremio-cliuwu/internal/stremio"
)

func ep(season, episode int, released string) stremio.Video {
	return stremio.Video{ID: "x", Season: season, Episode: episode, Released: released}
}

func TestNextWithinSeason(t *testing.T) {
	q := &EpQueue{Season: 1, Episodes: []stremio.Video{ep(1, 1, ""), ep(1, 2, "")}, Index: 0}
	v, season, ok := q.Next()
	if !ok || v.Episode != 2 || season != 1 {
		t.Errorf("got %+v %d %v", v, season, ok)
	}
	if !q.HasNext() || q.HasPrev() {
		t.Error("first of two: has a next, no previous")
	}
}

func TestNextRollsIntoLowestLaterSeason(t *testing.T) {
	all := []stremio.Video{ep(1, 1, ""), ep(1, 2, ""), ep(3, 2, ""), ep(3, 1, ""), ep(2, 5, ""), ep(2, 4, "")}
	q := &EpQueue{Season: 1, Episodes: all[:2], Index: 1, All: all}
	v, season, ok := q.Next()
	if !ok || season != 2 || v.Episode != 4 {
		t.Errorf("want season 2 episode 4 (earliest of the lowest later season), got %+v %d %v", v, season, ok)
	}
}

func TestNoNextAtTheEndOfTheShow(t *testing.T) {
	all := []stremio.Video{ep(1, 1, ""), ep(1, 2, "")}
	q := &EpQueue{Season: 1, Episodes: all, Index: 1, All: all}
	if _, _, ok := q.Next(); ok {
		t.Error("the last episode of the last season has no next")
	}
	if q.HasNext() {
		t.Error("HasNext should agree with Next")
	}
}

// Catching up with a running season: the next episode exists but cannot be
// played yet, and the two questions must give different answers.
func TestUnairedEpisodeIsUpcomingButNotNext(t *testing.T) {
	eps := []stremio.Video{ep(1, 1, "2001-01-01T00:00:00.000Z"), ep(1, 2, "2999-01-01T00:00:00.000Z")}
	q := &EpQueue{Season: 1, Episodes: eps, Index: 0, All: eps}

	if _, _, ok := q.Next(); ok {
		t.Error("an unaired episode must not be playable as Next")
	}
	if q.HasNext() {
		t.Error("HasNext must be false for an unaired episode")
	}
	v, _, ok := q.Upcoming()
	if !ok || v.Episode != 2 {
		t.Errorf("Upcoming should still report episode 2, got %+v %v", v, ok)
	}
}

func TestNilQueueIsSafe(t *testing.T) {
	var q *EpQueue
	if q.HasNext() || q.HasPrev() {
		t.Error("a nil queue has neither a next nor a previous")
	}
	if _, _, ok := q.Upcoming(); ok {
		t.Error("a nil queue has nothing upcoming")
	}
}

func TestSeasonEpisodesSorted(t *testing.T) {
	q := &EpQueue{All: []stremio.Video{ep(2, 3, ""), ep(1, 9, ""), ep(2, 1, ""), ep(2, 2, "")}}
	got := q.SeasonEpisodes(2)
	if len(got) != 3 || got[0].Episode != 1 || got[1].Episode != 2 || got[2].Episode != 3 {
		t.Errorf("got %+v", got)
	}
}
