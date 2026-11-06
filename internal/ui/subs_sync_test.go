package ui

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"stremio-cliuwu/internal/player"
	"stremio-cliuwu/internal/stremio"
	"stremio-cliuwu/internal/subsync"
)

func runeKey(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

// syncScreen is a loaded subtitle list with two English subs, row 0 selected.
// The screen reads the app's global ctx, so a bare one is installed for the
// test and put back after.
func syncScreen(t *testing.T) *subsScreen {
	t.Helper()
	old := ctx
	ctx = &appCtx{}
	t.Cleanup(func() { ctx = old })
	s := &subsScreen{
		id:     newAsyncID(),
		list:   newList(),
		loaded: true,
		subs: []stremio.Subtitle{
			{ID: "1", URL: "http://a/one", Lang: "eng", Addon: "x"},
			{ID: "2", URL: "http://a/two", Lang: "eng", Addon: "x"},
		},
	}
	s.collectLangs()
	s.rebuild()
	return s
}

// msgOf runs a command and returns what it produced.
func msgOf(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("want a command, got nil")
	}
	return cmd()
}

func wantToast(t *testing.T, cmd tea.Cmd, isErr bool, contains string) {
	t.Helper()
	m, ok := msgOf(t, cmd).(toastMsg)
	if !ok {
		t.Fatalf("want a toast, got %T", msgOf(t, cmd))
	}
	if m.isErr != isErr || !strings.Contains(m.text, contains) {
		t.Errorf("toast = %+v, want isErr=%v containing %q", m, isErr, contains)
	}
}

func TestMarkReferenceToggles(t *testing.T) {
	s := syncScreen(t)
	s.Update(runeKey('m'))
	if s.ref == nil || s.ref.URL != "http://a/one" {
		t.Fatalf("ref = %+v, want the first sub", s.ref)
	}
	s.Update(runeKey('m'))
	if s.ref != nil {
		t.Errorf("ref = %+v after marking the same row again, want nil", s.ref)
	}
}

func TestRebuildMarksReference(t *testing.T) {
	s := syncScreen(t)
	s.Update(runeKey('m'))
	if !strings.Contains(s.list.items[0].Badge, "ref") {
		t.Errorf("marked row badge = %q, want it to say ref", s.list.items[0].Badge)
	}
	if strings.Contains(s.list.items[1].Badge, "ref") {
		t.Errorf("unmarked row badge = %q, must not say ref", s.list.items[1].Badge)
	}
}

func TestSyncWithoutReference(t *testing.T) {
	s := syncScreen(t)
	_, cmd := s.Update(runeKey('S'))
	wantToast(t, cmd, true, "mark a well-timed sub first")
	if s.syncing {
		t.Error("syncing is true with no reference to sync to")
	}
}

func TestSyncRowIsReference(t *testing.T) {
	s := syncScreen(t)
	s.Update(runeKey('m'))
	_, cmd := s.Update(runeKey('S'))
	wantToast(t, cmd, true, "pick a different sub to sync")
	if s.syncing {
		t.Error("syncing is true for the reference row itself")
	}
}

func TestSyncIgnoredWhileRunning(t *testing.T) {
	s := syncScreen(t)
	s.Update(runeKey('m'))
	s.list.Focus(1)
	s.syncing = true
	cancelled := 0
	s.syncCancel = func() { cancelled++ }

	_, cmd := s.Update(runeKey('S'))
	if cmd != nil {
		t.Error("S while a sync is running returned a command")
	}
	s.syncCancel()
	if cancelled != 1 {
		t.Error("S while a sync is running replaced the cancel function of the one in flight")
	}
}

func TestSyncedMsgForStaleScreen(t *testing.T) {
	s := syncScreen(t)
	s.syncing = true
	_, cmd := s.Update(subSyncedMsg{id: s.id + 1000, err: errors.New("late")})
	if cmd != nil || !s.syncing {
		t.Errorf("a result for another screen changed this one (cmd=%v, syncing=%v)", cmd != nil, s.syncing)
	}
}

