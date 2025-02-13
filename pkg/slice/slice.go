package slice

import "sort"

func RemoveIndexes[T any](s []T, indexes []int) []T {
	idxs := append([]int(nil), indexes...)
	sort.Sort(sort.Reverse(sort.IntSlice(idxs)))

	for _, idx := range idxs {
		if idx < 0 || idx >= len(s) {
			continue
		}
		s = append(s[:idx], s[idx+1:]...)
	}
	return s
}
