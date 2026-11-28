package types

import (
	"cmp"
	"slices"
)

// RecentSnapshots returns at most n offers ordered by descending height and format.
func RecentSnapshots(offers []*Snapshot, n uint32) []*Snapshot {
	sorted := slices.DeleteFunc(slices.Clone(offers), func(s *Snapshot) bool { return s == nil })
	slices.SortStableFunc(sorted, func(a, b *Snapshot) int {
		if h := cmp.Compare(b.Height, a.Height); h != 0 {
			return h
		}
		return cmp.Compare(b.Format, a.Format)
	})
	return sorted[:min(uint64(n), uint64(len(sorted)))]
}
