package main

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Everything here used to be hardcoded against Cinemeta and Kitsu, which meant
// installing an addon could not add a catalog or extend search — addons only
// mattered for stream resolution. Now browsing and search are both derived
// from the installed manifests.

var (
	cacheCatalog = newCache[catalogResponse](10*time.Minute, 200)
	cacheSearch  = newCache[[]Meta](5*time.Minute, 100)
	// A SeriesMeta carries every episode's overview, so it scales with the
	// length of the show: a season of something is tens of kilobytes, a
	// thousand-episode anime is closer to half a megabyte. Bounded by both,
	// since neither number alone describes the memory.
	cacheSeries = newSizedCache(30*time.Minute, 40, 8<<20, func(sm SeriesMeta) int {
		n := len(sm.Name) + len(sm.Poster)
		for _, v := range sm.Videos {
			n += len(v.Title) + len(v.Overview) + len(v.ID) + len(v.Thumbnail) + 64
		}
		return n
	})
)

// ── Discovery ─────────────────────────────────────────────────────────────────

// Catalogs returns every browsable catalog across the installed addons, in
// addon order.
func Catalogs(addons []Addon) []CatalogRef {
	var out []CatalogRef
	for _, a := range addons {
		if a.Err != nil {
			continue
		}
		base := addonBase(a)
		if base == "" {
			continue
		}
		name := addonLabel(a)
		for _, c := range a.Manifest.Catalogs {
			if !c.Browsable() {
				continue
			}
			label := c.Name
			if label == "" {
				label = c.ID
			}
			out = append(out, CatalogRef{
				AddonName:  name,
				Base:       base,
				Type:       c.Type,
				ID:         c.ID,
				Name:       label,
				Search:     c.Supports("search"),
				Skip:       c.Supports("skip"),
				Genres:     c.Genres(),
				NeedsGenre: c.Requires("genre"),
			})
		}
	}
	return out
}

// CatalogsOfKind filters to one of the top-level menu buckets.
func CatalogsOfKind(addons []Addon, kind string) []CatalogRef {
	var out []CatalogRef
	for _, c := range Catalogs(addons) {
		if c.Kind() == kind {
			out = append(out, c)
		}
	}
	return out
}

// KindsAvailable reports which buckets actually have catalogs behind them, so
// the menu can grey out or hide the empty ones.
func KindsAvailable(addons []Addon) map[string]int {
	counts := map[string]int{}
	for _, c := range Catalogs(addons) {
		counts[c.Kind()]++
	}
	return counts
}

// SearchCatalogs returns catalogs that declare search support.
func SearchCatalogs(addons []Addon) []CatalogRef {
	var out []CatalogRef
	for _, a := range addons {
		if a.Err != nil {
			continue
		}
		base := addonBase(a)
		if base == "" {
			continue
		}
		name := addonLabel(a)
		for _, c := range a.Manifest.Catalogs {
			if !c.Supports("search") {
				continue
			}
			label := c.Name
			if label == "" {
				label = c.ID
			}
			out = append(out, CatalogRef{
				AddonName: name,
				Base:      base,
				Type:      c.Type,
				ID:        c.ID,
				Name:      label,
				Search:    true,
				Skip:      c.Supports("skip"),
			})
		}
	}
	return out
}

// ── Fetching ──────────────────────────────────────────────────────────────────

type catalogResponse struct {
	Metas   []Meta `json:"metas"`
	HasMore bool   `json:"hasMore"`
}

// sourceOf maps a catalog type onto the tag used for badges and history.
func sourceOf(catalogType string) string {
	switch catalogType {
	case "movie":
		return "movie"
	case "anime":
		return "anime"
	case "series":
		return "show"
	}
	return catalogType
}

