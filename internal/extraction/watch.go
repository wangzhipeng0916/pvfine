package extraction

import (
	"encoding/binary"
	"hash/fnv"
	"sort"

	"pvfine/internal/pvf"
)

// Watch is a content witness for the archive entries a preview was built from.
// Neither of the cheap signals lasts: the mutation journal is consumed by
// whichever service schedules an index refresh first, and a save empties the
// pending-edit overlay, so an edit can go invisible while a cached preview is
// still sitting there promising the pre-edit set. The bytes cannot.
type Watch struct {
	FileCount int32
	Entries   int32
	Bytes     int64
	Digest    uint64
}

// WatchEntries hashes identity, path and payload of the given entries. Indexes
// are deduplicated, visited in index order, and the archive's entry count joins
// the digest, so the result describes archive content only and never depends on
// how the caller collected the indexes.
func WatchEntries(a *pvf.Archive, indexes []int32) Watch {
	total := a.FileCount()
	seen := make(map[int32]bool, len(indexes))
	list := make([]int32, 0, len(indexes))
	for _, index := range indexes {
		if index < 0 || index >= total || seen[index] {
			continue
		}
		seen[index] = true
		list = append(list, index)
	}
	sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })

	h := fnv.New64a()
	var scratch [8]byte
	put := func(v uint64) {
		binary.LittleEndian.PutUint64(scratch[:], v)
		_, _ = h.Write(scratch[:])
	}
	put(uint64(total))
	watch := Watch{FileCount: total}
	for _, index := range list {
		put(a.FileIdentity(index))
		raw, err := a.RawBytes(index)
		if err != nil {
			// An unreadable payload is a fact about the archive too, so it joins
			// the digest as a marker instead of aborting the witness.
			put(^uint64(0))
			continue
		}
		path := a.FullPath(index)
		put(uint64(len(path)))
		_, _ = h.Write([]byte(path))
		put(uint64(len(raw)))
		_, _ = h.Write(raw)
		watch.Entries++
		watch.Bytes += int64(len(raw))
	}
	watch.Digest = h.Sum64()
	return watch
}
