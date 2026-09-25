package service

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

var errEmptyAllergens = errors.New("at least one valid allergen is required")

// normalizeAllergenList applies the same trim/dedupe/case-insensitive-key
// rules used when persisting profiles, so a what-if preview and the eventual
// saved profile describe the identical allergen set.
func normalizeAllergenList(items []string) ([]string, error) {
	seen := make(map[string]string)
	for _, item := range items {
		clean := strings.TrimSpace(item)
		if clean == "" {
			continue
		}
		key := strings.ToLower(clean)
		if _, exists := seen[key]; !exists {
			seen[key] = clean
		}
	}
	if len(seen) == 0 {
		return nil, errEmptyAllergens
	}
	result := make([]string, 0, len(seen))
	for _, value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

// allergenDelta returns the allergens the proposed set introduces and drops,
// compared against the persisted normalized set, using lower-case keys but
// preserving the proposed/persisted spellings.
func allergenDelta(current, proposed []string) ImpactAllergenDelta {
	currentMap := make(map[string]string, len(current))
	for _, item := range current {
		currentMap[strings.ToLower(item)] = item
	}
	proposedMap := make(map[string]string, len(proposed))
	for _, item := range proposed {
		proposedMap[strings.ToLower(item)] = item
	}
	delta := ImpactAllergenDelta{Added: []string{}, Removed: []string{}}
	for key, value := range proposedMap {
		if _, exists := currentMap[key]; !exists {
			delta.Added = append(delta.Added, value)
		}
	}
	for key, value := range currentMap {
		if _, exists := proposedMap[key]; !exists {
			delta.Removed = append(delta.Removed, value)
		}
	}
	sort.Strings(delta.Added)
	sort.Strings(delta.Removed)
	return delta
}

func unmarshalAllergensJSON(raw []byte, target *[]string) error {
	return json.Unmarshal(raw, target)
}