// catalogURL builds the request, folding optional extras into the path segment
// the protocol expects: /catalog/{type}/{id}/genre=Action&skip=100.json
func catalogURL(ref CatalogRef, skip int, genre string) string {
	var extras []string
	if genre != "" {
		extras = append(extras, "genre="+url.QueryEscape(genre))
	}
	// Trust hasMore over the manifest: an addon that handed back hasMore is
	// paginating, whether or not it declared "skip". At skip=0 there's nothing
	// to send either way.
	if skip > 0 {
		extras = append(extras, fmt.Sprintf("skip=%d", skip))
	}
	if len(extras) == 0 {
		return fmt.Sprintf("%s/catalog/%s/%s.json", ref.Base, ref.Type, url.PathEscape(ref.ID))
	}
	return fmt.Sprintf("%s/catalog/%s/%s/%s.json",
		ref.Base, ref.Type, url.PathEscape(ref.ID), strings.Join(extras, "&"))
}

// FetchCatalog pulls one page. The addon's own hasMore drives pagination; a
// cached page replays the hasMore it was fetched with rather than guessing.
func FetchCatalog(ref CatalogRef, skip int, genre string) ([]Meta, bool, error) {
	u := catalogURL(ref, skip, genre)

	key := u
	if v, ok := cacheCatalog.Get(key); ok {
		return v.Metas, v.HasMore, nil
	}

	var resp catalogResponse
	if err := getJSON(u, &resp); err != nil {
		return nil, false, err
	}

	src := sourceOf(ref.Type)
	for i := range resp.Metas {
		resp.Metas[i].normalize(src, ref.Base)
	}

	cacheCatalog.Set(key, resp)
	return resp.Metas, resp.HasMore, nil
}

// Search fans out across every search-capable catalog and merges the results.
// Addon order decides precedence when the same title comes back twice.
// imdbIDRe matches a bare imdb id, or one pasted as an imdb url.
//
// Seven digits minimum with no upper bound: imdb pads with leading zeros, so
// tt0910970 and tt0000000000910970 are the same title, and ids past eight
// digits are now common. Anchoring rules out a clash with a real search term.
var imdbIDRe = regexp.MustCompile(`^(?:https?://(?:www\.)?imdb\.com/title/)?(tt\d{7,})/?$`)

// SearchByID fetches a title directly by its imdb id.
//
// Catalog search matches on name, so an id finds nothing there. The meta
// endpoint takes the id instead. It needs a type and the id does not say
// which, so both are tried and whichever answers is used.
func SearchByID(addons []Addon, id string) []Meta {
	for _, t := range []string{"series", "movie"} {
		d, ok := GetMetaDetail(addons, t, id, "")
		if !ok || d.Name == "" {
			continue
		}
		src := "movie"
		if t == "series" {
			src = "show"
		}
		return []Meta{{
			ID: id, Type: t, Name: d.Name,
			Year: d.ReleaseInfo, ReleaseInfo: d.ReleaseInfo, Source: src,
		}}
	}
	return nil
}

func Search(addons []Addon, query string) []Meta {
	if m := imdbIDRe.FindStringSubmatch(strings.TrimSpace(query)); m != nil {
		return SearchByID(addons, m[1])
	}

	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}

	refs := SearchCatalogs(addons)
	if len(refs) == 0 {
		return nil
	}

	key := strings.ToLower(q)
	if v, ok := cacheSearch.Get(key); ok {
		return v
	}

	results := make([][]Meta, len(refs))
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Add(1)
		go func(i int, ref CatalogRef) {
			defer wg.Done()
			u := fmt.Sprintf("%s/catalog/%s/%s/search=%s.json",
				ref.Base, ref.Type, url.PathEscape(ref.ID), url.QueryEscape(q))

			var resp catalogResponse
			if getJSON(u, &resp) != nil {
				return
			}
			src := sourceOf(ref.Type)
			for j := range resp.Metas {
				resp.Metas[j].normalize(src, ref.Base)
			}
			results[i] = resp.Metas
		}(i, ref)
	}
	wg.Wait()

	seen := map[string]bool{}
	var merged []Meta
	for _, batch := range results {
		for _, m := range batch {
			k := m.Type + ":" + m.ID
			if m.ID == "" || seen[k] {
				continue
			}
			seen[k] = true
			merged = append(merged, m)
		}
	}

	cacheSearch.Set(key, merged)
	return merged
}

