package ui

import ()

// Item is a single row in any list screen.
type Item struct {
	Label   string // primary text
	Sub     string // dimmed secondary text
	Badge   string // right-aligned tag e.g. "[movie]" "S02 · 4/12"
	Watched bool   // prefix with green ✓
	Dim     bool   // grey out entire row
	Header  bool   // section label or blank spacer — not selectable
}

func (i Item) selectable() bool { return !i.Header }

// ── Episode queue ─────────────────────────────────────────────────────────────
