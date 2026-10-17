package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/soggycactus/paprika-3-mcp/internal/paprika"
)

const dateLayout = "2006-01-02"

// maxListedMeals keeps list_meals responses compact when a long period is requested
const maxListedMeals = 500

func (s *Server) listMeals(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.Params.Arguments
	startDate, err := time.Parse(dateLayout, strings.TrimSpace(stringArg(args, "start_date")))
	if err != nil {
		return mcp.NewToolResultError("start_date must be a date in the format YYYY-MM-DD"), nil
	}
	endDate := startDate
	if end := strings.TrimSpace(stringArg(args, "end_date")); end != "" {
		if endDate, err = time.Parse(dateLayout, end); err != nil {
			return mcp.NewToolResultError("end_date must be a date in the format YYYY-MM-DD"), nil
		}
	}
	if endDate.Before(startDate) {
		return mcp.NewToolResultError("end_date must not be before start_date"), nil
	}

	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	meals, err := s.paprika3.ListMeals(ctx)
	if err != nil {
		return nil, err
	}
	mealTypes, err := s.paprika3.ListMealTypes(ctx)
	if err != nil {
		return nil, err
	}
	// Recipe names are only needed for meals whose stored name is empty, so a cache failure is not fatal
	if err := s.cache.ensureFresh(ctx, cacheMaxAge); err != nil {
		s.logger.Warn("listing meals without recipe cache", "err", err)
	}
	recipes, _ := s.cache.snapshot()

	return mcp.NewToolResultText(formatMeals(meals.Result, mealTypes.Result, recipes, startDate, endDate)), nil
}

// formatMeals renders the meals between start and end (inclusive) grouped per day, in meal type order
func formatMeals(meals []paprika.Meal, mealTypes []paprika.MealType, recipes []paprika.Recipe, start, end time.Time) string {
	typesByUID := map[string]paprika.MealType{}
	for _, mt := range mealTypes {
		typesByUID[mt.UID] = mt
	}
	recipeNames := map[string]string{}
	for _, r := range recipes {
		recipeNames[r.UID] = r.Name
	}

	from, to := start.Format(dateLayout), end.Format(dateLayout)
	var selected []paprika.Meal
	for _, m := range meals {
		if len(m.Date) < len(dateLayout) {
			continue
		}
		if day := m.Date[:len(dateLayout)]; day >= from && day <= to {
			selected = append(selected, m)
		}
	}

	period := from
	if to != from {
		period = fmt.Sprintf("%s to %s", from, to)
	}
	if len(selected) == 0 {
		return fmt.Sprintf("No meals planned for %s.", period)
	}

	typeOrder := func(m paprika.Meal) int {
		if mt, ok := typesByUID[m.TypeUID]; ok {
			return mt.OrderFlag
		}
		return m.Type
	}
	sort.SliceStable(selected, func(i, j int) bool {
		a, b := selected[i], selected[j]
		if a.Date[:10] != b.Date[:10] {
			return a.Date < b.Date
		}
		if typeOrder(a) != typeOrder(b) {
			return typeOrder(a) < typeOrder(b)
		}
		return a.OrderFlag < b.OrderFlag
	})

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%d meals for %s:\n", len(selected), period))
	if len(selected) > maxListedMeals {
		sb.WriteString(fmt.Sprintf("(showing the first %d; ask for a shorter period to see the rest)\n", maxListedMeals))
		selected = selected[:maxListedMeals]
	}

	currentDay := ""
	for _, m := range selected {
		day := m.Date[:len(dateLayout)]
		if day != currentDay {
			currentDay = day
			weekday := ""
			if t, err := time.Parse(dateLayout, day); err == nil {
				weekday = " (" + t.Weekday().String() + ")"
			}
			sb.WriteString(fmt.Sprintf("\n%s%s\n", day, weekday))
		}

		typeName := "Meal"
		if mt, ok := typesByUID[m.TypeUID]; ok {
			typeName = mt.Name
		}

		if m.RecipeUID != nil && *m.RecipeUID != "" {
			name := m.Name
			if current, ok := recipeNames[*m.RecipeUID]; ok {
				name = current
			}
			sb.WriteString(fmt.Sprintf("- %s: %s (recipe uid: %s)\n", typeName, name, *m.RecipeUID))
		} else {
			sb.WriteString(fmt.Sprintf("- %s: %s (free text, no recipe)\n", typeName, m.Name))
		}
	}

	return sb.String()
}

func (s *Server) addMeal(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	args := req.Params.Arguments
	date, err := time.Parse(dateLayout, strings.TrimSpace(stringArg(args, "date")))
	if err != nil {
		return mcp.NewToolResultError("date must be a date in the format YYYY-MM-DD"), nil
	}
	mealTypeName := strings.TrimSpace(stringArg(args, "meal_type"))
	if mealTypeName == "" {
		return mcp.NewToolResultError("meal_type is required"), nil
	}
	recipeRef := strings.TrimSpace(stringArg(args, "recipe"))
	freeText := strings.TrimSpace(stringArg(args, "name"))
	if (recipeRef == "") == (freeText == "") {
		return mcp.NewToolResultError("pass either recipe (an existing recipe's uid or name) or name (free text), not both"), nil
	}

	readCtx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	mealTypes, err := s.paprika3.ListMealTypes(readCtx)
	if err != nil {
		return nil, err
	}
	mealType, err := resolveMealType(mealTypes.Result, mealTypeName)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	meal := paprika.Meal{
		Date:    date.Format(paprika.MealDateLayout),
		Type:    mealType.OriginalType,
		TypeUID: mealType.UID,
		Name:    freeText,
	}
	if recipeRef != "" {
		if err := s.cache.ensureFresh(readCtx, cacheMaxAge); err != nil {
			return nil, err
		}
		recipes, _ := s.cache.snapshot()
		recipe, err := resolveRecipe(recipes, recipeRef)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		meal.RecipeUID = &recipe.UID
		meal.Name = recipe.Name
	}

	ctx, cancel = context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	saved, err := s.paprika3.SaveMeal(ctx, meal)
	if err != nil {
		return nil, err
	}

	s.logger.Info("Added meal", "name", saved.Name, "uid", saved.UID, "date", saved.Date, "type", mealType.Name, "duration", time.Since(start))

	what := fmt.Sprintf("recipe %q", saved.Name)
	if saved.RecipeUID == nil {
		what = fmt.Sprintf("%q (free text)", saved.Name)
	}
	return mcp.NewToolResultText(fmt.Sprintf("Added %s as %s on %s (%s). Meal uid: %s", what, mealType.Name, date.Format(dateLayout), date.Weekday(), saved.UID)), nil
}
