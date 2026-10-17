package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/soggycactus/paprika-3-mcp/internal/paprika"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClient is an in-memory Paprika account
type fakeClient struct {
	mu         sync.Mutex
	recipes    map[string]paprika.Recipe
	categories []paprika.Category
	mealTypes  []paprika.MealType
	meals      []paprika.Meal

	getCalls    []string
	savedMeals  []paprika.Meal
	savedRecipe []paprika.Recipe
	failGet     map[string]bool
}

func (f *fakeClient) ListRecipes(ctx context.Context) (*paprika.RecipeList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var list paprika.RecipeList
	for _, r := range f.recipes {
		list.Result = append(list.Result, struct {
			UID  string `json:"uid"`
			Hash string `json:"hash"`
		}{UID: r.UID, Hash: r.Hash})
	}
	return &list, nil
}

func (f *fakeClient) GetRecipe(ctx context.Context, uid string) (*paprika.Recipe, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls = append(f.getCalls, uid)
	if f.failGet[uid] {
		return nil, errors.New("connection reset by peer")
	}
	r, ok := f.recipes[uid]
	if !ok {
		return nil, errors.New("not found")
	}
	return &r, nil
}

func (f *fakeClient) SaveRecipe(ctx context.Context, recipe paprika.Recipe) (*paprika.Recipe, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if recipe.UID == "" {
		recipe.UID = "NEW-RECIPE"
	}
	f.savedRecipe = append(f.savedRecipe, recipe)
	f.recipes[recipe.UID] = recipe
	return &recipe, nil
}

func (f *fakeClient) ListCategories(ctx context.Context) (*paprika.CategoryList, error) {
	return &paprika.CategoryList{Result: f.categories}, nil
}

func (f *fakeClient) ListMealTypes(ctx context.Context) (*paprika.MealTypeList, error) {
	return &paprika.MealTypeList{Result: f.mealTypes}, nil
}

func (f *fakeClient) ListMeals(ctx context.Context) (*paprika.MealList, error) {
	return &paprika.MealList{Result: f.meals}, nil
}

func (f *fakeClient) SaveMeal(ctx context.Context, meal paprika.Meal) (*paprika.Meal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	meal.UID = "NEW-MEAL"
	f.savedMeals = append(f.savedMeals, meal)
	return &meal, nil
}

func strPtr(s string) *string { return &s }

// newFakeAccount mirrors the shape of a real account: nested categories and a custom meal type
func newFakeAccount() *fakeClient {
	return &fakeClient{
		categories: []paprika.Category{
			{UID: "C-DISH", Name: "Soort gerecht"},
			{UID: "C-PASTA", Name: "Pasta", ParentUID: "C-DISH", OrderFlag: 1},
			{UID: "C-SOUP", Name: "Soep", ParentUID: "C-DISH", OrderFlag: 0},
			{UID: "C-OVEN", Name: "Ovenschotel", ParentUID: "C-PASTA"},
			{UID: "C-VEGA", Name: "Vegetarisch"},
		},
		recipes: map[string]paprika.Recipe{
			"R1": {UID: "R1", Hash: "h1", Name: "Pasta met prei", Ingredients: "250 g pasta\n2 preien\nroom", Categories: []string{"C-PASTA", "C-VEGA"}},
			"R2": {UID: "R2", Hash: "h2", Name: "Preisoep", Ingredients: "3 Preien\nbouillon", Categories: []string{"C-SOUP"}},
			"R3": {UID: "R3", Hash: "h3", Name: "Lasagne", Ingredients: "lasagnebladen\ngehakt", Categories: []string{"C-OVEN"}},
			"R4": {UID: "R4", Hash: "h4", Name: "Crème brûlée", Ingredients: "room\neieren"},
			"R5": {UID: "R5", Hash: "h5", Name: "Oude soep", Ingredients: "prei", InTrash: true},
		},
		mealTypes: []paprika.MealType{
			{UID: "T-DINNER", Name: "Dinner", OrderFlag: 2, OriginalType: 2},
			{UID: "T-LUNCH", Name: "Lunch", OrderFlag: 1, OriginalType: 1},
			{UID: "T-BREAKFAST", Name: "Breakfast", OrderFlag: 0, OriginalType: 0},
			{UID: "T-DESSERT", Name: "Nagerecht", OrderFlag: 4, OriginalType: 0},
		},
		meals: []paprika.Meal{
			{UID: "M1", RecipeUID: strPtr("R1"), Date: "2025-03-10 00:00:00", Type: 2, TypeUID: "T-DINNER", Name: "Old name"},
			{UID: "M2", Date: "2025-03-10 00:00:00", Type: 0, TypeUID: "T-BREAKFAST", Name: "Yoghurt"},
			{UID: "M3", RecipeUID: strPtr("R4"), Date: "2025-03-11 00:00:00", Type: 0, TypeUID: "T-DESSERT", Name: "Crème brûlée"},
			{UID: "M4", Date: "2025-03-12 00:00:00", Type: 2, TypeUID: "T-DINNER", Name: "Uit eten"},
		},
	}
}

