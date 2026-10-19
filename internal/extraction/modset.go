package extraction

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"

	"pvfine/internal/pvf"
)

// DiffVerdicts is what the MOD changed relative to the baseline. The caller gets
// it from CompareEntries, because deciding "changed" needs the token-level
// comparison and that is the diff's job, not this one's.
type DiffVerdicts struct {
	// Modified lists paths present in both whose content differs, plus paths only
	// the MOD has (those are new files).
	Modified []string
	// Deleted lists paths the baseline has and the MOD dropped.
	Deleted []string
}

// PackageOptions bounds a plan. Checking deletions costs a second, whole-archive
// reverse pass over the baseline, so it is opt-in.
type PackageOptions struct {
	// Workers is the scan concurrency; 0 means one.
	Workers int
	// ReportBrokenReferences walks the baseline graph to find files that still
	// reference something the MOD deletes. Those files are not shipped by the MOD,
	// so the answer is an advisory list, not something to package.
	ReportBrokenReferences bool
	// MaxCarry caps the closure, so a mistaken whole-archive change set does not
	// silently ask for the entire game.
	MaxCarry int
	// Scope keeps the plan to entries under these path fragments.
	Scope []string
}

// PackagePlan answers "what must the MOD carry, and what will dangle once it is
// applied". It is the reference-closure counterpart to the existing export
// dependency pass, which by design only completes text tables, table-number
// mappings and list rows — never scripts, sprites or sounds.
type PackagePlan struct {
	// Carry is the changed set plus everything those entries transitively need,
	// resolved inside the MOD archive.
	Carry []string
	// Dependencies is Carry minus the changed entries.
	Dependencies []string
	// SatisfiedByBaseline names references the MOD cannot resolve but the baseline
	// can: the MOD relies on the host archive having them, which is a real
	// compatibility claim and must be shown, not quietly packaged or dropped.
	SatisfiedByBaseline []SatisfiedRef
	// Unresolvable names references that fail in both archives.
	Unresolvable []SatisfiedRef
	// BrokenReferences names baseline files that still point at something the MOD
	// deletes. Only present when ReportBrokenReferences is set.
	BrokenReferences []string
	// ByCategory is the carry set counted per export category, so the preview can
	// offer the same per-category selection the extraction UI uses.
	ByCategory map[string]int
	Bytes      int64
	// Seeds counts the changed entries that actually exist in the MOD.
	Seeds int
	// CarryTruncated says PackageOptions.MaxCarry stopped the walk, so Carry is a
	// sample rather than a complete package. A capped plan must never read like a
	// closed one.
	CarryTruncated bool
	// Dropped counts reachable entries the cap left unexpanded.
	Dropped int
	// Closure is the walk that produced Carry, kept so a plan is exportable rather
	// than only readable: Selection{Paths: plan.Carry} over this closure is the
	// package, and the exporter's own completion/strict rules then apply.
	Closure *Closure
}

// SatisfiedRef is one reference the MOD cannot resolve on its own.
type SatisfiedRef struct {
	From   string
	EdgeID string
	Value  string
	Target string
	// InBaseline says the same path exists in the host archive.
	InBaseline bool
}

