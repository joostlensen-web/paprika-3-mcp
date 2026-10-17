package mcpserver

import (
	"fmt"
	"sort"
	"strings"

	"github.com/soggycactus/paprika-3-mcp/internal/paprika"
)

var accentReplacer = strings.NewReplacer(
	"á", "a", "à", "a", "ä", "a", "â", "a", "ã", "a", "å", "a",
	"é", "e", "è", "e", "ë", "e", "ê", "e",
	"í", "i", "ì", "i", "ï", "i", "î", "i",
	"ó", "o", "ò", "o", "ö", "o", "ô", "o", "õ", "o",
	"ú", "u", "ù", "u", "ü", "u", "û", "u",
	"ç", "c", "ñ", "n", "ß", "ss",
)

// fold normalizes text for matching: case-insensitive, accent-insensitive and trimmed
func fold(s string) string {
	return accentReplacer.Replace(strings.ToLower(strings.TrimSpace(s)))
}

// categoryIndex makes the category tree searchable by name and walkable by parent
type categoryIndex struct {
	byUID    map[string]paprika.Category
	children map[string][]paprika.Category
}

func newCategoryIndex(categories []paprika.Category) *categoryIndex {
	ix := &categoryIndex{
		byUID:    make(map[string]paprika.Category, len(categories)),
		children: map[string][]paprika.Category{},
	}
	for _, c := range categories {
		ix.byUID[c.UID] = c
	}
	for _, c := range categories {
		parent := c.ParentUID
		if _, ok := ix.byUID[parent]; !ok {
			parent = ""
		}
		ix.children[parent] = append(ix.children[parent], c)
	}
	for _, siblings := range ix.children {
		sort.Slice(siblings, func(i, j int) bool {
			if siblings[i].OrderFlag != siblings[j].OrderFlag {
				return siblings[i].OrderFlag < siblings[j].OrderFlag
			}
			return fold(siblings[i].Name) < fold(siblings[j].Name)
		})
	}
	return ix
}

// find returns the category with exactly this name, ignoring case and accents
func (ix *categoryIndex) find(name string) (paprika.Category, bool) {
	for _, c := range ix.byUID {
		if fold(c.Name) == fold(name) {
			return c, true
		}
	}
	return paprika.Category{}, false
}

// suggest returns category names that partially match name, for error messages
func (ix *categoryIndex) suggest(name string) []string {
	n := fold(name)
	var out []string
	for _, c := range ix.byUID {
		cn := fold(c.Name)
		if n != "" && (strings.Contains(cn, n) || strings.Contains(n, cn)) {
			out = append(out, c.Name)
		}
	}
	sort.Strings(out)
	return out
}

// withDescendants returns the set containing uid and all categories below it
func (ix *categoryIndex) withDescendants(uid string) map[string]bool {
	set := map[string]bool{uid: true}
	queue := []string{uid}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		for _, child := range ix.children[next] {
			if !set[child.UID] {
				set[child.UID] = true
				queue = append(queue, child.UID)
			}
		}
	}
	return set
}

// names maps category uids to names, skipping uids that no longer exist
func (ix *categoryIndex) names(uids []string) []string {
	out := make([]string, 0, len(uids))
	for _, uid := range uids {
		if c, ok := ix.byUID[uid]; ok {
			out = append(out, c.Name)
		}
	}
	sort.Strings(out)
	return out
}

// resolveCategories maps category names to uids. Unknown names are an error: categories are never created.
func (ix *categoryIndex) resolveCategories(names []string) ([]string, error) {
	uids := []string{}
	var unknown []string
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			continue
		}
		c, ok := ix.find(name)
		if !ok {
			msg := fmt.Sprintf("%q", name)
			if s := ix.suggest(name); len(s) > 0 {
				msg += fmt.Sprintf(" (did you mean: %s?)", strings.Join(s, ", "))
			}
			unknown = append(unknown, msg)
			continue
		}
		uids = append(uids, c.UID)
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown categories: %s. Categories are never created by this server; use list_categories to see the existing ones", strings.Join(unknown, "; "))
	}
	return uids, nil
}

// resolveRecipe finds a recipe by uid or name. A name matches exactly (ignoring case and accents),
// or as a substring if exactly one recipe contains it. Anything else is an error listing the candidates.
func resolveRecipe(recipes []paprika.Recipe, ref string) (paprika.Recipe, error) {
	ref = strings.TrimSpace(ref)
	for _, r := range recipes {
		if strings.EqualFold(r.UID, ref) {
			return r, nil
		}
	}

	var exact, partial []paprika.Recipe
	for _, r := range recipes {
		switch name := fold(r.Name); {
		case name == fold(ref):
			exact = append(exact, r)
		case strings.Contains(name, fold(ref)):
			partial = append(partial, r)
		}
	}

	candidates := exact
	if len(candidates) == 0 {
		candidates = partial
	}
	switch len(candidates) {
	case 0:
		return paprika.Recipe{}, fmt.Errorf("no recipe found matching %q; use search_recipes to find it", ref)
	case 1:
		return candidates[0], nil
	}

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Name < candidates[j].Name })
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%d recipes match %q; pass the uid of the one you mean:\n", len(candidates), ref))
	for i, r := range candidates {
		if i == 20 {
			sb.WriteString(fmt.Sprintf("... and %d more\n", len(candidates)-i))
			break
		}
		sb.WriteString(fmt.Sprintf("- %s (uid: %s)\n", r.Name, r.UID))
	}
	return paprika.Recipe{}, fmt.Errorf("%s", sb.String())
}

// mealTypeAliases maps common (Dutch and English) names to alternatives a meal type might be called in Paprika
var mealTypeAliases = map[string][]string{
	"ontbijt":       {"breakfast"},
	"breakfast":     {"ontbijt"},
	"lunch":         {"middageten"},
	"middageten":    {"lunch"},
	"diner":         {"dinner", "avondeten"},
	"avondeten":     {"dinner", "diner"},
	"dinner":        {"diner", "avondeten"},
	"snack":         {"snacks", "tussendoortje"},
	"snacks":        {"snack", "tussendoortje"},
	"tussendoortje": {"snacks", "snack"},
	"dessert":       {"nagerecht", "toetje"},
	"nagerecht":     {"dessert", "toetje"},
	"toetje":        {"nagerecht", "dessert"},
}

// resolveMealType finds the user's meal type by name, accepting Dutch and English aliases
func resolveMealType(mealTypes []paprika.MealType, name string) (paprika.MealType, error) {
	candidates := append([]string{fold(name)}, mealTypeAliases[fold(name)]...)
	for _, candidate := range candidates {
		for _, mt := range mealTypes {
			if fold(mt.Name) == candidate {
				return mt, nil
			}
		}
	}

	available := make([]string, 0, len(mealTypes))
	for _, mt := range sortedMealTypes(mealTypes) {
		available = append(available, mt.Name)
	}
	return paprika.MealType{}, fmt.Errorf("unknown meal type %q; available meal types: %s", name, strings.Join(available, ", "))
}

func sortedMealTypes(mealTypes []paprika.MealType) []paprika.MealType {
	out := append([]paprika.MealType(nil), mealTypes...)
	sort.Slice(out, func(i, j int) bool { return out[i].OrderFlag < out[j].OrderFlag })
	return out
}
