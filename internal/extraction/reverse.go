package extraction

import (
	"context"
	"path"
	"sort"
	"strings"
	"time"

	"pvfine/internal/pvf"
)

// How an entry acquired an incoming reference. The distinction decides whether a
// file is dead: something only a client-asset edge names is still loaded by the
// game, and a registry row means the content is live even when no script
// references it.
const (
	IncomingEdge     uint8 = iota // a declared edge resolved to it
	IncomingAsset                 // only a client-asset edge points here
	IncomingRegistry              // a .lst row registers it
	// IncomingList marks the list files themselves: the client loads a registry
	// by name, so a .lst is never dead content even though nothing points at it.
	IncomingList
)

// Incoming is one reference pointing at an entry, stored backwards. Only the
// provenance is kept: the literal value is recovered by re-reading the source,
// which is what lets a whole-archive index stay tens of megabytes instead of
// hundreds.
type Incoming struct {
	From int32
	// Name is the interned edge id, or "registry:<relation>" for a list row.
	Name int32
	Kind uint8
}

type rawLink struct {
	to, from, name int32
	kind           uint8
}

// ReverseIndex answers "who points at this entry" over a scanned universe. It is
// built from a scan pass, not a database: measured on 86, a full pass is ~13 s at
// ~55k entries/s, which is fast enough for an explicit action.
type ReverseIndex struct {
	archive   *pvf.Archive
	incoming  map[int32][]Incoming
	names     []string
	nameIndex map[string]int32
	// universe keeps the entries the index was built over, so attribution can
	// rescan exactly the same set instead of assuming the whole archive.
	universe []int32

	// Universe counts scanned entries; RawLinks counts links seen and Refs
	// counts links after provenance deduplication. Their ratio is how much of
	// the archive's reference volume is the same fact repeated.
	Universe    int
	Entries     int
	RawLinks    int
	Refs        int
	Failures    int
	ScanElapsed time.Duration
}

// BytesEstimate approximates the index's own footprint: 12 bytes per stored link
// plus one map bucket per referenced entry. Quoting the process heap instead
// would mislead, because a full-archive pass is dominated by the archive's own
// chunk cache.
func (r *ReverseIndex) BytesEstimate() int64 {
	return int64(r.Refs)*12 + int64(r.Entries)*48 + int64(len(r.names))*32
}

func (r *ReverseIndex) intern(name string) int32 {
	if at, ok := r.nameIndex[name]; ok {
		return at
	}
	at := int32(len(r.names))
	r.names = append(r.names, name)
	r.nameIndex[name] = at
	return at
}

// EdgeNames lists the interned names, index-aligned with Incoming.Name.
func (r *ReverseIndex) EdgeNames() []string { return append([]string(nil), r.names...) }

// EdgeName resolves one interned name.
func (r *ReverseIndex) EdgeName(id int32) string {
	if id < 0 || int(id) >= len(r.names) {
		return "?"
	}
	return r.names[id]
}

// BuildReverseIndex scans the given entries and records every resolved reference
// backwards. Warm the scanner first: the parallel scan shares its basename index
// and lazy registries.
func BuildReverseIndex(ctx context.Context, scanner *Scanner, indexes []int32, workers int) (*ReverseIndex, error) {
	reverse := &ReverseIndex{
		archive:   scanner.archive,
		incoming:  make(map[int32][]Incoming, len(indexes)/4),
		nameIndex: make(map[string]int32, 64),
		Universe:  len(indexes),
		universe:  indexes,
	}
	links := make([]rawLink, 0, 1<<20)
	started := time.Now()
	stats, err := scanner.ScanIndices(ctx, indexes, workers, func(result ScanResult) {
		for _, ref := range result.Refs {
			if ref.State != StateResolved || ref.Target < 0 {
				continue
			}
			kind := IncomingEdge
			if ref.Kind == TargetAsset {
				kind = IncomingAsset
			}
			links = append(links, rawLink{to: ref.Target, from: result.FileIndex,
				name: reverse.intern(ref.EdgeID), kind: kind})
		}
	})
	if err != nil {
		return nil, err
	}
	reverse.ScanElapsed = time.Since(started)
	reverse.Failures = stats.Failed

	// 登记行也算"有人要它"：怪物表里的 .mob 没有任何脚本引用也要随包发布，
	// 把它当孤儿等于把正常内容判成死内容。
	registers := scanner.RegistrySet()
	for _, relation := range registers.Relations() {
		index, err := registers.PathIndex(relation)
		if err != nil {
			continue
		}
		for registered := range index {
			at, ok := reverse.archive.Find(registered)
			if !ok {
				continue
			}
			links = append(links, rawLink{to: at, from: -1,
				name: reverse.intern("registry:" + relation), kind: IncomingRegistry})
		}
	}
	// 登记表自己也标成"被引用"：客户端按名字加载 .lst，把它列成孤儿是假警报。
	for _, listPath := range registers.ListFiles() {
		if at, ok := reverse.archive.Find(listPath); ok {
			links = append(links, rawLink{to: at, from: -1,
				name: reverse.intern("list:" + listPath), kind: IncomingList})
		}
	}

	sort.Slice(links, func(i, j int) bool {
		a, b := links[i], links[j]
		if a.to != b.to {
			return a.to < b.to
		}
		if a.from != b.from {
			return a.from < b.from
		}
		if a.name != b.name {
			return a.name < b.name
		}
		return a.kind < b.kind
	})
	reverse.RawLinks = len(links)
	for i := range links {
		link := links[i]
		if i > 0 && links[i-1] == link {
			continue // 同一来源经同一边重复指向同一目标，是一次事实而不是多次
		}
		reverse.incoming[link.to] = append(reverse.incoming[link.to],
			Incoming{From: link.from, Name: link.name, Kind: link.kind})
		reverse.Refs++
	}
	links = nil
	reverse.Entries = len(reverse.incoming)
	return reverse, nil
}

