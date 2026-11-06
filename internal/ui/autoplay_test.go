package ui

import (
	"testing"

	"stremio-cliuwu/internal/player"
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

func autoPicked() autoPickedMsg {
	return autoPickedMsg{
		target: streamTarget{VideoID: "tt1:1:2", MediaType: "series", Label: "Show · S01E02", Meta: stremio.Meta{ID: "tt1", Name: "Show", Type: "series"}},
		stream: stremio.Stream{URL: "http://x/ep2"},
		ok:     true,
	}
}

func TestAutoPickedQueuesBehindCurrentEpisode(t *testing.T) {
	autoCtx(t, true)
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

func TestAutoPickedNeverReplacesYourOwnQueue(t *testing.T) {
	autoCtx(t, true)
	ctx.player.Queue(player.PlayRequest{VideoID: "tt1:1:2", URL: "http://x/mine"})
	a := &app{}
	a.Update(autoPicked())
	if q := ctx.player.Queued(); q == nil || q.URL != "http://x/mine" {
		t.Errorf("queued = %+v, want your own pick left alone", q)
	}
}

func TestAutoPickedNothingSafeQueuesNothing(t *testing.T) {
	autoCtx(t, true)
	a := &app{}
	m := autoPicked()
	m.ok = false
	a.Update(m)
	if q := ctx.player.Queued(); q != nil {
		t.Errorf("queued = %+v, want nothing", q)
	}
}

func TestAutoPickedIgnoredWhenSettingOff(t *testing.T) {
	autoCtx(t, false)
	a := &app{}
	a.Update(autoPicked())
	if q := ctx.player.Queued(); q != nil {
		t.Errorf("queued = %+v with autoplay off", q)
	}
}

func TestAutoPickCmdOffWhenSettingsOff(t *testing.T) {
	prev := player.PlayRequest{Queue: nil}
	autoCtx(t, true)
	if autoPickCmd(prev, false) != nil {
		t.Error("a request with no episode queue (a film) must not autopick")
	}
	autoCtx(t, false)
	if autoPickCmd(prev, false) != nil {
		t.Error("autopick ran with the setting off")
	}
}
