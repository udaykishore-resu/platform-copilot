package vectorstore

import "sort"

// RRF fuses several ranked lists with Reciprocal Rank Fusion:
//
//	score(d) = Σ_lists 1 / (k + rank_list(d))
//
// RRF needs no score calibration between lists (cosine in [0,1] vs BM25 in
// [0,∞)), which is why it beats naive weighted sums in practice. k = 60 is the
// value from the original paper (Cormack et al., 2009) and is rarely tuned.
// The fused Score on each Hit is the RRF score; the original per-list scores
// are recorded in Metadata as "score_<list>" for the --explain flag.
func RRF(k int, lists ...[]Hit) []Hit {
	if k <= 0 {
		k = 60
	}
	type agg struct {
		hit   Hit
		score float32
	}
	byID := map[string]*agg{}
	names := []string{"vector", "bm25", "list3", "list4"}
	for li, list := range lists {
		for rank, h := range list {
			a, ok := byID[h.ID]
			if !ok {
				hc := h
				hc.Metadata = copyMeta(h.Metadata)
				a = &agg{hit: hc}
				byID[h.ID] = a
			}
			a.score += 1 / float32(k+rank+1)
			if li < len(names) {
				a.hit.Metadata["score_"+names[li]] = ftoa(h.Score)
			}
		}
	}
	out := make([]Hit, 0, len(byID))
	for _, a := range byID {
		a.hit.Score = a.score
		out = append(out, a.hit)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].ID < out[j].ID
		}
		return out[i].Score > out[j].Score
	})
	return out
}

func copyMeta(m map[string]string) map[string]string {
	out := make(map[string]string, len(m)+2)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func ftoa(f float32) string {
	// small, allocation-light float formatter (4 decimals)
	neg := f < 0
	if neg {
		f = -f
	}
	i := int(f)
	frac := int((f - float32(i)) * 10000)
	s := itoa(i) + "." + pad4(frac)
	if neg {
		return "-" + s
	}
	return s
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

func pad4(i int) string {
	s := itoa(i)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}
