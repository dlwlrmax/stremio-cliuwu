// Package paths decides where the app keeps its files and resolves the paths
// people type into settings.
package paths

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// AppName is the directory name under the user's config and downloads folders.
const AppName = "stremio-cliuwu"

// ConfigDir returns the per-user config directory, honouring XDG on unix.
func ConfigDir() string {
	if runtime.GOOS == "windows" {
		if ad := os.Getenv("APPDATA"); ad != "" {
			return filepath.Join(ad, AppName)
		}
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, AppName)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "." + AppName
	}
	return filepath.Join(home, ".config", AppName)
}

func CfgFile() string    { return filepath.Join(ConfigDir(), "config.json") }
func AddonsFile() string { return filepath.Join(ConfigDir(), "addons.json") }
func FavsFile() string   { return filepath.Join(ConfigDir(), "favourites.json") }
func HistFile() string   { return filepath.Join(ConfigDir(), "history.json") }

// SubsPrefsFile holds per-show subtitle preferences, kept beside the global
// ones rather than inside config.json so clearing it can't touch settings.
func SubsPrefsFile() string { return filepath.Join(ConfigDir(), "sub_prefs.json") }
func BingeFile() string     { return filepath.Join(ConfigDir(), "binge.json") }
func DownloadsFile() string { return filepath.Join(ConfigDir(), "downloads.json") }

// AccountFile holds the stremio authKey, so it gets the same 0600 treatment as
// addons.json.
func AccountFile() string { return filepath.Join(ConfigDir(), "account.json") }

func EnsureDir() { os.MkdirAll(ConfigDir(), 0700) }

// DefaultDownloadDir is a sensible starting point for the settings prompt.
func DefaultDownloadDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "downloads"
	}
	return filepath.Join(home, "Downloads", AppName)
}

var winEnvVar = regexp.MustCompile(`%([A-Za-z_][A-Za-z0-9_]*)%`)

// Expand resolves what people actually type into a path field: a leading
// ~, $HOME style variables, and on Windows %USERPROFILE% style ones, which
// os.ExpandEnv doesn't understand.
func Expand(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}

	if runtime.GOOS == "windows" {
		p = winEnvVar.ReplaceAllStringFunc(p, func(m string) string {
			if v := os.Getenv(strings.Trim(m, "%")); v != "" {
				return v
			}
			return m
		})
	}
	p = os.ExpandEnv(p)

	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			rest := strings.TrimLeft(strings.TrimPrefix(p, "~"), `/\`)
			return filepath.Join(home, rest)
		}
	}
	return filepath.Clean(p)
}