func TestSyncedMsgError(t *testing.T) {
	s := syncScreen(t)
	s.syncing = true
	_, cmd := s.Update(subSyncedMsg{id: s.id, err: subsync.ErrNoAlass})
	wantToast(t, cmd, true, "install alass-cli")
	if s.syncing {
		t.Error("syncing still true after a failure")
	}

	s.syncing = true
	_, cmd = s.Update(subSyncedMsg{id: s.id, err: errors.New("caused by: bad digit")})
	wantToast(t, cmd, true, "caused by: bad digit")
}

func TestAddedMsgForSyncedPathMarksOrigin(t *testing.T) {
	s := syncScreen(t)
	s.synced = map[string]string{"/tmp/x/out.srt": "http://a/two"}
	s.Update(player.SubtitleAddedMsg{URL: "/tmp/x/out.srt", Title: "English"})
	if s.active != "http://a/two" {
		t.Errorf("active = %q, want the originating sub's url", s.active)
	}
}

func TestLeavingCancelsSync(t *testing.T) {
	for _, k := range []tea.KeyMsg{{Type: tea.KeyEsc}, runeKey('q')} {
		s := syncScreen(t)
		s.syncing = true
		cancelled := false
		s.syncCancel = func() { cancelled = true }
		s.Update(k)
		if !cancelled {
			t.Errorf("%v did not cancel the running sync", k)
		}
	}
}

// The app binds S globally to "open subtitles", and global keys run before the
// screen sees the key. On the subtitle list itself that would open a second
// list and the sync key would never arrive.
func TestGlobalSStepsAsideOnSubsScreen(t *testing.T) {
	a := newApp(syncScreen(t))
	if _, handled := a.globalKey(runeKey('S')); handled {
		t.Error("global S handled the key on the subtitle list; the list's own S never runs")
	}
}

// A sub that was synced earlier loads from the retimed file, not the original.
func TestEnterPrefersSyncedFile(t *testing.T) {
	t.Cleanup(subsync.Cleanup)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alass-cli"), []byte("#!/bin/sh\ncp \"$2\" \"$3\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "1\n00:00:01,000 --> 00:00:02,000\nhi\n")
	}))
	t.Cleanup(srv.Close)

	s := syncScreen(t)
	s.subs[0].URL, s.subs[1].URL = srv.URL+"/one", srv.URL+"/two"
	s.query.VideoID = "tt1:1:1"
	s.rebuild()
	s.ref = &s.subs[0]
	s.list.Focus(1)

	// Sync row 1 for real, the way S does, and feed the result back.
	_, cmd := s.Update(runeKey('S'))
	var synced subSyncedMsg
	for _, c := range flatten(msgOf(t, cmd)) {
		if m, ok := c().(subSyncedMsg); ok {
			synced = m
		}
	}
	if synced.err != nil || synced.path == "" {
		t.Fatalf("sync failed: %+v", synced)
	}
	s.Update(synced)

	// A fresh list for the same video: Enter on the same sub.
	s2 := syncScreen(t)
	s2.subs[1].URL = srv.URL + "/two"
	s2.query.VideoID = "tt1:1:1"
	s2.rebuild()
	s2.list.Focus(1)
	_, cmd = s2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	// The load itself needs a live mpv, so the commands are run with the
	// player's panic swallowed and the toast says which file was chosen.
	var toastText string
	for _, c := range flatten(msgOf(t, cmd)) {
		func() {
			defer func() { _ = recover() }()
			if m, ok := c().(toastMsg); ok {
				toastText = m.text
			}
		}()
	}
	if !strings.Contains(toastText, "synced") {
		t.Errorf("Enter toast = %q, want it to say the synced copy is loading", toastText)
	}
	if got, ok := subsync.Recall(s2.syncKey(s2.subs[1])); !ok || got != synced.path {
		t.Errorf("recalled %q, %v; want %q", got, ok, synced.path)
	}
}

// flatten expands a tea.Batch into its commands; a plain message becomes one.
func flatten(m tea.Msg) []tea.Cmd {
	if b, ok := m.(tea.BatchMsg); ok {
		var out []tea.Cmd
		for _, c := range b {
			if c != nil {
				out = append(out, c)
			}
		}
		return out
	}
	return []tea.Cmd{func() tea.Msg { return m }}
}
