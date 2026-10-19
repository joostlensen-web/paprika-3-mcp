package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/soggycactus/paprika-3-mcp/internal/paprika"
)

const defaultSearchLimit = 100

func (s *Server) searchRecipes(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	query := fold(stringArg(args, "query"))
	ingredient := fold(stringArg(args, "ingredient"))
	categoryName := strings.TrimSpace(stringArg(args, "category"))
	limit := defaultSearchLimit
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	if err := s.cache.ensureFresh(ctx, cacheMaxAge); err != nil {
		return nil, err
	}
	recipes, categories := s.cache.snapshot()
	ix := newCategoryIndex(categories)

	var inCategory map[string]bool
	if categoryName != "" {
		category, ok := ix.find(categoryName)
		if !ok {
			msg := fmt.Sprintf("unknown category %q", categoryName)
			if suggestions := ix.suggest(categoryName); len(suggestions) > 0 {
				msg += fmt.Sprintf(" (did you mean: %s?)", strings.Join(suggestions, ", "))
			}
			return mcp.NewToolResultError(msg + "; use list_categories to see all categories"), nil
		}
		inCategory = ix.withDescendants(category.UID)
	}

	var matches []paprika.Recipe
	for _, r := range recipes {
		if query != "" && !strings.Contains(fold(r.Name), query) {
			continue
		}
		if ingredient != "" && !strings.Contains(fold(r.Ingredients), ingredient) {
			continue
		}
		if inCategory != nil && !hasAnyCategory(r, inCategory) {
			continue
		}
		matches = append(matches, r)
	}
	sort.Slice(matches, func(i, j int) bool { return fold(matches[i].Name) < fold(matches[j].Name) })

	if len(matches) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf("No recipes found (searched %d recipes).", len(recipes))), nil
	}

	var sb strings.Builder
	if len(matches) > limit {
		sb.WriteString(fmt.Sprintf("Found %d recipes, showing the first %d:\n", len(matches), limit))
		matches = matches[:limit]
	} else {
		sb.WriteString(fmt.Sprintf("Found %d recipes:\n", len(matches)))
	}
	for _, r := range matches {
		sb.WriteString(formatRecipeLine(r, ix))
	}
	return mcp.NewToolResultText(sb.String()), nil
}

func hasAnyCategory(r paprika.Recipe, set map[string]bool) bool {
	for _, uid := range r.Categories {
		if set[uid] {
			return true
		}
	}
	return false
}

// formatRecipeLine renders a recipe as a single compact line: name, categories, uid
func formatRecipeLine(r paprika.Recipe, ix *categoryIndex) string {
	if names := ix.names(r.Categories); len(names) > 0 {
		return fmt.Sprintf("- %s [%s] (uid: %s)\n", r.Name, strings.Join(names, ", "), r.UID)
	}
	return fmt.Sprintf("- %s (uid: %s)\n", r.Name, r.UID)
}

func (s *Server) getRecipe(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ref := strings.TrimSpace(stringArg(req.GetArguments(), "recipe"))
	if ref == "" {
		return mcp.NewToolResultError("recipe is required"), nil
	}

	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	if err := s.cache.ensureFresh(ctx, cacheMaxAge); err != nil {
		return nil, err
	}
	recipes, categories := s.cache.snapshot()

	recipe, err := resolveRecipe(recipes, ref)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(recipeMarkdown(recipe, newCategoryIndex(categories))), nil
}

func (s *Server) listCategories(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	if err := s.cache.ensureFresh(ctx, cacheMaxAge); err != nil {
		return nil, err
	}
	recipes, categories := s.cache.snapshot()
	ix := newCategoryIndex(categories)

	direct := map[string]int{}
	uncategorized := 0
	for _, r := range recipes {
		known := 0
		for _, uid := range r.Categories {
			if _, ok := ix.byUID[uid]; ok {
				direct[uid]++
				known++
			}
		}
		if known == 0 {
			uncategorized++
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%d categories, %d recipes. Counts in parentheses: recipes directly in the category, and including subcategories where that differs.\n", len(categories), len(recipes)))
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		for _, c := range ix.children[parent] {
			// A recipe in several subcategories is counted once in the parent's total
			total := 0
			set := ix.withDescendants(c.UID)
			for _, r := range recipes {
				if hasAnyCategory(r, set) {
					total++
				}
			}
			count := fmt.Sprintf("%d", direct[c.UID])
			if total != direct[c.UID] {
				count = fmt.Sprintf("%d direct, %d incl. subcategories", direct[c.UID], total)
			}
			sb.WriteString(fmt.Sprintf("%s- %s (%s)\n", strings.Repeat("  ", depth), c.Name, count))
			walk(c.UID, depth+1)
		}
	}
	walk("", 0)
	sb.WriteString(fmt.Sprintf("Recipes without a category: %d\n", uncategorized))

	return mcp.NewToolResultText(sb.String()), nil
}
