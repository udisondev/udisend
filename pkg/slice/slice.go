package slice

import (
	"bytes"
	"sort"
)

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

func ConcatWithDel(del byte, bts ...[]byte) []byte {
	out := make([]byte, 0)
	for i, bs := range bts {
		out = append(out, bs...)
		if i == len(bts)-1 {
			break
		}
		out = append(out, del)
	}

	return out
}

func SplitBy(sls []byte, spliter byte) [][]byte {
	return bytes.Split(sls, []byte{spliter})
}