func newTestServer(client *fakeClient) *Server {
	return newServer(client, "test", slog.New(slog.NewTextHandler(io.Discard, nil)), "")
}

func callTool(t *testing.T, handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error), args map[string]interface{}) (string, bool) {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Arguments = args
	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)

	var sb strings.Builder
	for _, c := range result.Content {
		switch c := c.(type) {
		case mcp.TextContent:
			sb.WriteString(c.Text)
		case mcp.EmbeddedResource:
			sb.WriteString(c.Resource.(mcp.TextResourceContents).Text)
		}
	}
	return sb.String(), result.IsError
}

func TestSearchRecipes(t *testing.T) {
	s := newTestServer(newFakeAccount())

	t.Run("by ingredient, case and accent insensitive, skipping the trash", func(t *testing.T) {
		out, isErr := callTool(t, s.searchRecipes, map[string]interface{}{"ingredient": "PREI"})
		assert.False(t, isErr)
		assert.Contains(t, out, "Found 2 recipes")
		assert.Contains(t, out, "- Pasta met prei [Pasta, Vegetarisch] (uid: R1)")
		assert.Contains(t, out, "- Preisoep [Soep] (uid: R2)")
		assert.NotContains(t, out, "Oude soep")
	})

	t.Run("by category includes subcategories", func(t *testing.T) {
		out, _ := callTool(t, s.searchRecipes, map[string]interface{}{"category": "pasta"})
		assert.Contains(t, out, "Found 2 recipes")
		assert.Contains(t, out, "Pasta met prei")
		assert.Contains(t, out, "- Lasagne [Ovenschotel] (uid: R3)")

		out, _ = callTool(t, s.searchRecipes, map[string]interface{}{"category": "Soort gerecht"})
		assert.Contains(t, out, "Found 3 recipes")
	})

	t.Run("filters combine", func(t *testing.T) {
		out, _ := callTool(t, s.searchRecipes, map[string]interface{}{"category": "Soort gerecht", "ingredient": "prei", "query": "soep"})
		assert.Contains(t, out, "Found 1 recipes")
		assert.Contains(t, out, "Preisoep")
	})

	t.Run("by name with accents", func(t *testing.T) {
		out, _ := callTool(t, s.searchRecipes, map[string]interface{}{"query": "creme brulee"})
		assert.Contains(t, out, "- Crème brûlée (uid: R4)")
	})

	t.Run("unknown category suggests close matches", func(t *testing.T) {
		out, isErr := callTool(t, s.searchRecipes, map[string]interface{}{"category": "soe"})
		assert.True(t, isErr)
		assert.Contains(t, out, "did you mean: Soep")
	})

	t.Run("limit", func(t *testing.T) {
		out, _ := callTool(t, s.searchRecipes, map[string]interface{}{"limit": float64(1)})
		assert.Contains(t, out, "Found 4 recipes, showing the first 1")
	})
}

