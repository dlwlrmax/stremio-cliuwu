package jsonio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadQuarantinesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.json")
	os.WriteFile(p, []byte("{not json"), 0600)
	var v map[string]any
	if Read(p, &v) == nil {
		t.Fatal("expected a parse error")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("corrupt file should have been moved aside")
	}
	m, _ := filepath.Glob(p + ".corrupt-*")
	if len(m) != 1 {
		t.Errorf("want one quarantined copy, got %v", m)
	}
}

func TestWriteReadRoundTripIsPrivateAndLeavesNoTempFiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	p := filepath.Join(dir, "x.json")

	want := map[string]int{"a": 1, "b": 2}
	if err := Write(p, want); err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	if err := Read(p, &got); err != nil || got["a"] != 1 || got["b"] != 2 {
		t.Fatalf("round trip: %v %v", got, err)
	}

	// addons.json carries debrid keys, so nobody else may read these files.
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0600 {
		t.Errorf("perm = %v, want 0600", fi.Mode().Perm())
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	if len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

func TestReadMissingFileIsNotQuarantined(t *testing.T) {
	var v map[string]any
	if err := Read(filepath.Join(t.TempDir(), "nope.json"), &v); err == nil || !os.IsNotExist(err) {
		t.Errorf("want a not-exist error, got %v", err)
	}
}
