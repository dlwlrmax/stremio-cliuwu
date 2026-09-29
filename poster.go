package main

import (
	"fmt"
	"image"
	"math"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	// Pure Go, no cgo — cross-compilation stays as it is. Covers lossy VP8
	// and lossless VP8L; animated webp isn't supported upstream, which
	// doesn't matter for posters.
	_ "golang.org/x/image/webp"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Posters are drawn as Unicode half blocks: one cell holds two vertical
// pixels, "▀" with the top pixel as foreground and the bottom as background.
//
// The inline image protocols (kitty, sixel, iTerm2) look better, but they
// don't fit a diffing renderer. Bubble Tea builds a string per frame and
// compares it to the last one; an escape sequence that paints over several
// rows isn't something it can measure, so repaints, scrolling and resize all
// go wrong unless the renderer itself reserves the space. Half blocks are just
// text — width calculations, the pane layout and resize all work untouched,
// and there's no C library or protocol negotiation involved.

// A cell is one pixel wide, so the cell width *is* the horizontal resolution:
// a 22-cell poster is a 22-pixel image, which no amount of careful scaling
// rescues. More cells is the only real quality dial, which is why the size is
// a setting rather than a fixed constant.
var posterSizes = []string{"small", "medium", "large", "xl", "xxl"}

// posterBudget returns the cell limits for a size, given the panel dimensions.
// Height used to bind first at small sizes (e.g. medium capped at paneH/3),
// which shrank a 2:3 poster to ~13 cells wide — inherently blurry. Budgets now
// favor width so the panel width binds instead; the panel scrolls, so extra
// height is cheaper than lost horizontal resolution.
func posterBudget(size string, paneW, paneH int) (int, int) {
	switch size {
	case "small":
		return min(paneW, 24), max(6, paneH/3)
	case "large":
		return min(paneW, 56), max(12, paneH*2/3)
	case "xl":
		return min(paneW, 72), max(14, paneH*3/4)
	case "xxl":
		// Fills the panel width. Every other size caps height first, which
		// leaves the width short whenever the panel is taller than it is
		// wide — this lets width bind instead and accepts a poster taller
		// than the visible area, since the panel scrolls.
		return min(paneW, 120), max(14, paneH)
	}
	return min(paneW, 40), max(10, paneH/2) // medium
}

// nextPosterSize cycles through the sizes.
func nextPosterSize(cur string) string {
	for i, s := range posterSizes {
		if s == cur {
			return posterSizes[(i+1)%len(posterSizes)]
		}
	}
	return "medium"
}

// posterGen bumps when the size setting changes, so panels know to re-render.
var posterGen int

var (
	cachePosterImg = newCache[*image.RGBA](30*time.Minute, 60)
	cachePosterArt = newCache[string](30*time.Minute, 120)
)

type posterMsg struct {
	id  asyncID
	url string
	art string
}

// openURL hands a link to the desktop.
func openURL(u string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	case "darwin":
		return exec.Command("open", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}

// ── Colour ────────────────────────────────────────────────────────────────────

// posterColors reports whether the terminal can render half blocks usefully.
func posterColors() termenv.Profile { return lipgloss.ColorProfile() }

func rgb256(r, g, b uint8) int {
	near := func(a, b uint8) bool {
		if a > b {
			return a-b < 8
		}
		return b-a < 8
	}
	if near(r, g) && near(g, b) {
		switch {
		case r < 8:
			return 16
		case r > 248:
			return 231
		}
		return 232 + int(r-8)*24/247
	}
	return 16 + 36*(int(r)*5/255) + 6*(int(g)*5/255) + int(b)*5/255
}

func cellEscape(profile termenv.Profile, tr, tg, tb, br, bg, bb uint8) string {
	if profile == termenv.TrueColor {
		return fmt.Sprintf("\x1b[38;2;%d;%d;%d;48;2;%d;%d;%dm▀", tr, tg, tb, br, bg, bb)
	}
	return fmt.Sprintf("\x1b[38;5;%d;48;5;%dm▀", rgb256(tr, tg, tb), rgb256(br, bg, bb))
}

// ── Scaling ───────────────────────────────────────────────────────────────────

// boxScale downscales by averaging each destination pixel's source box in
// linear light. Slower than nearest neighbour but far less noisy, which
// matters a lot when the result is 22 cells wide. Averaging directly in sRGB
// darkens mid-tones and looks soft/muddy, so each channel goes sRGB -> linear
// (gamma 2.2), averages there, then converts back.
func boxScale(src *image.RGBA, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw == 0 || sh == 0 || w == 0 || h == 0 {
		return dst
	}
	toLinear := func(c uint8) float64 {
		return math.Pow(float64(c)/255, 2.2)
	}
	toSRGB := func(v float64) uint8 {
		return uint8(math.Pow(v, 1/2.2)*255 + 0.5)
	}

	for y := range h {
		y0 := sb.Min.Y + y*sh/h
		y1 := sb.Min.Y + (y+1)*sh/h
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := range w {
			x0 := sb.Min.X + x*sw/w
			x1 := sb.Min.X + (x+1)*sw/w
			if x1 <= x0 {
				x1 = x0 + 1
			}

			var r, g, b float64
			var n float64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					c := src.RGBAAt(sx, sy)
					r += toLinear(c.R)
					g += toLinear(c.G)
					b += toLinear(c.B)
					n++
				}
			}
			if n == 0 {
				continue
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = toSRGB(r / n)
			dst.Pix[i+1] = toSRGB(g / n)
			dst.Pix[i+2] = toSRGB(b / n)
			dst.Pix[i+3] = 255
		}
	}
	return dst
}

func toRGBA(src image.Image) *image.RGBA {
	if r, ok := src.(*image.RGBA); ok {
		return r
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := range b.Dy() {
		for x := range b.Dx() {
			r, g, bl, a := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = uint8(r >> 8)
			dst.Pix[i+1] = uint8(g >> 8)
			dst.Pix[i+2] = uint8(bl >> 8)
			dst.Pix[i+3] = uint8(a >> 8)
		}
	}
	return dst
}

// ── Rendering ─────────────────────────────────────────────────────────────────

// posterSize works out the cell dimensions for a poster, preserving aspect.
// Each cell is one pixel wide and two tall, so the pixel grid is square and
// the usual terminal aspect correction isn't needed.
func posterSize(srcW, srcH, maxW, maxH int) (int, int) {
	if srcW <= 0 || srcH <= 0 {
		return 0, 0
	}
	ratio := float64(srcH) / float64(srcW)

	w := maxW
	h := int(float64(w) * ratio / 2)

	if h > maxH {
		h = maxH
		w = int(float64(h) * 2 / ratio)
	}
	return max(w, 1), max(h, 1)
}

// posterRamp shades a colourless placeholder: dark to light, so the field
// still reads as an image silhouette on a terminal without colour.
var posterRamp = []rune("░▒▓█")

// posterPlaceholder draws a deterministic gray half-block field sized to the
// poster budget. Returning "" left the info pane blank on colourless
// terminals, so something — anything — must occupy the poster's space.
func posterPlaceholder(maxW, maxH int) string {
	cw, ch := posterSize(2, 3, maxW, maxH) // typical 2:3 poster aspect
	if cw == 0 || ch == 0 {
		return ""
	}
	var sb strings.Builder
	for y := range ch {
		for x := range cw {
			sb.WriteRune(posterRamp[(x+2*y)%len(posterRamp)])
		}
		if y < ch-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// renderPoster turns an image into half-block rows.
func renderPoster(img *image.RGBA, maxW, maxH int) string {
	profile := posterColors()
	if profile == termenv.Ascii {
		return posterPlaceholder(maxW, maxH)
	}

	b := img.Bounds()
	cw, ch := posterSize(b.Dx(), b.Dy(), maxW, maxH)
	if cw == 0 || ch == 0 {
		return ""
	}

	scaled := boxScale(img, cw, ch*2)

	var sb strings.Builder
	for y := range ch {
		for x := range cw {
			t := scaled.RGBAAt(x, y*2)
			bo := scaled.RGBAAt(x, y*2+1)
			sb.WriteString(cellEscape(profile, t.R, t.G, t.B, bo.R, bo.G, bo.B))
		}
		// Reset before the newline, so a truncated row can't leak its
		// background onto whatever renders on the following line.
		sb.WriteString("\x1b[0m")
		if y < ch-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// posterImg downloads and decodes a poster, cached by URL. Shared by the
// half-block renderer below and the kitty graphics path in kitty.go.
func posterImg(url string) (*image.RGBA, bool) {
	img, ok := cachePosterImg.Get(url)
	if ok {
		return img, true
	}
	res, err := httpClient.Get(url)
	if err != nil {
		return nil, false
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, false
	}
	decoded, _, err := image.Decode(res.Body)
	if err != nil {
		return nil, false
	}
	img = toRGBA(decoded)
	cachePosterImg.Set(url, img)
	return img, true
}

// FetchPoster downloads and renders a poster. Safe to call from a goroutine.
func FetchPoster(url string, maxW, maxH int) string {
	if url == "" || !ctx.cfg.Posters {
		return ""
	}

	key := fmt.Sprintf("%s|%d|%d", url, maxW, maxH)
	if art, ok := cachePosterArt.Get(key); ok {
		return art
	}

	img, ok := posterImg(url)
	if !ok {
		return ""
	}

	art := renderPoster(img, maxW, maxH)
	cachePosterArt.Set(key, art)
	return art
}