func TestGetRecipe(t *testing.T) {
	s := newTestServer(newFakeAccount())

	out, isErr := callTool(t, s.getRecipe, map[string]interface{}{"recipe": "lasagne"})
	assert.False(t, isErr)
	assert.Contains(t, out, "# Lasagne")
	assert.Contains(t, out, "- lasagnebladen")
	assert.Contains(t, out, "**Categories:** Ovenschotel")
	assert.Contains(t, out, "**UID:** R3")

	out, isErr = callTool(t, s.getRecipe, map[string]interface{}{"recipe": "R2"})
	assert.False(t, isErr)
	assert.Contains(t, out, "# Preisoep")

	out, isErr = callTool(t, s.getRecipe, map[string]interface{}{"recipe": "p"})
	assert.True(t, isErr)
	assert.Contains(t, out, "recipes match")
}

func TestListCategories(t *testing.T) {
	s := newTestServer(newFakeAccount())

	out, _ := callTool(t, s.listCategories, nil)
	assert.Contains(t, out, "- Soort gerecht (0 direct, 3 incl. subcategories)\n  - Soep (1)\n  - Pasta (1 direct, 2 incl. subcategories)\n    - Ovenschotel (1)\n")
	assert.Contains(t, out, "- Vegetarisch (1)")
	assert.Contains(t, out, "Recipes without a category: 1")
}

func TestListMeals(t *testing.T) {
	s := newTestServer(newFakeAccount())

	out, isErr := callTool(t, s.listMeals, map[string]interface{}{"start_date": "2025-03-10", "end_date": "2025-03-11"})
	assert.False(t, isErr)
	assert.Contains(t, out, "3 meals for 2025-03-10 to 2025-03-11")
	assert.Contains(t, out, "2025-03-10 (Monday)\n- Breakfast: Yoghurt (free text, no recipe)\n- Dinner: Pasta met prei (recipe uid: R1)\n")
	assert.Contains(t, out, "2025-03-11 (Tuesday)\n- Nagerecht: Crème brûlée (recipe uid: R4)")
	assert.NotContains(t, out, "Uit eten")

	out, _ = callTool(t, s.listMeals, map[string]interface{}{"start_date": "2025-03-12"})
	assert.Contains(t, out, "- Dinner: Uit eten (free text, no recipe)")

	out, _ = callTool(t, s.listMeals, map[string]interface{}{"start_date": "2030-01-01"})
	assert.Contains(t, out, "No meals planned for 2030-01-01")

	_, isErr = callTool(t, s.listMeals, map[string]interface{}{"start_date": "10-03-2025"})
	assert.True(t, isErr)
}

