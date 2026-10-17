package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/soggycactus/paprika-3-mcp/internal/paprika"
)

// paprikaClient is the subset of the Paprika API the server uses; it lets tests swap in a fake
type paprikaClient interface {
	ListRecipes(ctx context.Context) (*paprika.RecipeList, error)
	GetRecipe(ctx context.Context, uid string) (*paprika.Recipe, error)
	SaveRecipe(ctx context.Context, recipe paprika.Recipe) (*paprika.Recipe, error)
	ListCategories(ctx context.Context) (*paprika.CategoryList, error)
	ListMealTypes(ctx context.Context) (*paprika.MealTypeList, error)
	ListMeals(ctx context.Context) (*paprika.MealList, error)
	SaveMeal(ctx context.Context, meal paprika.Meal) (*paprika.Meal, error)
}

const (
	// maxConcurrentFetches limits parallel recipe downloads; paprikaapp.com throttles when hammered
	maxConcurrentFetches = 3
	// maxFetchFailures aborts a refresh once the API is clearly throttling; the rest is fetched next time
	maxFetchFailures = 5
)

// recipeCache keeps all recipes and categories in memory. A refresh only downloads
// recipes whose hash changed since the previous refresh, so it stays cheap after the first load.
// If path is set, the cache is also stored on disk so a restart doesn't download every recipe again.
type recipeCache struct {
	client paprikaClient
	logger *slog.Logger
	path   string

	// refreshMu serializes refreshes; mu guards the cached data
	refreshMu sync.Mutex
	mu        sync.RWMutex

	recipes     map[string]paprika.Recipe // uid -> recipe, excluding recipes in the trash
	hashes      map[string]string         // uid -> hash from the recipe list, including trashed recipes
	categories  []paprika.Category
	lastRefresh time.Time
	loaded      bool
}

func newRecipeCache(client paprikaClient, logger *slog.Logger, path string) *recipeCache {
	c := &recipeCache{
		client:  client,
		logger:  logger,
		path:    path,
		recipes: map[string]paprika.Recipe{},
		hashes:  map[string]string{},
	}
	c.load()
	return c
}

// cacheFile is the on-disk format of the cache
type cacheFile struct {
	Recipes    map[string]paprika.Recipe `json:"recipes"`
	Hashes     map[string]string         `json:"hashes"`
	Categories []paprika.Category        `json:"categories"`
}

// load restores the cache from disk. The data counts as loaded but stale, so the first
// tool call still syncs with Paprika, falling back to this data if that fails.
func (c *recipeCache) load() {
	if c.path == "" {
		return
	}
	data, err := os.ReadFile(c.path)
	if err != nil {
		if !os.IsNotExist(err) {
			c.logger.Warn("failed to read recipe cache", "path", c.path, "err", err)
		}
		return
	}

	var f cacheFile
	if err := json.Unmarshal(data, &f); err != nil || f.Recipes == nil || f.Hashes == nil {
		c.logger.Warn("ignoring unreadable recipe cache", "path", c.path, "err", err)
		return
	}
	c.recipes, c.hashes, c.categories = f.Recipes, f.Hashes, f.Categories
	c.loaded = true
	c.logger.Info("loaded recipe cache from disk", "recipes", len(c.recipes), "path", c.path)
}

// save writes the cache to disk atomically; callers must hold mu
func (c *recipeCache) save() {
	if c.path == "" {
		return
	}
	data, err := json.Marshal(cacheFile{Recipes: c.recipes, Hashes: c.hashes, Categories: c.categories})
	if err == nil {
		err = os.MkdirAll(filepath.Dir(c.path), 0o700)
	}
	if err == nil {
		tmp := c.path + ".tmp"
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			err = os.Rename(tmp, c.path)
		}
	}
	if err != nil {
		c.logger.Warn("failed to write recipe cache", "path", c.path, "err", err)
	}
}

// ensureFresh refreshes the cache if it is older than maxAge. Once the cache has been loaded,
// a failed refresh is logged and the stale data is used instead.
func (c *recipeCache) ensureFresh(ctx context.Context, maxAge time.Duration) error {
	c.mu.RLock()
	fresh := c.loaded && time.Since(c.lastRefresh) < maxAge
	c.mu.RUnlock()
	if fresh {
		return nil
	}

	_, err := c.refresh(ctx, maxAge)
	if err == nil {
		return nil
	}

	c.mu.RLock()
	loaded := c.loaded
	c.mu.RUnlock()
	if loaded {
		c.logger.Warn("using stale recipe cache", "err", err)
		return nil
	}

	return fmt.Errorf("failed to load recipes from Paprika: %w", err)
}