// ── Meta ──────────────────────────────────────────────────────────────────────

// metaBases returns addon bases able to answer a meta request for this id,
// with the addon that produced the item tried first.
func metaBases(addons []Addon, mediaType, id, hint string) []string {
	var out []string
	if hint != "" {
		out = append(out, hint)
	}
	for _, a := range addons {
		if a.Err != nil || !a.SupportsResource("meta", mediaType, id) {
			continue
		}
		base := addonBase(a)
		if base == "" || base == hint {
			continue
		}
		out = append(out, base)
	}
	return out
}

var cacheMetaDetail = newCache[MetaDetail](30*time.Minute, 300)

// metaFlight dedupes concurrent meta fetches for the same title: the first
// caller does the request and callers that arrive while it is in flight wait
// for its result instead of issuing a second one.
var metaFlight = struct {
	mu    sync.Mutex
	calls map[string]*metaFlightCall
}{calls: map[string]*metaFlightCall{}}

type metaFlightCall struct {
	wg  sync.WaitGroup
	val MetaDetail
	ok  bool
}

// GetMetaDetail fetches the full meta object for one title, asking whichever
// addons declare a meta resource for that id.
func GetMetaDetail(addons []Addon, mediaType, id, hint string) (MetaDetail, bool) {
	return fetchMetaDetail(addons, mediaType, id, hint, true)
}

// fetchMetaDetail resolves a meta detail, joining an in-flight request for the
// same id. negative controls whether a total miss is remembered: prefetch
// passes false, so a transient failure on a row the user hasn't reached yet
// isn't cached and doesn't block a real attempt later.
func fetchMetaDetail(addons []Addon, mediaType, id, hint string, negative bool) (MetaDetail, bool) {
	key := mediaType + ":" + id
	if v, ok := cacheMetaDetail.Get(key); ok {
		return v, v.ID != ""
	}

	metaFlight.mu.Lock()
	if c, ok := metaFlight.calls[key]; ok {
		metaFlight.mu.Unlock()
		c.wg.Wait()
		return c.val, c.ok
	}
	c := &metaFlightCall{}
	c.wg.Add(1)
	metaFlight.calls[key] = c
	metaFlight.mu.Unlock()

	c.val, c.ok = getMetaDetail(addons, mediaType, id, hint, negative)

	// Done before the delete: a caller that finds the entry just before it is
	// removed joins an already-finished call rather than starting a duplicate.
	c.wg.Done()
	metaFlight.mu.Lock()
	delete(metaFlight.calls, key)
	metaFlight.mu.Unlock()

	return c.val, c.ok
}

// getMetaDetail performs the request behind fetchMetaDetail. Successes are
// cached. On a total miss it stores a negative entry only when negative is
// true, and never over a positive entry that arrived while it was fetching.
func getMetaDetail(addons []Addon, mediaType, id, hint string, negative bool) (MetaDetail, bool) {
	key := mediaType + ":" + id
	for _, base := range metaBases(addons, mediaType, id, hint) {
		var resp struct {
			Meta MetaDetail `json:"meta"`
		}
		u := fmt.Sprintf("%s/meta/%s/%s.json", base, mediaType, url.PathEscape(id))
		if getJSON(u, &resp) == nil && resp.Meta.Name != "" {
			cacheMetaDetail.Set(key, resp.Meta)
			return resp.Meta, true
		}
	}

	if v, ok := cacheMetaDetail.Get(key); ok && v.ID != "" {
		return v, true // a positive result landed while we were fetching
	}
	if negative {
		cacheMetaDetail.Set(key, MetaDetail{}) // negative cache, don't re-ask
	}
	return MetaDetail{}, false
}

