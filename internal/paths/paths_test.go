package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestConfigDirHonoursXDG(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows uses APPDATA")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if got, want := ConfigDir(), filepath.Join(dir, AppName); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := SubsPrefsFile(), filepath.Join(dir, AppName, "sub_prefs.json"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("STREMIO_TEST_DIR", "/data")

	cases := []struct{ in, want string }{
		{"", ""},
		{"  ", ""},
		{"~", home},
		{"~/Videos", filepath.Join(home, "Videos")},
		{"$STREMIO_TEST_DIR/shows", filepath.Join("/data", "shows")},
		{"/a/b/../c", filepath.Clean("/a/c")},
	}
	for _, c := range cases {
		if got := Expand(c.in); got != c.want {
			t.Errorf("Expand(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatal(err)
	}
}
