// Package jsonio reads and writes the app's JSON files safely: atomic writes
// with private permissions, and corrupt files set aside rather than lost.
package jsonio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"stremio-cliuwu/internal/paths"
)

// Write writes v to path as indented JSON with 0600 perms.
//
// 0600 matters more than it looks: addon manifest URLs routinely carry a
// debrid API key in the path (torrentio.strem.fun/torbox=<key>/manifest.json),
// so addons.json is effectively a credentials file.
func Write(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(path, b, 0600)
}

// WriteAtomic writes to a temporary file in the same directory and renames
// over the target.
//
// os.WriteFile truncates in place, so an interrupted write leaves a truncated
// file rather than the previous one. For history that loses what you have
// watched; for addons.json it loses the addon urls, which carry the debrid
// keys and are not recoverable from anywhere else.
func WriteAtomic(path string, b []byte, perm os.FileMode) error {
	paths.EnsureDir()

	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	// Synced before the rename: a rename is atomic, but on a crash the
	// directory entry can land before the contents do.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}

	if err := os.Chmod(name, perm); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

func Read(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		Quarantine(path)
		return err
	}
	return nil
}

// Quarantine moves an unreadable file aside. Callers fall back to an empty
// value on a parse error, and the next save would overwrite the only copy of
// addon URLs or watch history with it; renaming keeps it recoverable.
func Quarantine(path string) {
	os.Rename(path, fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix()))
}
