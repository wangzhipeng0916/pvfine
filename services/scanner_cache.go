package services

import (
	"sync"

	appconfig "pvfine/config"
	"pvfine/internal/annotations"
	"pvfine/internal/extraction"
	"pvfine/internal/pvf"
)

// relationSpecsOf takes the relation table from the merged annotation document,
// so a user override applies to extraction and to the annotation engine alike.
// The shipped config/lists.json is missing aicharacter/aicharacter.lst even
// though 86 has it; the same supplement the CLI harness uses keeps the two paths
// identical.
func relationSpecsOf(c *core) (map[string]annotations.RelationSpec, error) {
	specs := map[string]annotations.RelationSpec{}
	if c.ruleStore != nil {
		snap, err := c.ruleStore.Load("annotations")
		if err != nil {
			return nil, err
		}
		engine, _, err := snap.Engines("annotations")
		if err != nil {
			return nil, err
		}
		if engine != nil {
			for name, spec := range engine.Document().Relations {
				specs[name] = spec
			}
		}
	}
	if len(specs) == 0 {
		lists, err := annotations.ParseLists(appconfig.ListsJSON)
		if err != nil {
			return nil, err
		}
		for name, spec := range lists.Relations {
			specs[name] = spec
		}
	}
	if _, ok := specs["APC"]; !ok {
		specs["APC"] = annotations.RelationSpec{
			ListPath: "aicharacter/aicharacter.lst", IDToken: 0, PathToken: 1,
			RecordTokens: 2, NameSection: "name",
		}
	}
	return specs, nil
}

// contentWitness hashes archive content while no writer can be inside it, which
// is the only way a witness stays comparable.
func contentWitness(c *core, archive *pvf.Archive, indexes []int32) extraction.Watch {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return extraction.WatchEntries(archive, indexes)
}

// witnessFunc is supplied by the caller rather than chosen by the cache, because
// the core read lock is already held on some paths (MOD export runs the whole
// Prepare under it). Re-taking it there is a self-deadlock: RWMutex blocks a
// recursive RLock as soon as a writer is waiting.
type witnessFunc func(archive *pvf.Archive, indexes []int32) extraction.Watch

// unlockedWitness is for callers that already hold at least the core read lock.
func unlockedWitness() witnessFunc {
	return func(archive *pvf.Archive, indexes []int32) extraction.Watch {
		return extraction.WatchEntries(archive, indexes)
	}
}

// coreWitness is for callers that hold no core lock at all.
func coreWitness(c *core) witnessFunc {
	return func(archive *pvf.Archive, indexes []int32) extraction.Watch {
		return contentWitness(c, archive, indexes)
	}
}

// scannerCache holds one warmed scanner keyed by everything its internals cache:
// the archive pointer and generation (structural changes), the rule engine
// identity (the relation table it resolves with is loaded from the document that
// was activated), and a content witness over the registry lists plus section
// indexes, because a .lst edit changes neither of the first two while it changes
// every answer the cached registries would give.
//
// Extraction and MOD export both need this. Duplicating the key rules in two
// places is how one of them drifts stale, so there is exactly one implementation.
type scannerCache struct {
	mu         sync.Mutex
	scanner    *extraction.Scanner
	archive    *pvf.Archive
	generation uint64
	rules      *annotations.Engine
	watched    []int32
	digest     extraction.Watch
}

// forArchive returns the cached scanner or rebuilds it. onRebuild, when given,
// runs under the cache lock right after a fresh scanner replaces the old one,
// which is the moment a caller's derived previews stop being valid. The cache
// never takes the core lock itself: c is only used to read the relation table
// (ruleStore has its own locking), and the caller supplies `witness` so the lock
// discipline stays where the lock actually is held.
func (sc *scannerCache) forArchive(c *core, archive *pvf.Archive, generation uint64,
	rules *annotations.Engine, witness witnessFunc, onRebuild func()) (*extraction.Scanner, error) {
	sc.mu.Lock()
	cached, same := sc.scanner, sc.archive == archive && sc.generation == generation && sc.rules == rules
	watched, digest := sc.watched, sc.digest
	sc.mu.Unlock()
	if cached != nil && same && witness(archive, watched) == digest {
		return cached, nil
	}
	catalog, err := extraction.LoadCatalog(extraction.EmbeddedEdges)
	if err != nil {
		return nil, err
	}
	specs, err := relationSpecsOf(c)
	if err != nil {
		return nil, err
	}
	registrySet := extraction.NewRegistrySet(archive, specs)
	scanner := extraction.NewScanner(archive, catalog, registrySet)
	if err := scanner.Prewarm(nil); err != nil {
		return nil, err
	}
	lists := registrySet.ListFiles()
	// A section index (e.g. etc/hellparty.etc) is not a registry, but the group
	// table cached inside the scanner comes from its bytes, so an edit to it has
	// to rebuild the scanner just like a .lst change does.
	for _, indexPath := range catalog.SectionIndexPaths() {
		lists = append(lists, indexPath)
	}
	indexes := make([]int32, 0, len(lists))
	for _, listPath := range lists {
		if index, ok := archive.Find(listPath); ok {
			indexes = append(indexes, index)
		}
	}
	digest = witness(archive, indexes)
	sc.mu.Lock()
	sc.scanner, sc.archive, sc.generation, sc.rules = scanner, archive, generation, rules
	sc.watched, sc.digest = indexes, digest
	if onRebuild != nil {
		onRebuild()
	}
	sc.mu.Unlock()
	return scanner, nil
}

// registryWitness lists the entries whose content the cached scanner depends on,
// so a caller can include them in the witness of a derived result.
func (sc *scannerCache) registryWitness() []int32 {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	out := make([]int32, len(sc.watched))
	copy(out, sc.watched)
	return out
}
