package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"stremio-cliuwu/internal/store"
	"stremio-cliuwu/internal/stremio"
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

	cfg := store.AppConfig{MpvPath: wrap, SubtitleLang: "eng, en, English", AutoSubtitle: true}
	p := NewPlayer(cfg)
	ctx = &appCtx{cfg: cfg, player: p}
	store.PublishCfg(cfg)
	store.LoadSubPrefs()
	store.Hist = &store.HistoryStore{}

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
	eps := []stremio.Video{{ID: show + ":1:1", Season: 1, Episode: 1}, {ID: show + ":1:2", Season: 1, Episode: 2}}
	return PlayRequest{
		VideoID: fmt.Sprintf("%s:1:%d", show, ep), MediaType: "series", Label: "t", URL: file,
		Entry: store.HistoryEntry{ID: show, Name: "Show", Type: "series", Season: 1, Episode: ep, VideoID: fmt.Sprintf("%s:1:%d", show, ep)},
		Queue: &EpQueue{Show: stremio.Meta{ID: show, Name: "Show"}, Season: 1, Episodes: eps, All: eps, Index: ep - 1},
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
		p, ok := store.GetSubPref("ttA")
		return ok && p.Lang == store.SubOff
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
	if p, ok := store.GetSubPref("ttC"); ok {
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

// itTrack describes one in-file subtitle track for itTracks.
type itTrack struct{ lang, title string }

// itTracks builds an 8s clip with the given subtitle tracks, in order.
func itTracks(t *testing.T, tracks ...itTrack) string {
	t.Helper()
	dir := t.TempDir()
	args := []string{"-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=d=8:s=160x90:r=10",
		"-f", "lavfi", "-i", "sine=d=8"}
	maps := []string{"-map", "0:v", "-map", "1:a"}
	var meta []string
	for i, tr := range tracks {
		srt := filepath.Join(dir, fmt.Sprintf("%d.srt", i))
		os.WriteFile(srt, []byte("1\n00:00:00,000 --> 00:00:02,000\nline\n"), 0600)
		args = append(args, "-i", srt)
		maps = append(maps, "-map", fmt.Sprintf("%d:s", i+2))
		meta = append(meta, fmt.Sprintf("-metadata:s:s:%d", i), "language="+tr.lang)
		if tr.title != "" {
			meta = append(meta, fmt.Sprintf("-metadata:s:s:%d", i), "title="+tr.title)
		}
	}
	out := filepath.Join(dir, "clip.mkv")
	args = append(args, maps...)
	args = append(args, "-c:v", "mpeg4", "-c:a", "aac", "-c:s", "srt")
	args = append(args, meta...)
	args = append(args, out)
	if b, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, b)
	}
	return out
}

// itTwoTracks builds a clip with in-file English and French subtitle tracks.
func itTwoTracks(t *testing.T) string {
	t.Helper()
	return itTracks(t, itTrack{"eng", ""}, itTrack{"fre", ""})
}

// itFakeSubAddon installs a subtitle addon that offers one track in lang.
func itFakeSubAddon(t *testing.T, lang string, delay time.Duration) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/sub.srt", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "1\n00:00:00,000 --> 00:00:02,000\nplugin\n")
	})
	mux.HandleFunc("/subtitles/", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		fmt.Fprintf(w, `{"subtitles":[{"id":"p1","url":%q,"lang":%q}]}`, srv.URL+"/sub.srt", lang)
	})
	ctx.setAddons([]stremio.Addon{{
		TransportURL: srv.URL + "/manifest.json",
		Manifest: stremio.Manifest{
			ID: "fake.subs", Name: "fake", Types: []string{"series"},
			Resources: []any{"subtitles"},
		},
	}})
}

// externalSubs counts subtitle tracks mpv has that are not inside the file.
func (e *itEnv) externalSubs() int {
	resp, err := e.p.command("get_property", "track-list")
	if err != nil {
		return -1
	}
	n := 0
	raw, _ := resp["data"].([]any)
	for _, it := range raw {
		m, _ := it.(map[string]any)
		if m["type"] == "sub" && m["external"] == true {
			n++
		}
	}
	return n
}

