package main

import "sort"

// ── Index space ───────────────────────────────────────────────────────────────

// orderedEpisodes is the episode order a bitfield index maps onto: the show's
// videos sorted by season, then episode. The cached meta supplies the full
// list when the title has been browsed; otherwise only the episodes the store
// has already recorded can be addressed, because an episode never touched
// locally has no index to send or receive. Callers hold hist.mu.
func orderedEpisodes(showID string, sh *showState) [][2]int {
	if eps := epsFromMeta(showID); len(eps) > 0 {
		return eps
	}
	return epsFromStore(sh)
}

func epsFromMeta(showID string) [][2]int {
	sm, ok := cacheSeries.Get(showID)
	if !ok {
		return nil
	}
	out := make([][2]int, 0, len(sm.Videos))
	for _, v := range sm.Videos {
		if v.Episode <= 0 {
			continue
		}
		out = append(out, [2]int{v.Season, v.Episode})
	}
	sortSeEp(out)
	return out
}

func epsFromStore(sh *showState) [][2]int {
	if sh == nil {
		return nil
	}
	out := make([][2]int, 0, len(sh.Eps))
	for k := range sh.Eps {
		s, e := parseEpKey(k)
		if e <= 0 {
			continue
		}
		out = append(out, [2]int{s, e})
	}
	sortSeEp(out)
	return out
}

func sortSeEp(eps [][2]int) {
	sort.Slice(eps, func(i, j int) bool {
		if eps[i][0] != eps[j][0] {
			return eps[i][0] < eps[j][0]
		}
		return eps[i][1] < eps[j][1]
	})
}
