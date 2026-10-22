package web

import (
	"context"
	"log/slog"
	"sync"

	"github.com/escorbuto-petoruti/eve-markets/internal/store"
)

const (
	// maxTypeFills bounds how many unknown types one request resolves through
	// ESI; the rest are filled by later requests.
	maxTypeFills = 200
	// typeFillWorkers bounds the concurrent ESI calls of one fill.
	typeFillWorkers = 8
)

// haulVolume is the volume of one unit when hauled: the packaged volume when
// the item has one, else its plain volume.
func haulVolume(m store.TypeMeta) float64 {
	if m.PackagedVolume > 0 {
		return m.PackagedVolume
	}
	return m.Volume
}

// typeMetas returns the cached metadata of ids, first filling the cache from
// public ESI for the ids it lacks. Metadata is decoration: any failure is
// logged and the affected ids are simply absent from the result.
func (s *server) typeMetas(ctx context.Context, ids []int64) map[int64]store.TypeMeta {
	metas, err := s.deps.Store.TypeMetas(ctx, ids)
	if err != nil {
		slog.Warn("read type metadata", "err", err)
		return nil
	}
	var missing []int64
	for _, id := range ids {
		if _, ok := metas[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return metas
	}
	if len(missing) > maxTypeFills {
		missing = missing[:maxTypeFills]
	}
	groups := newLookupCache(func(id int64) (name string, parent int64, err error) {
		g, err := s.deps.Structures.UniverseGroup(ctx, id)
		return g.Name, g.CategoryID, err
	})
	categories := newLookupCache(func(id int64) (name string, parent int64, err error) {
		c, err := s.deps.Structures.UniverseCategory(ctx, id)
		return c.Name, 0, err
	})
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	work := make(chan int64)
	for range typeFillWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range work {
				m, err := s.fetchTypeMeta(ctx, id, groups, categories)
				if err != nil {
					slog.Warn("fetch type metadata", "type_id", id, "err", err)
					continue
				}
				if err := s.deps.Store.UpsertTypeMeta(ctx, m); err != nil {
					slog.Warn("cache type metadata", "type_id", id, "err", err)
					continue
				}
				mu.Lock()
				metas[id] = m
				mu.Unlock()
			}
		}()
	}
	for _, id := range missing {
		work <- id
	}
	close(work)
	wg.Wait()
	return metas
}

func (s *server) fetchTypeMeta(ctx context.Context, id int64, groups, categories *lookupCache) (store.TypeMeta, error) {
	t, err := s.deps.Structures.UniverseType(ctx, id)
	if err != nil {
		return store.TypeMeta{}, err
	}
	groupName, categoryID, err := groups.get(t.GroupID)
	if err != nil {
		return store.TypeMeta{}, err
	}
	categoryName, _, err := categories.get(categoryID)
	if err != nil {
		return store.TypeMeta{}, err
	}
	return store.TypeMeta{TypeID: id, Name: t.Name, GroupID: t.GroupID, GroupName: groupName,
		CategoryID: categoryID, CategoryName: categoryName, Volume: t.Volume, PackagedVolume: t.PackagedVolume}, nil
}

// lookupCache memoizes the successful answers of one group or category
// lookup for the duration of a fill.
type lookupCache struct {
	fetch func(id int64) (name string, parent int64, err error)
	mu    sync.Mutex
	seen  map[int64]lookupEntry
}

type lookupEntry struct {
	name   string
	parent int64
}

func newLookupCache(fetch func(id int64) (string, int64, error)) *lookupCache {
	return &lookupCache{fetch: fetch, seen: map[int64]lookupEntry{}}
}

func (c *lookupCache) get(id int64) (string, int64, error) {
	c.mu.Lock()
	e, ok := c.seen[id]
	c.mu.Unlock()
	if ok {
		return e.name, e.parent, nil
	}
	name, parent, err := c.fetch(id)
	if err != nil {
		return "", 0, err
	}
	c.mu.Lock()
	c.seen[id] = lookupEntry{name, parent}
	c.mu.Unlock()
	return name, parent, nil
}