func TestAddMeal(t *testing.T) {
	t.Run("recipe by name with a Dutch meal type", func(t *testing.T) {
		client := newFakeAccount()
		s := newTestServer(client)

		out, isErr := callTool(t, s.addMeal, map[string]interface{}{"date": "2025-04-01", "meal_type": "diner", "recipe": "preisoep"})
		assert.False(t, isErr, out)
		assert.Contains(t, out, `Added recipe "Preisoep" as Dinner on 2025-04-01 (Tuesday)`)

		require.Len(t, client.savedMeals, 1)
		meal := client.savedMeals[0]
		assert.Equal(t, "R2", *meal.RecipeUID)
		assert.Equal(t, "Preisoep", meal.Name)
		assert.Equal(t, "2025-04-01 00:00:00", meal.Date)
		assert.Equal(t, "T-DINNER", meal.TypeUID)
		assert.Equal(t, 2, meal.Type)
	})

	t.Run("free text on a custom meal type", func(t *testing.T) {
		client := newFakeAccount()
		s := newTestServer(client)

		_, isErr := callTool(t, s.addMeal, map[string]interface{}{"date": "2025-04-01", "meal_type": "dessert", "name": "Ijsje"})
		assert.False(t, isErr)
		require.Len(t, client.savedMeals, 1)
		assert.Nil(t, client.savedMeals[0].RecipeUID)
		assert.Equal(t, "Ijsje", client.savedMeals[0].Name)
		assert.Equal(t, "T-DESSERT", client.savedMeals[0].TypeUID)
		assert.Equal(t, 0, client.savedMeals[0].Type)
	})

	t.Run("rejects ambiguous recipes, unknown meal types and bad input without saving", func(t *testing.T) {
		client := newFakeAccount()
		s := newTestServer(client)

		out, isErr := callTool(t, s.addMeal, map[string]interface{}{"date": "2025-04-01", "meal_type": "dinner", "recipe": "prei"})
		assert.True(t, isErr)
		assert.Contains(t, out, "2 recipes match")

		out, isErr = callTool(t, s.addMeal, map[string]interface{}{"date": "2025-04-01", "meal_type": "brunch", "name": "x"})
		assert.True(t, isErr)
		assert.Contains(t, out, "available meal types: Breakfast, Lunch, Dinner, Nagerecht")

		_, isErr = callTool(t, s.addMeal, map[string]interface{}{"date": "2025-04-01", "meal_type": "dinner", "recipe": "R1", "name": "x"})
		assert.True(t, isErr)

		_, isErr = callTool(t, s.addMeal, map[string]interface{}{"date": "april", "meal_type": "dinner", "name": "x"})
		assert.True(t, isErr)

		assert.Empty(t, client.savedMeals)
	})
}

func TestCreateRecipe(t *testing.T) {
	t.Run("only required arguments does not panic (issue #4)", func(t *testing.T) {
		client := newFakeAccount()
		s := newTestServer(client)

		_, isErr := callTool(t, s.createRecipe, map[string]interface{}{"name": "Soep", "ingredients": "water", "directions": "koken"})
		assert.False(t, isErr)
		require.Len(t, client.savedRecipe, 1)
		assert.Equal(t, []string{}, client.savedRecipe[0].Categories)
	})

	t.Run("categories are linked by name", func(t *testing.T) {
		client := newFakeAccount()
		s := newTestServer(client)

		out, isErr := callTool(t, s.createRecipe, map[string]interface{}{"name": "Ravioli", "ingredients": "pasta", "directions": "koken", "categories": []interface{}{"pasta", "Vegetarisch"}})
		assert.False(t, isErr)
		assert.Contains(t, out, "**Categories:** Pasta, Vegetarisch")
		require.Len(t, client.savedRecipe, 1)
		assert.Equal(t, []string{"C-PASTA", "C-VEGA"}, client.savedRecipe[0].Categories)

		// The new recipe is searchable straight away
		out, _ = callTool(t, s.searchRecipes, map[string]interface{}{"query": "ravioli"})
		assert.Contains(t, out, "Ravioli [Pasta, Vegetarisch]")
	})

	t.Run("unknown categories are never created and nothing is saved", func(t *testing.T) {
		client := newFakeAccount()
		s := newTestServer(client)

		out, isErr := callTool(t, s.createRecipe, map[string]interface{}{"name": "Ravioli", "ingredients": "pasta", "directions": "koken", "categories": []interface{}{"Pasta", "Italiaans"}})
		assert.True(t, isErr)
		assert.Contains(t, out, `unknown categories: "Italiaans"`)
		assert.Contains(t, out, "never created")
		assert.Empty(t, client.savedRecipe)
	})
}