// PlanPackage computes the minimal carry set for a MOD against a baseline.
func PlanPackage(baseline, mod *pvf.Archive, modScanner, baselineScanner *Scanner, verdicts DiffVerdicts, options PackageOptions) (*PackagePlan, error) {
	if options.Workers < 1 {
		options.Workers = 1
	}
	plan := &PackagePlan{ByCategory: map[string]int{}}
	seeds := make([]int32, 0, len(verdicts.Modified))
	modified := map[string]bool{}
	for _, entryPath := range ScopePaths(verdicts.Modified, options.Scope) {
		index, ok := mod.Find(entryPath)
		if !ok {
			continue
		}
		seeds = append(seeds, index)
		modified[entryPath] = true
	}
	plan.Seeds = len(seeds)
	if len(seeds) == 0 {
		return plan, nil
	}
	closure, err := modScanner.Closure(seeds, Options{
		MaxEntries: options.MaxCarry, CollectUnmodeled: false,
	})
	if err != nil {
		return nil, err
	}
	carry := make([]string, 0, len(closure.Nodes))
	for _, node := range closure.Nodes {
		carry = append(carry, node.Path)
		if raw, rawErr := mod.RawBytes(node.Index); rawErr == nil {
			plan.Bytes += int64(len(raw))
		}
		plan.ByCategory[modScanner.Catalog().ClassifyNode(node).ID]++
	}
	sort.Strings(carry)
	plan.Carry = carry
	plan.Closure = closure
	plan.CarryTruncated = closure.Truncated
	plan.Dropped = closure.Dropped
	for _, entryPath := range carry {
		if !modified[entryPath] {
			plan.Dependencies = append(plan.Dependencies, entryPath)
		}
	}

	baselineHas := func(candidate string) bool {
		if candidate == "" {
			return false
		}
		_, ok := baseline.Find(candidate)
		return ok
	}
	// 逐条目重扫带来源：closure.Unresolved 不带"是谁引用的"，而"这条引用来自哪个
	// 文件、能否由基线满足"正是这份计划要回答的问题。
	seenRef := map[string]bool{}
	for _, node := range closure.Nodes {
		result, err := modScanner.ScanFile(node.Index)
		if err != nil {
			continue
		}
		for _, ref := range result.Refs {
			if ref.State == StateResolved || ref.Kind == TargetAsset || ref.TargetPath == "" {
				continue
			}
			target := ref.TargetPath
			inBaseline := baselineHas(target)
			key := strings.Join([]string{node.Path, ref.EdgeID, target}, "|")
			if seenRef[key] {
				continue
			}
			seenRef[key] = true
			row := SatisfiedRef{From: node.Path, EdgeID: ref.EdgeID,
				Value: clip(ref.Value), Target: target, InBaseline: inBaseline}
			if inBaseline {
				plan.SatisfiedByBaseline = append(plan.SatisfiedByBaseline, row)
			} else {
				plan.Unresolvable = append(plan.Unresolvable, row)
			}
		}
	}
	if options.ReportBrokenReferences && len(verdicts.Deleted) > 0 {
		broken, err := brokenReferences(context.Background(), baseline, mod, baselineScanner, verdicts.Deleted, options)
		if err != nil {
			return nil, err
		}
		plan.BrokenReferences = broken
	}
	return plan, nil
}

// errNoBaselineScanner names the missing input for the deletion check, which is
// the only place a plan needs a second graph.
var errNoBaselineScanner = errors.New("extraction: 检查被删条目的引用方需要基线扫描器")

// brokenReferences builds a baseline reverse index and reports which baseline
// files still point at entries the MOD removes. Those files are not in the carry
// set, so applying the MOD leaves them dangling — the one failure mode a MOD
// author cannot see by looking at their own archive.
func brokenReferences(ctx context.Context, baseline, mod *pvf.Archive, baselineScanner *Scanner, deleted []string, options PackageOptions) ([]string, error) {
	removed := map[int32]string{}
	for _, entryPath := range deleted {
		index, ok := baseline.Find(entryPath)
		if !ok {
			continue
		}
		removed[index] = entryPath
	}
	if len(removed) == 0 {
		return nil, nil
	}
	if baselineScanner == nil {
		return nil, errNoBaselineScanner
	}
	indexes := make([]int32, 0, baseline.FileCount())
	for index := int32(0); index < baseline.FileCount(); index++ {
		indexes = append(indexes, index)
	}
	reverse, err := BuildReverseIndex(ctx, baselineScanner, indexes, options.Workers)
	if err != nil {
		return nil, err
	}
	out := []string{}
	seen := map[string]bool{}
	for index := range removed {
		for _, link := range reverse.Incoming(index) {
			if link.From < 0 {
				continue
			}
			// 只有"MOD 仍保留这个引用方、却删掉了被引用项"才算破坏：
			// 引用方自己也一起删掉的话，问题不存在。
			source := baseline.FullPath(link.From)
			if _, stillThere := mod.Find(source); !stillThere {
				continue
			}
			if seen[source] {
				continue
			}
			seen[source] = true
			out = append(out, source)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ExtCount is one extension's share of the carry set.
type ExtCount struct {
	Extension string
	Count     int
}

// ExtensionTotals counts the carry set by extension, biggest first.
func (p *PackagePlan) ExtensionTotals() []ExtCount {
	counts := map[string]int{}
	for _, entryPath := range p.Carry {
		counts[strings.ToLower(path.Ext(entryPath))]++
	}
	out := make([]ExtCount, 0, len(counts))
	for ext, count := range counts {
		out = append(out, ExtCount{Extension: ext, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Extension < out[j].Extension
	})
	return out
}
