package main

import (
	"fmt"
	"strings"
	"time"
)

// Every list row is built here.
//
// These used to live beside the code that loads the data — HistoryItem in
// history.go, FavItem in favs.go — which meant the storage layer reached into
// the styling layer to produce ANSI. Wrong direction, and the thing that would
// block ever splitting this into packages.

func metaItem(m Meta) Item {
	if m.Name == "" {
		m.Name = m.ID // some debrid catalogs omit names entirely
	}
	yr := m.Year
	if yr == "" {
		yr = "?"
	}
	return Item{
		Label: bold(m.Name),
		Sub:   "(" + yr + ")",
		Badge: kindTag(m.Source, m.Type),
	}
}

func epItem(v Video, watched bool) Item {
	badge := ""
	if r := fmtRelease(v.Released); r != "" {
		badge = grey(r)
	}

	it := Item{
		Label:   bold(hi(fmt.Sprintf("E%02d", v.Episode))),
		Sub:     v.Title,
		Badge:   badge,
		Watched: watched,
	}

	// Catalogs list episodes well before they air. Without a marker they look
	// identical to everything else, and you only find out when the stream
	// search comes back empty.
	if !videoAired(v) {
		// The date, same as an aired episode — a row is for scanning, and
		// switching format between aired and unaired broke the column. How
		// far off it is belongs in the info panel, where there's room to say
		// it properly.
		it.Badge = stWarn.Render("○ " + fmtRelease(v.Released))
		it.Dim = true
	}
	return it
}

func HistoryItem(e HistoryEntry) Item {
	yr := e.Year
	if yr == "" {
		yr = "?"
	}
	ep := ""
	if e.Episode > 0 {
		ep = fmt.Sprintf("  S%02dE%02d", e.Season, e.Episode)
		if e.EpTitle != "" {
			ep += "  " + e.EpTitle
		}
	}
	label := bold(e.Name) + grey("  ("+yr+")") + ep

	badge := kindTag(e.Source, e.Type)
	if e.Watched {
		badge += "  " + good("✓")
	} else if e.Position > 0 && e.Duration > 0 {
		badge += "  " + yell("▶ "+fmtSecs(e.Position))
	}

	kind := "dmy"
	if ctx != nil {
		kind = ctx.cfg.DateFormat
	}
	badge += grey("  " + e.WatchedAt.Format(dateLayout(kind)))

	return Item{Label: label, Badge: badge, Watched: e.Watched}
}

// ShowHistoryItem is a title row in the grouped history view.
func ShowHistoryItem(sh ShowSummary) Item {
	// No "(?)" filler. A library entry is a torrent name with no release
	// year to know, so an empty pair of brackets says nothing except that
	// something is missing.
	label := bold(sh.Name)
	if sh.Year != "" {
		label += "  " + grey("("+sh.Year+")")
	}

	// A library entry holds files, a series holds episodes, and a film is
	// just itself — calling any of them by the wrong noun reads as a bug.
	noun := "episode"
	if sh.Type == "other" {
		noun = "file"
	}

	sub := "film"
	switch {
	case sh.Type == "movie":
	case sh.Episodes == 1:
		sub = "1 " + noun
	default:
		sub = fmt.Sprintf("%d %ss", sh.Episodes, noun)
	}

	badge := kindTag(sh.Source, sh.Type)
	if sh.SeenAt > 0 {
		kind := "dmy"
		if ctx != nil {
			kind = ctx.cfg.DateFormat
		}
		badge = grey(time.Unix(sh.SeenAt, 0).Format(dateLayout(kind))) + "  " + badge
	}
	return Item{Label: label, Sub: sub, Badge: badge}
}

func FavItem(f Favourite) Item {
	yr := f.Year
	if yr == "" {
		yr = "?"
	}
	season := ""
	if f.Season > 0 {
		season = fmt.Sprintf("  S%02d", f.Season)
	}
	label := fmt.Sprintf("%s  %s", bold(f.Name), grey("("+yr+")"))

	// Get watch progress from history
	badge := kindTag(f.Source, f.Type) + season
	if f.Type == "series" {
		h := LoadHistory()
		var lastEntry *HistoryEntry
		for i := range h.Items {
			e := &h.Items[i]
			if e.ID == f.ID {
				if f.Season == 0 || e.Season == f.Season {
					lastEntry = e
					break
				}
			}
		}
		if lastEntry != nil {
			if lastEntry.Watched {
				badge += "  " + good("✓")
			} else if lastEntry.Position > 0 && lastEntry.Duration > 0 {
				badge += "  " + yell("▶ "+fmtSecs(lastEntry.Position))
			} else if lastEntry.Episode > 0 {
				badge += "  " + grey(fmt.Sprintf("S%02dE%02d", lastEntry.Season, lastEntry.Episode))
			}
		}
	}

	return Item{Label: label, Badge: badge}
}

// AddonItem renders an addon row for the addons screen.
func AddonItem(ref AddonRef, a *Addon) Item {
	name := RedactURL(ref.URL)
	sub := ""
	badge := ""

	switch {
	case a == nil:
		badge = grey("…")
	case a.Err != nil:
		badge = bad("failed")
		sub = a.Err.Error()
	default:
		name = a.Manifest.Name
		var caps []string
		if a.HasStreams() {
			caps = append(caps, "streams")
		}
		if len(a.Manifest.Catalogs) > 0 {
			caps = append(caps, fmt.Sprintf("%d catalog(s)", len(a.Manifest.Catalogs)))
		}
		sub = strings.Join(caps, " · ")
		if a.Manifest.Version != "" {
			badge = grey("v" + a.Manifest.Version)
		}

		// Flags the addon declares about itself. Configuration especially:
		// an addon awaiting setup returns empty results rather than errors,
		// so without this it just looks broken.
		h := a.Manifest.BehaviorHints
		if h.P2P {
			badge = stWarn.Render("p2p") + "  " + badge
		}
		if h.ConfigurationRequired {
			badge = bad("needs configuring") + "  " + badge
		}
	}

	if ref.Disabled {
		badge = grey("off")
	}
	return Item{Label: bold(name), Sub: sub, Badge: badge, Dim: ref.Disabled}
}
