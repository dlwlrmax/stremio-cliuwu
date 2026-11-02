package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// These drive a real mpv, headless. They are opt-in because they need mpv and
// ffmpeg and take a few seconds each:
//
//	STREMIO_MPV_TEST=1 go test -run Integration -v ./...

type sink struct{ ch chan tea.Msg }

func (s sink) Init() tea.Cmd { return nil }
func (s sink) Update(m tea.Msg) (tea.Model, tea.Cmd) {
	select {
	case s.ch <- m:
	default:
	}
	return s, nil
}
func (s sink) View() string { return "" }

type itEnv struct {
	p     *Player
	msgs  chan tea.Msg
	vid   string // 8s clip, one embedded English subtitle
	brief string // 3s clip, same
}

func itSetup(t *testing.T) *itEnv {
	t.Helper()
	if os.Getenv("STREMIO_MPV_TEST") == "" {
		t.Skip("set STREMIO_MPV_TEST=1 to run against a real mpv")
	}
	for _, b := range []string{"mpv", "ffmpeg"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s not installed", b)
		}
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("XDG_RUNTIME_DIR", dir)

	srt := filepath.Join(dir, "en.srt")
	os.WriteFile(srt, []byte("1\n00:00:00,000 --> 00:00:02,000\nhello\n"), 0600)
	mk := func(name string, secs int) string {
		out := filepath.Join(dir, name)
		cmd := exec.Command("ffmpeg", "-v", "error", "-y",
			"-f", "lavfi", "-i", fmt.Sprintf("testsrc=d=%d:s=160x90:r=10", secs),
			"-f", "lavfi", "-i", fmt.Sprintf("sine=d=%d", secs),
			"-i", srt, "-c:v", "mpeg4", "-c:a", "aac", "-c:s", "srt",
			"-metadata:s:s:0", "language=eng", out)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v\n%s", err, b)
		}
		return out
	}

	// mpv without a window or audio device.
	real, _ := exec.LookPath("mpv")
	wrap := filepath.Join(dir, "mpvw")
	os.WriteFile(wrap, []byte("#!/bin/sh\nexec "+real+" \"$@\" --vo=null --ao=null --force-window=no\n"), 0755)

	cfg := AppConfig{MpvPath: wrap, SubtitleLang: "eng, en, English", AutoSubtitle: true}
	p := NewPlayer(cfg)
	ctx = &appCtx{cfg: cfg, player: p}
	publishCfg(cfg)
	LoadSubPrefs()
	hist = &historyStore{}

	msgs := make(chan tea.Msg, 256)
	prog := tea.NewProgram(sink{msgs}, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer())
	go prog.Run()
	time.Sleep(200 * time.Millisecond)
	p.Attach(prog)

	// The next test replaces ctx and hist; the read loop and its helpers
	// must be done with them first.
	t.Cleanup(func() {
		p.command("quit")
		itWait(t, 5*time.Second, "mpv to go away", func() bool { return !p.State().Alive })
		time.Sleep(500 * time.Millisecond)
		prog.Quit()
	})
	return &itEnv{p: p, msgs: msgs, vid: mk("long.mkv", 8), brief: mk("short.mkv", 3)}
}

func itWait(t *testing.T, d time.Duration, what string, f func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if f() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (e *itEnv) sid() any {
	resp, err := e.p.command("get_property", "sid")
	if err != nil {
		return "err:" + err.Error()
	}
	if resp["error"] != "success" {
		return "unavailable"
	}
	return resp["data"]
}

func (e *itEnv) loaded(t *testing.T) {
	t.Helper()
	itWait(t, 10*time.Second, "file loaded", func() bool {
		st := e.p.State()
		return st.Alive && !st.Loading && e.sid() != "unavailable"
	})
}

func seriesReq(file, show string, ep int) PlayRequest {
	eps := []Video{{ID: show + ":1:1", Season: 1, Episode: 1}, {ID: show + ":1:2", Season: 1, Episode: 2}}
	return PlayRequest{
		VideoID: fmt.Sprintf("%s:1:%d", show, ep), MediaType: "series", Label: "t", URL: file,
		Entry: HistoryEntry{ID: show, Name: "Show", Type: "series", Season: 1, Episode: ep, VideoID: fmt.Sprintf("%s:1:%d", show, ep)},
		Queue: &EpQueue{Show: Meta{ID: show, Name: "Show"}, Season: 1, Episodes: eps, All: eps, Index: ep - 1},
	}
}

func TestIntegrationSubsOffIsRememberedPerShow(t *testing.T) {
	e := itSetup(t)

	if msg := e.p.play(seriesReq(e.vid, "ttA", 1)); msg == nil {
		t.Fatal("play returned nothing")
	} else if err, ok := msg.(PlayerErrMsg); ok {
		t.Fatal(err.Err)
	}
	e.loaded(t)
	if got := e.sid(); got != float64(1) {
		t.Fatalf("embedded English sub should be on by default, sid=%v", got)
	}
	time.Sleep(time.Second) // let the sid event reach onProperty

	// The user turns subtitles off.
	e.p.command("set_property", "sid", "no")
	itWait(t, 5*time.Second, "off preference to be saved", func() bool {
		p, ok := GetSubPref("ttA")
		return ok && p.Lang == subOff
	})

	// Next episode: nothing may turn them back on.
	e.p.play(seriesReq(e.vid, "ttA", 2))
	e.loaded(t)
	time.Sleep(3 * time.Second) // autoload would have run by now
	if got := e.sid(); got != false {
		t.Errorf("subtitles came back on for the next episode, sid=%v", got)
	}

	// A different show is unaffected.
	e.p.play(seriesReq(e.vid, "ttB", 1))
	e.loaded(t)
	time.Sleep(time.Second)
	if got := e.sid(); got != float64(1) {
		t.Errorf("other show should still get subtitles, sid=%v", got)
	}
}

func TestIntegrationEndOfFileDoesNotRecordOff(t *testing.T) {
	e := itSetup(t)

	e.p.play(seriesReq(e.brief, "ttC", 1))
	e.loaded(t)
	time.Sleep(time.Second)

	// Let it run to the end (3s clip).
	time.Sleep(4 * time.Second)
	if p, ok := GetSubPref("ttC"); ok {
		t.Errorf("finishing an episode must not record a preference, got %+v", p)
	}
}

func TestIntegrationBadURLSurfacesError(t *testing.T) {
	e := itSetup(t)

	e.p.play(seriesReq(filepath.Join(t.TempDir(), "missing.mkv"), "ttD", 1))
	deadline := time.After(10 * time.Second)
	for {
		select {
		case m := <-e.msgs:
			if _, ok := m.(PlayerErrMsg); ok {
				return
			}
		case <-deadline:
			t.Fatal("no PlayerErrMsg for a file mpv cannot open")
		}
	}
}
