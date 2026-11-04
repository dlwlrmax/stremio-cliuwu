package ui

import (
	"stremio-cliuwu/internal/format"
	"strings"
)

func fmtRelease(s string) string {
	t, ok := format.ParseRelease(s)
	if !ok {
		return ""
	}

	kind := "dmy"
	if ctx != nil {
		kind = ctx.cfg.DateFormat
	}

	// As published, matching daysUntil — the date a database gives for an
	// episode is the broadcast day, and reinterpreting it in another zone
	// would print a different day than the countdown beside it counts to.
	return t.Format(format.DateLayout(kind))
}

// kindTag picks whichever of source or type is a bucket we can render.
//
// Source is the better answer when it's set to something meaningful, but it
// isn't always — it can arrive empty, or carry a value from elsewhere in the
// protocol like "series". Falling back to the item's own type beats printing
// "[?]" about something we can perfectly well identify.
func kindTag(source, typ string) string {
	if format.KnownKind(source) {
		return sourceTag(source)
	}
	if format.KnownKind(typ) {
		return sourceTag(typ)
	}
	if typ == "series" {
		return sourceTag("show")
	}
	return sourceTag(source)
}

func sourceTag(source string) string {
	switch source {
	case "movie":
		return accent("[movie]")
	case "show":
		return hi("[show]")
	case "anime":
		return yell("[anime]")
	case "other":
		return grey("[library]")
	}
	return grey("[?]")
}

// progressBar draws a filled bar of the given width.
func progressBar(frac float64, w int) string {
	if w <= 0 {
		return ""
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	fill := int(frac * float64(w))
	return stBarFill.Render(strings.Repeat("━", fill)) +
		stBarRest.Render(strings.Repeat("━", w-fill))
}
