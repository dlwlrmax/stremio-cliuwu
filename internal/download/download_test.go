package download

import (
	"path/filepath"
	"testing"

	"stremio-cliuwu/internal/queue"
	"stremio-cliuwu/internal/store"
	"stremio-cliuwu/internal/stremio"
)

func cfg(folders bool) store.AppConfig {
	var c store.AppConfig
	c.SetDefaults()
	c.DownloadFolders = folders
	return c
}

func TestPathMovieFollowsPattern(t *testing.T) {
	store.PublishCfg(cfg(true))
	got := Path("/dl", stremio.Meta{Name: "Heat", Year: "1995"}, "", nil, "https://x.test/f.mp4?t=1")
	want := filepath.Join("/dl", "Movies", "(1995) Heat.mp4")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPathMovieFallsBackToLabelWhenNameEmpty(t *testing.T) {
	store.PublishCfg(cfg(true))
	got := Path("/dl", stremio.Meta{Year: "2001"}, "My Label", nil, "https://x.test/f")
	want := filepath.Join("/dl", "Movies", "(2001) My Label.mkv") // no extension in the URL: .mkv
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPathEpisodeUsesShowSeasonEpisode(t *testing.T) {
	store.PublishCfg(cfg(true))
	q := &queue.EpQueue{
		Show:     stremio.Meta{Name: "Fate/Zero", Year: "2011"},
		Season:   1,
		Episodes: []stremio.Video{{ID: "x:1:1", Season: 1, Episode: 1, Title: "Pilot"}, {ID: "x:1:2", Season: 1, Episode: 2, Title: "Second"}},
		Index:    1,
	}
	got := Path("/dl", stremio.Meta{}, "", q, "https://x.test/f.mkv")
	// A slash inside the show name must stay one folder, not become two.
	want := filepath.Join("/dl", "Fate-Zero", "Season 01", "02 Second.mkv")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPathFlatWhenFoldersOff(t *testing.T) {
	store.PublishCfg(cfg(false))
	t.Cleanup(func() { store.PublishCfg(cfg(true)) })
	got := Path("/dl", stremio.Meta{Name: "Heat", Year: "1995"}, "", nil, "https://x.test/f.mkv")
	want := filepath.Join("/dl", "(1995) Heat.mkv")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLibraryPathKeepsPackLayoutOnlyWithFolders(t *testing.T) {
	store.PublishCfg(cfg(true))
	got := LibraryPath("/dl", "Pack Name", "S01/E01.mkv")
	if want := filepath.Join("/dl", "Pack Name", "S01", "E01.mkv"); got != want {
		t.Errorf("with folders: got %q, want %q", got, want)
	}
	store.PublishCfg(cfg(false))
	t.Cleanup(func() { store.PublishCfg(cfg(true)) })
	got = LibraryPath("/dl", "Pack Name", "S01/E01.mkv")
	if want := filepath.Join("/dl", "E01.mkv"); got != want {
		t.Errorf("flat: got %q, want %q", got, want)
	}
}

func TestStateStringIsNeverEmpty(t *testing.T) {
	for s := State(0); s < 8; s++ {
		if s.String() == "" {
			t.Errorf("state %d has no label", s)
		}
	}
}
