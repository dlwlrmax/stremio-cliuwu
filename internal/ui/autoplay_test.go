package ui

import (
	"testing"

	"stremio-cliuwu/internal/player"
	"stremio-cliuwu/internal/queue"
	"stremio-cliuwu/internal/store"
	"stremio-cliuwu/internal/stremio"
)

func autoCtx(t *testing.T, on bool) {
	t.Helper()
	old := ctx
	ctx = &appCtx{cfg: store.AppConfig{AutoNext: true, AutoPlayNext: on}}
	ctx.player = player.New(ctx.cfg, nil)
	t.Cleanup(func() { ctx = old })
}

// following stands in for the player's view of the episode on screen.
func following(t *testing.T, current, ended bool) {
	t.Helper()
	old := playerFollowing
	playerFollowing = func(string) (bool, bool) { return current, ended }
	t.Cleanup(func() { playerFollowing = old })
}

func autoPicked() autoPickedMsg {
	prevQ := &queue.EpQueue{Show: stremio.Meta{ID: "tt1"}, Season: 1, Index: 0,
		Episodes: []stremio.Video{{ID: "tt1:1:1", Season: 1, Episode: 1}, {ID: "tt1:1:2", Season: 1, Episode: 2}}}
	return autoPickedMsg{
		prev:   player.PlayRequest{VideoID: "tt1:1:1", Label: "Show · S01E01", Queue: prevQ},
		target: streamTarget{VideoID: "tt1:1:2", MediaType: "series", Label: "Show · S01E02", Meta: stremio.Meta{ID: "tt1", Name: "Show", Type: "series"}},
		stream: stremio.Stream{URL: "http://x/ep2"},
		ok:     true,
	}
}

func TestAutoPickedQueuesBehindCurrentEpisode(t *testing.T) {
	autoCtx(t, true)
	following(t, true, false)
	a := &app{}
	_, cmd := a.Update(autoPicked())
	if cmd == nil {
		t.Fatal("want a command (toast + state), got nil")
	}
	q := ctx.player.Queued()
	if q == nil || q.URL != "http://x/ep2" || q.VideoID != "tt1:1:2" {
		t.Fatalf("queued = %+v, want the picked stream", q)
	}
}

// The episode ended while the streams were still being fetched: nothing would
// ever consume a queued item, so it has to play now.
func TestAutoPickedPlaysAtOnceWhenEpisodeAlreadyEnded(t *testing.T) {
	autoCtx(t, true)
	following(t, true, true)
	a := &app{}
	_, cmd := a.Update(autoPicked())
	if cmd == nil {
		t.Fatal("want a play command")
	}
	if q := ctx.player.Queued(); q != nil {
		t.Errorf("queued = %+v, want it played, not parked", q)
	}
}

// You started something else (or closed mpv) while the fetch was running.
func TestAutoPickedDroppedWhenEpisodeIsNoLongerOnScreen(t *testing.T) {
	autoCtx(t, true)
	following(t, false, false)
	a := &app{}
	_, cmd := a.Update(autoPicked())
	if cmd != nil {
		t.Error("a stale pick produced a command")
	}
	if q := ctx.player.Queued(); q != nil {
		t.Errorf("queued = %+v, want nothing", q)
	}
}

func TestAutoPickedNeverReplacesYourOwnQueue(t *testing.T) {
	autoCtx(t, true)
	following(t, true, false)
	ctx.player.Queue(player.PlayRequest{VideoID: "tt1:1:2", URL: "http://x/mine"})
	a := &app{}
	a.Update(autoPicked())
	if q := ctx.player.Queued(); q == nil || q.URL != "http://x/mine" {
		t.Errorf("queued = %+v, want your own pick left alone", q)
	}
}

func TestAutoPickedNothingSafeQueuesNothing(t *testing.T) {
	autoCtx(t, true)
	following(t, true, false)
	a := &app{}
	m := autoPicked()
	m.ok = false
	m.listShown = true
	_, cmd := a.Update(m)
	if q := ctx.player.Queued(); q != nil {
		t.Errorf("queued = %+v, want nothing", q)
	}
	if cmd != nil {
		t.Error("list was already shown at prefetch; nothing more to do")
	}
}

// With no pick and no list yet (episode ended first), the list opens now.
func TestAutoPickedNothingSafeOpensListIfNoneShown(t *testing.T) {
	autoCtx(t, true)
	following(t, true, true)
	a := &app{stack: []screen{&subsScreen{}}}
	m := autoPicked()
	m.ok = false
	if _, cmd := a.Update(m); cmd == nil {
		t.Error("no safe pick and no list shown: want the next episode surfaced")
	}
}

func TestAutoPickedIgnoredWhenSettingOff(t *testing.T) {
	autoCtx(t, false)
	following(t, true, false)
	a := &app{}
	a.Update(autoPicked())
	if q := ctx.player.Queued(); q != nil {
		t.Errorf("queued = %+v with autoplay off", q)
	}
}

func TestAutoPickCmdOffWhenSettingsOff(t *testing.T) {
	prev := player.PlayRequest{Queue: nil}
	autoCtx(t, true)
	if autoPickCmd(prev, true) != nil {
		t.Error("a request with no episode queue (a film) must not autopick")
	}
	autoCtx(t, false)
	if autoPickCmd(prev, true) != nil {
		t.Error("autopick ran with the setting off")
	}
}