// Picking an in-file track in mpv's own menu is remembered for the show, and
// the next episode starts on it even though the global list ranks it lower.
func TestIntegrationHandPickedEmbeddedTrackCarriesToNextEpisode(t *testing.T) {
	e := itSetup(t)
	two := itTwoTracks(t)

	e.p.play(seriesReq(two, "ttE", 1))
	e.loaded(t)
	if got := e.sid(); got != float64(1) {
		t.Fatalf("--slang should start on English, sid=%v", got)
	}
	time.Sleep(handPickAfter + time.Second) // past the load window and any async save
	if p, ok := store.GetSubPref("ttE"); ok {
		t.Fatalf("mpv's own auto-selection must not be recorded, got %+v", p)
	}

	// The user picks French from mpv's subtitle menu.
	e.p.command("set_property", "sid", 2)
	itWait(t, 5*time.Second, "hand pick to be saved", func() bool {
		p, ok := store.GetSubPref("ttE")
		return ok && p.Lang == "French" && p.Src == "embedded"
	})

	e.p.play(seriesReq(two, "ttE", 2))
	e.loaded(t)
	itWait(t, 8*time.Second, "next episode to start on French", func() bool {
		return e.sid() == float64(2)
	})
}

// A saved language is looked for inside the file first, whatever source it was
// picked from: the plugin copy is only the fallback.
func TestIntegrationSavedLanguagePrefersEmbeddedOverPlugin(t *testing.T) {
	e := itSetup(t)
	two := itTwoTracks(t)
	itFakeSubAddon(t, "fre", 0)
	store.SetSubPref("ttF", "French", "plugin")

	e.p.play(seriesReq(two, "ttF", 2))
	e.loaded(t)
	itWait(t, 8*time.Second, "French in-file track to be selected", func() bool {
		return e.sid() == float64(2)
	})
	time.Sleep(2 * time.Second) // a plugin load would have landed by now
	if got := e.sid(); got != float64(2) {
		t.Errorf("plugin track replaced the in-file one, sid=%v", got)
	}
	if n := e.externalSubs(); n != 0 {
		t.Errorf("plugin track was loaded although the file has the language: %d external", n)
	}
}

// When the file does not have the saved language, the plugin supplies it.
func TestIntegrationSavedLanguageFallsBackToPlugin(t *testing.T) {
	e := itSetup(t)
	two := itTwoTracks(t)
	itFakeSubAddon(t, "spa", 0)
	store.SetSubPref("ttG", "Spanish", "embedded")

	e.p.play(seriesReq(two, "ttG", 2))
	e.loaded(t)
	itWait(t, 10*time.Second, "plugin Spanish track to load", func() bool {
		return e.externalSubs() == 1
	})
}

// The in-file track is applied straight away; it must not wait for the
// subtitle addons, which can take seconds to answer.
func TestIntegrationEmbeddedAppliesBeforeAddonsAnswer(t *testing.T) {
	e := itSetup(t)
	two := itTwoTracks(t)
	itFakeSubAddon(t, "fre", 6*time.Second)
	store.SetSubPref("ttH", "French", "embedded")

	e.p.play(seriesReq(two, "ttH", 2))
	e.loaded(t)
	itWait(t, 3*time.Second, "French to be on well before the addon answers", func() bool {
		return e.sid() == float64(2)
	})
}

// Several tracks share a language; the remembered title picks the right one,
// both when recording a hand pick and when replaying it.
func TestIntegrationSameLanguageTracksAreToldApartByTitle(t *testing.T) {
	e := itSetup(t)
	clip := itTracks(t, itTrack{"eng", "Full"}, itTrack{"eng", "Signs"})

	e.p.play(seriesReq(clip, "ttI", 1))
	e.loaded(t)
	time.Sleep(handPickAfter + time.Second)

	e.p.command("set_property", "sid", 2) // the second English track
	itWait(t, 5*time.Second, "titled hand pick to be saved", func() bool {
		p, ok := store.GetSubPref("ttI")
		return ok && p.Lang == "English" && p.Track == "Signs"
	})

	e.p.play(seriesReq(clip, "ttI", 2))
	e.loaded(t)
	itWait(t, 8*time.Second, "next episode to start on the Signs track", func() bool {
		return e.sid() == float64(2)
	})
}
