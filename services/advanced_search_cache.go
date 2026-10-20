package services

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"

	"pvfine/internal/appdirs"
	"pvfine/internal/pvf"
)

func advancedIndexFiles(shards int) []string {
	names := []string{"index.db"}
	for shard := 0; shard < shards; shard++ {
		names = append(names, advancedShardName(shard))
	}
	return names
}

// Publish all files with one atomic directory rename. Readers can never mix
// shards from concurrent builders or see a half-written index. Hard links avoid
// copying gigabytes and let active sessions survive eviction of their cache.
func (d *advancedSQLite) publishCache(root string) error {
	finishIO := holdCachePaths(root)
	defer finishIO()
	d.cacheReleases = append(d.cacheReleases, holdCachePaths(d.cachePath))
	staging, err := os.MkdirTemp(root, "publish-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	for _, name := range advancedIndexFiles(d.shards) {
		var err error
		if name == "index.db" && d.mutable {
			err = backupSQLiteDatabase(d.requestContext(), d.db, filepath.Join(staging, name))
		} else {
			err = os.Link(filepath.Join(d.dir, name), filepath.Join(staging, name))
		}
		if err != nil {
			return err
		}
	}
	if err := d.ctx.Err(); err != nil {
		return err
	}
	return os.Rename(staging, d.cachePath)
}

func advancedDBValid(ctx context.Context, db *sql.DB) bool {
	var check string
	return db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&check) == nil && check == "ok"
}

func (d *advancedSQLite) restoreCache() (valid bool) {
	sessionPath := filepath.Join(d.dir, "index.db")
	if err := os.Link(filepath.Join(d.cachePath, "index.db"), sessionPath); err != nil {
		// A cache directory without its manifest cannot be reused or published
		// over; discard it so the completed replacement can be installed.
		if d.ctx.Err() == nil {
			_ = os.RemoveAll(d.cachePath)
		}
		return false
	}
	linked := []string{sessionPath}
	var db *sql.DB
	defer func() {
		if !valid {
			if db != nil {
				db.Close()
			}
			for _, path := range linked {
				os.Remove(path)
			}
			if d.ctx.Err() == nil {
				os.RemoveAll(d.cachePath)
			}
		}
	}()
	var err error
	db, err = openAdvancedReadDB(sessionPath)
	if err != nil {
		return false
	}
	var version, shards int
	if db.QueryRowContext(d.ctx, `SELECT version,shards FROM meta`).Scan(&version, &shards) != nil || version != 4 || shards < 1 || shards > advancedMaxShards || !advancedDBValid(d.ctx, db) {
		return false
	}
	for shard := 0; shard < shards; shard++ {
		name := advancedShardName(shard)
		path := filepath.Join(d.dir, name)
		if os.Link(filepath.Join(d.cachePath, name), path) != nil {
			return false
		}
		linked = append(linked, path)
	}
	checks := make(chan bool, shards)
	for shard := 0; shard < shards; shard++ {
		go func(shard int) {
			refDB, err := openAdvancedReadDB(filepath.Join(d.dir, advancedShardName(shard)))
			if err != nil {
				checks <- false
				return
			}
			var version, id int
			ok := refDB.QueryRowContext(d.ctx, `SELECT version,shard FROM meta`).Scan(&version, &id) == nil && version == 3 && id == shard && advancedDBValid(d.ctx, refDB)
			refDB.Close()
			checks <- ok
		}(shard)
	}
	ok := true
	for shard := 0; shard < shards; shard++ {
		ok = <-checks && ok
	}
	if !ok || d.ctx.Err() != nil {
		return false
	}
	d.db, d.shards = db, shards
	_ = db.QueryRowContext(d.ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='file_map')`).Scan(&d.overlay)
	now := time.Now()
	_ = os.Chtimes(d.cachePath, now, now)
	return true
}

// A saved, fully synchronized overlay can be reused on reopen. The writable
// main database is snapshotted; unchanged reference shards remain hard linked.
// Caller holds d.mu, never core.mu while publishing disk data.
func (d *advancedSQLite) publishCurrentCache(c *core, a *pvf.Archive) {
	if !d.overlay || d.cachePath != "" {
		return
	}
	c.mu.RLock()
	if c.archive != a || c.advancedDisk != d || d.epoch != d.indexedEpoch || d.requestContext().Err() != nil {
		c.mu.RUnlock()
		return
	}
	digest, clean := a.SavedContentHash()
	c.mu.RUnlock()
	if !clean {
		return
	}
	root, err := appdirs.CacheDirIn("advanced-search")
	if err != nil {
		return
	}
	d.cachePath = filepath.Join(root, "refs-v4-"+digest)
	if err = d.publishCache(root); err != nil && !os.IsExist(err) {
		d.cachePath = ""
		return
	}
	requestCacheMaintenance()
}

func advancedIndexDirectorySize(dir string) int64 {
	entries, _ := os.ReadDir(dir)
	var size int64
	for _, entry := range entries {
		if !entry.IsDir() {
			if info, err := entry.Info(); err == nil {
				size += info.Size()
			}
		}
	}
	return size
}
