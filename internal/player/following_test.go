package player

import (
	"testing"

	"stremio-cliuwu/internal/store"
)

func TestFollowing(t *testing.T) {
	p := New(store.AppConfig{}, nil)
	if cur, _ := p.Following("v1"); cur {
		t.Error("Following says current with nothing playing")
	}

	p.mu.Lock()
	p.now = &PlayRequest{VideoID: "v1"}
	p.state.Alive = true
	p.mu.Unlock()
	if cur, ended := p.Following("v1"); !cur || ended {
		t.Errorf("playing v1: current=%v ended=%v, want true false", cur, ended)
	}
	if cur, _ := p.Following("v2"); cur {
		t.Error("Following matched a different video")
	}

	p.mu.Lock()
	p.ended = true
	p.mu.Unlock()
	if cur, ended := p.Following("v1"); !cur || !ended {
		t.Errorf("after eof: current=%v ended=%v, want true true", cur, ended)
	}

	p.mu.Lock()
	p.state.Alive = false
	p.mu.Unlock()
	if cur, _ := p.Following("v1"); cur {
		t.Error("Following says current after mpv closed")
	}
}