// Indexes lists the entries the index was built over.
func (r *ReverseIndex) Indexes() []int32 { return r.universe }

// Incoming returns the references pointing at one entry, ordered by source.
func (r *ReverseIndex) Incoming(index int32) []Incoming { return r.incoming[index] }

// Referenced reports whether anything points at the entry.
func (r *ReverseIndex) Referenced(index int32) bool { return len(r.incoming[index]) > 0 }

// Hit is one transitive ancestor: who pulls the start in, and how far away.
type Hit struct {
	Index int32
	Depth int
	Via   Incoming
}

// Ancestors walks the reverse graph: everything that reaches this entry. Depth 1
// means "references it directly". This is the answer to "what breaks if I change
// or delete this file".
func (r *ReverseIndex) Ancestors(index int32, maxDepth int) []Hit {
	visited := map[int32]int{index: 0}
	out := []Hit{}
	frontier := []int32{index}
	for depth := 1; len(frontier) > 0; depth++ {
		if maxDepth > 0 && depth > maxDepth {
			break
		}
		var next []int32
		for _, at := range frontier {
			for _, incoming := range r.incoming[at] {
				if incoming.From < 0 {
					continue // registry rows have no source entry to walk from
				}
				if seen, ok := visited[incoming.From]; ok && seen <= depth {
					continue
				}
				visited[incoming.From] = depth
				out = append(out, Hit{Index: incoming.From, Depth: depth, Via: incoming})
				next = append(next, incoming.From)
			}
		}
		frontier = next
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Depth != out[j].Depth {
			return out[i].Depth < out[j].Depth
		}
		return out[i].Index < out[j].Index
	})
	return out
}

// Impact intersects the ancestors of each changed entry with a caller's seed set,
// so a preset can ask "which of my presets does this edit touch".
func (r *ReverseIndex) Impact(changed []int32, seeds []int32) map[int32][]int32 {
	seedSet := map[int32]bool{}
	for _, seed := range seeds {
		seedSet[seed] = true
	}
	out := map[int32][]int32{}
	for _, index := range changed {
		touched := []int32{}
		seen := map[int32]bool{}
		for _, hit := range r.Ancestors(index, 0) {
			if !seedSet[hit.Index] || seen[hit.Index] {
				continue
			}
			seen[hit.Index] = true
			touched = append(touched, hit.Index)
		}
		out[index] = touched
	}
	return out
}

// OrphanRow is one extension's share of the unreferenced content.
type OrphanRow struct {
	Extension string
	Count     int
	Bytes     int64
}

// Orphans lists entries nothing points at — not a resolved edge, not a registry
// row. Entries only client assets point at are counted separately by AssetOnly,
// because those are still loaded.
func (r *ReverseIndex) Orphans() ([]int32, []OrphanRow) {
	byExt := map[string]*OrphanRow{}
	orphans := []int32{}
	for index := int32(0); index < r.archive.FileCount(); index++ {
		if r.Referenced(index) {
			continue
		}
		full := r.archive.FullPath(index)
		ext := strings.ToLower(path.Ext(full))
		row, ok := byExt[ext]
		if !ok {
			row = &OrphanRow{Extension: ext}
			byExt[ext] = row
		}
		row.Count++
		row.Bytes += int64(r.archive.File(index).DataSize)
		orphans = append(orphans, index)
	}
	rows := make([]OrphanRow, 0, len(byExt))
	for _, row := range byExt {
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		return rows[i].Extension < rows[j].Extension
	})
	return orphans, rows
}

// AssetOnly lists entries that nothing but a client-asset edge points at: real
// content, but outside every text closure.
func (r *ReverseIndex) AssetOnly() []int32 {
	out := []int32{}
	for index, links := range r.incoming {
		onlyAsset := len(links) > 0
		for _, link := range links {
			if link.Kind != IncomingAsset {
				onlyAsset = false
				break
			}
		}
		if onlyAsset {
			out = append(out, index)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Explain renders one incoming link as a report line: who points here, via which
// edge, and with what literal value. The value is recovered by re-reading the
// source entry rather than stored in the index.
func (r *ReverseIndex) Explain(scanner *Scanner, target int32, link Incoming) string {
	name := r.EdgeName(link.Name)
	if link.Kind == IncomingRegistry {
		return name + "（登记行，无脚本引用）"
	}
	if link.Kind == IncomingList {
		return name + "（登记表本体，客户端按名字加载）"
	}
	if link.From < 0 {
		return "? --" + name + "-->"
	}
	who := r.archive.FullPath(link.From)
	value := ""
	if result, err := scanner.ScanFile(link.From); err == nil {
		for _, ref := range result.Refs {
			if ref.Target != target || ref.EdgeID != name {
				continue
			}
			if (ref.Kind == TargetAsset) != (link.Kind == IncomingAsset) {
				continue
			}
			value = ref.Value
			break
		}
	}
	if value == "" {
		return who + " --" + name + "-->"
	}
	return who + " --" + name + "--> " + clip(value)
}