func TestUpdateRecipeKeepsExistingFields(t *testing.T) {
	client := newFakeAccount()
	r := client.recipes["R1"]
	r.Rating = 5
	r.Photo = "photo.jpg"
	r.Created = "2020-01-01 10:00:00"
	client.recipes["R1"] = r
	s := newTestServer(client)

	_, isErr := callTool(t, s.updateRecipe, map[string]interface{}{"uid": "R1", "notes": "Lekker"})
	assert.False(t, isErr)

	require.Len(t, client.savedRecipe, 1)
	saved := client.savedRecipe[0]
	assert.Equal(t, "Lekker", saved.Notes)
	assert.Equal(t, "Pasta met prei", saved.Name)
	assert.Equal(t, []string{"C-PASTA", "C-VEGA"}, saved.Categories)
	assert.Equal(t, 5, saved.Rating)
	assert.Equal(t, "photo.jpg", saved.Photo)
	assert.Equal(t, "2020-01-01 10:00:00", saved.Created)
}

func TestRecipeCacheOnlyFetchesChangedRecipes(t *testing.T) {
	client := newFakeAccount()
	cache := newRecipeCache(client, slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	ctx := context.Background()

	changed, err := cache.refresh(ctx, 0)
	require.NoError(t, err)
	assert.Len(t, changed, 4) // the trashed recipe is not cached
	assert.Len(t, client.getCalls, 5)

	client.getCalls = nil
	_, err = cache.refresh(ctx, 0)
	require.NoError(t, err)
	assert.Empty(t, client.getCalls)

	r := client.recipes["R2"]
	r.Hash = "h2-changed"
	r.Name = "Preisoep deluxe"
	client.recipes["R2"] = r
	delete(client.recipes, "R3")
	changed, err = cache.refresh(ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"R2"}, client.getCalls)
	require.Len(t, changed, 1)
	assert.Equal(t, "Preisoep deluxe", changed[0].Name)

	recipes, _ := cache.snapshot()
	assert.Len(t, recipes, 3)

	// Fresh enough: no refresh at all
	client.getCalls = nil
	require.NoError(t, cache.ensureFresh(ctx, time.Hour))
	assert.Empty(t, client.getCalls)
}

func TestRecipeCacheRetriesFailedFetches(t *testing.T) {
	client := newFakeAccount()
	client.failGet = map[string]bool{"R1": true}
	cache := newRecipeCache(client, slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	ctx := context.Background()

	_, err := cache.refresh(ctx, 0)
	assert.Error(t, err)

	client.failGet = nil
	client.getCalls = nil
	_, err = cache.refresh(ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"R1"}, client.getCalls)
}

func TestRecipeCachePersistsToDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "recipes.json")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := newFakeAccount()
	ctx := context.Background()

	_, err := newRecipeCache(client, logger, path).refresh(ctx, 0)
	require.NoError(t, err)

	// A new process only fetches what changed since the cache was written
	client.getCalls = nil
	r := client.recipes["R1"]
	r.Hash = "h1-changed"
	client.recipes["R1"] = r
	restarted := newRecipeCache(client, logger, path)
	recipes, categories := restarted.snapshot()
	assert.Len(t, recipes, 4)
	assert.Len(t, categories, 5)

	require.NoError(t, restarted.ensureFresh(ctx, time.Hour))
	assert.Equal(t, []string{"R1"}, client.getCalls)
}

func TestRecipeCacheAbortsWhenThrottled(t *testing.T) {
	client := newFakeAccount()
	client.failGet = map[string]bool{}
	for i := 0; i < 50; i++ {
		uid := fmt.Sprintf("X%d", i)
		client.recipes[uid] = paprika.Recipe{UID: uid, Hash: uid, Name: uid}
		client.failGet[uid] = true
	}
	cache := newRecipeCache(client, slog.New(slog.NewTextHandler(io.Discard, nil)), "")

	_, err := cache.refresh(context.Background(), 0)
	assert.Error(t, err)
	// Requests stop shortly after maxFetchFailures instead of trying all 55 recipes
	assert.Less(t, len(client.getCalls), maxFetchFailures+maxConcurrentFetches+1)
}
