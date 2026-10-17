package paprika

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// MealDateLayout is the format the sync API uses for meal dates; the time portion is always midnight
const MealDateLayout = "2006-01-02 15:04:05"

type Category struct {
	UID       string `json:"uid"`
	Name      string `json:"name"`
	OrderFlag int    `json:"order_flag"`
	ParentUID string `json:"parent_uid"`
}

type CategoryList struct {
	Result []Category `json:"result"`
}

// ListCategories retrieves all recipe categories. Categories form a tree via ParentUID;
// recipes reference categories by UID.
func (c *Client) ListCategories(ctx context.Context) (*CategoryList, error) {
	var categories CategoryList
	if err := c.getResult(ctx, "https://paprikaapp.com/api/v2/sync/categories/", &categories); err != nil {
		c.logger.Error("failed to get categories", "error", err)
		return nil, fmt.Errorf("failed to get categories: %w", err)
	}

	return &categories, nil
}

// MealType is a slot in the meal planner (Breakfast, Lunch, Dinner, or a user-defined one).
// Users can add their own, so meals must be matched on TypeUID rather than on the integer Type.
type MealType struct {
	UID          string `json:"uid"`
	Name         string `json:"name"`
	OrderFlag    int    `json:"order_flag"`
	Color        string `json:"color"`
	ExportAllDay bool   `json:"export_all_day"`
	ExportTime   int    `json:"export_time"`
	OriginalType int    `json:"original_type"`
}

type MealTypeList struct {
	Result []MealType `json:"result"`
}

func (c *Client) ListMealTypes(ctx context.Context) (*MealTypeList, error) {
	var mealTypes MealTypeList
	if err := c.getResult(ctx, "https://paprikaapp.com/api/v2/sync/mealtypes/", &mealTypes); err != nil {
		c.logger.Error("failed to get meal types", "error", err)
		return nil, fmt.Errorf("failed to get meal types: %w", err)
	}

	return &mealTypes, nil
}

// Meal is an entry in the meal planner. RecipeUID is nil for free-text meals,
// in which case Name is the only description; for recipe meals Name holds the recipe name.
type Meal struct {
	UID          string  `json:"uid"`
	RecipeUID    *string `json:"recipe_uid"`
	Date         string  `json:"date"`
	Type         int     `json:"type"`
	TypeUID      string  `json:"type_uid"`
	Name         string  `json:"name"`
	OrderFlag    int     `json:"order_flag"`
	Scale        *string `json:"scale"`
	IsIngredient bool    `json:"is_ingredient"`
}

type MealList struct {
	Result []Meal `json:"result"`
}

// ListMeals retrieves every meal in the planner. The API has no date filter, so callers filter themselves.
func (c *Client) ListMeals(ctx context.Context) (*MealList, error) {
	var meals MealList
	if err := c.getResult(ctx, "https://paprikaapp.com/api/v2/sync/meals/", &meals); err != nil {
		c.logger.Error("failed to get meals", "error", err)
		return nil, fmt.Errorf("failed to get meals: %w", err)
	}

	c.logger.Info("found meals", "count", len(meals.Result))
	return &meals, nil
}

// SaveMeal creates or updates a meal. Unlike recipes, meals are written to a bulk endpoint
// that takes an array; the per-item and v1 endpoints reject writes.
func (c *Client) SaveMeal(ctx context.Context, meal Meal) (*Meal, error) {
	if meal.UID == "" {
		meal.UID = strings.ToUpper(uuid.New().String())
	}

	if err := c.postV2(ctx, "https://www.paprikaapp.com/api/v2/sync/meals/", []Meal{meal}); err != nil {
		c.logger.Error("failed to save meal", "error", err)
		return nil, fmt.Errorf("failed to save meal: %w", err)
	}

	defer c.notify(ctx)

	return &meal, nil
}
