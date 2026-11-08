package manifest

import "strings"

// Category is one section of the application menu (decision 0025).
type Category struct {
	ID    string `json:"id"`    // the token a manifest writes: lowercase, no spaces
	Label string `json:"label"` // what a menu shows
}

// Categories is the closed vocabulary, in display order: a category's index is
// its sort position, and the order the controller sends. Adding a category is
// one line here. There is deliberately no "system" entry: that section holds
// programs the compositor starts, which no manifest can claim.
var Categories = []Category{
	{"files", "Files"},
	{"media", "Media"},
	{"development", "Development"},
	{"utilities", "Utilities"},
	{"haiku", "Haiku"},
}

// KnownCategory reports whether id is in the vocabulary.
func KnownCategory(id string) bool {
	for _, c := range Categories {
		if c.ID == id {
			return true
		}
	}
	return false
}

// categoryIDs is the vocabulary as an error message lists it.
func categoryIDs() string {
	ids := make([]string, len(Categories))
	for i, c := range Categories {
		ids[i] = c.ID
	}
	return strings.Join(ids, ", ")
}