// GetSeriesMeta fetches the episode list, asking each capable addon in turn
// rather than guessing between Cinemeta and Kitsu based on a source tag.
func GetSeriesMeta(addons []Addon, m Meta) SeriesMeta {
	if v, ok := cacheSeries.Get(m.ID); ok {
		return v
	}

	for _, base := range metaBases(addons, "series", m.ID, m.Base) {
		var resp struct {
			Meta SeriesMeta `json:"meta"`
		}
		u := fmt.Sprintf("%s/meta/series/%s.json", base, url.PathEscape(m.ID))
		if getJSON(u, &resp) != nil || len(resp.Meta.Videos) == 0 {
			continue
		}
		for i, v := range resp.Meta.Videos {
			resp.Meta.Videos[i] = v.fill()
		}

		// Kitsu files each cour as its own entry, so a long anime arrives as
		// eight separate "season 1"s. Asking the same addon for the imdb id
		// it just told us about returns the whole show with real seasons —
		// and every episode still carries its kitsu reference, so streams can
		// be requested the accurate way. See Video.StreamID.
		if id := resp.Meta.ImdbID; id != "" && id != m.ID {
			var full struct {
				Meta SeriesMeta `json:"meta"`
			}
			fu := fmt.Sprintf("%s/meta/series/%s.json", base, url.PathEscape(id))
			if getJSON(fu, &full) == nil && len(full.Meta.Videos) > len(resp.Meta.Videos) {
				for i, v := range full.Meta.Videos {
					full.Meta.Videos[i] = v.fill()
				}
				// Only when this entry is one of the numbered seasons.
				//
				// An OVA or a recap collection is filed under season 0 of the
				// merged series, along with every other special — forty-odd
				// unrelated shorts in one list. Kitsu already keeps them as
				// separate titles, which is the more useful shape, so those
				// keep their own episodes and don't merge.
				if n, ok := full.Meta.SeasonOf(kitsuIDOf(m.ID)); ok && n > 0 {
					cacheSeries.Set(m.ID, full.Meta)
					return full.Meta
				}
			}
		}

		cacheSeries.Set(m.ID, resp.Meta)
		return resp.Meta
	}

	empty := SeriesMeta{}
	cacheSeries.Set(m.ID, empty)
	return empty
}

// GetSeasonEpisodes returns sorted episodes for a season, enriched from OMDB
// for IMDB-identified shows (Cinemeta often omits episode titles).
func GetSeasonEpisodes(m Meta, season int, sm SeriesMeta, omdbKey string) []Video {
	byEp := map[int]Video{}
	for _, v := range sm.Videos {
		if v.Season == season {
			byEp[v.Episode] = v
		}
	}

	if m.Source != "anime" && strings.HasPrefix(m.ID, "tt") && omdbKey != "" {
		var resp struct {
			Episodes []struct {
				Episode  string `json:"Episode"`
				Title    string `json:"Title"`
				Released string `json:"Released"`
			} `json:"Episodes"`
		}
		u := fmt.Sprintf("%s/?i=%s&Season=%d&apikey=%s", urlOMDB, m.ID, season, omdbKey)
		if getJSON(u, &resp) == nil {
			for _, e := range resp.Episodes {
				n := 0
				fmt.Sscanf(e.Episode, "%d", &n)
				if n <= 0 {
					continue
				}
				v := byEp[n]
				if e.Title != "" && e.Title != "N/A" {
					v.Title = e.Title
				}
				if e.Released != "" && e.Released != "N/A" {
					v.Released = e.Released
				}
				byEp[n] = v
			}
		}
	}

	eps := make([]Video, 0, len(byEp))
	for _, v := range byEp {
		eps = append(eps, v)
	}
	sort.Slice(eps, func(i, j int) bool { return eps[i].Episode < eps[j].Episode })
	return eps
}