// refresh syncs the cache with Paprika and returns the recipes that were added or changed.
// If another refresh finished less than skipIfNewerThan ago, it returns without doing anything.
func (c *recipeCache) refresh(ctx context.Context, skipIfNewerThan time.Duration) ([]paprika.Recipe, error) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	c.mu.RLock()
	skip := c.loaded && time.Since(c.lastRefresh) < skipIfNewerThan
	c.mu.RUnlock()
	if skip {
		return nil, nil
	}

	start := time.Now()
	categories, err := c.client.ListCategories(ctx)
	if err != nil {
		return nil, err
	}

	list, err := c.client.ListRecipes(ctx)
	if err != nil {
		return nil, err
	}

	c.mu.RLock()
	// toFetch maps uid -> hash from the list; the list hash is what the next refresh compares against
	toFetch := map[string]string{}
	for _, r := range list.Result {
		if c.hashes[r.UID] != r.Hash {
			toFetch[r.UID] = r.Hash
		}
	}
	c.mu.RUnlock()

	type fetched struct {
		uid    string
		hash   string
		recipe *paprika.Recipe
		err    error
	}
	results := make(chan fetched, len(toFetch))
	buffer := make(chan struct{}, maxConcurrentFetches)
	fetchCtx, abort := context.WithCancel(ctx)
	defer abort()
	var failures int
	var failuresMu sync.Mutex
	var wg sync.WaitGroup
	for uid, hash := range toFetch {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buffer <- struct{}{}
			defer func() { <-buffer }()

			if fetchCtx.Err() != nil {
				results <- fetched{uid: uid, hash: hash, err: fetchCtx.Err()}
				return
			}
			recipe, err := c.client.GetRecipe(fetchCtx, uid)
			if err != nil {
				failuresMu.Lock()
				failures++
				if failures >= maxFetchFailures {
					abort()
				}
				failuresMu.Unlock()
			}
			results <- fetched{uid: uid, hash: hash, recipe: recipe, err: err}
		}()
	}
	wg.Wait()
	close(results)

	listed := make(map[string]bool, len(list.Result))
	for _, r := range list.Result {
		listed[r.UID] = true
	}

	var changed []paprika.Recipe
	var errs []error
	c.mu.Lock()
	for f := range results {
		if f.err != nil {
			// Leave the hash untouched so the recipe is fetched again on the next refresh
			errs = append(errs, f.err)
			continue
		}
		c.hashes[f.uid] = f.hash
		if f.recipe.InTrash {
			delete(c.recipes, f.uid)
			continue
		}
		c.recipes[f.uid] = *f.recipe
		changed = append(changed, *f.recipe)
	}
	for uid := range c.hashes {
		if !listed[uid] {
			delete(c.hashes, uid)
			delete(c.recipes, uid)
		}
	}
	c.categories = categories.Result
	c.lastRefresh = time.Now()
	// The cache counts as loaded once most recipes are in; the rest are retried on the next refresh
	if len(errs) <= len(list.Result)/10 {
		c.loaded = true
	}
	c.save()
	c.mu.Unlock()

	c.logger.Info("refreshed recipe cache", "recipes", len(list.Result), "fetched", len(toFetch), "failed", len(errs), "duration", time.Since(start))

	if len(errs) > 0 {
		return changed, fmt.Errorf("failed to fetch %d of %d recipes: %w", len(errs), len(toFetch), errs[0])
	}
	return changed, nil
}

// put stores a recipe that was just saved, so it is visible before the next refresh
func (c *recipeCache) put(recipe paprika.Recipe) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recipes[recipe.UID] = recipe
	c.hashes[recipe.UID] = recipe.Hash
	c.save()
}

// snapshot returns copies of the cached recipes and categories
func (c *recipeCache) snapshot() ([]paprika.Recipe, []paprika.Category) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	recipes := make([]paprika.Recipe, 0, len(c.recipes))
	for _, r := range c.recipes {
		recipes = append(recipes, r)
	}
	categories := append([]paprika.Category(nil), c.categories...)
	return recipes, categories
}
