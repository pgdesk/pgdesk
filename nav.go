package pgdesk

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

func initial(label string) string {
	r, _ := utf8.DecodeRuneInString(strings.TrimSpace(label))
	if !unicode.IsLetter(r) {
		return "#"
	}
	return string(unicode.ToUpper(r))
}

func groupNav(items []navItem) []navGroup {
	sorted := append([]navItem(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return strings.ToLower(sorted[i].Label) < strings.ToLower(sorted[j].Label)
	})
	var groups []navGroup
	for _, it := range sorted {
		letter := initial(it.Label)
		if n := len(groups); n == 0 || groups[n-1].Letter != letter {
			groups = append(groups, navGroup{Letter: letter})
		}
		groups[len(groups)-1].Items = append(groups[len(groups)-1].Items, it)
	}
	return groups
}

func groupIndex(items []resourceNav) []indexGroup {
	sorted := append([]resourceNav(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return strings.ToLower(sorted[i].LabelPlural) < strings.ToLower(sorted[j].LabelPlural)
	})
	var groups []indexGroup
	for _, it := range sorted {
		letter := initial(it.LabelPlural)
		if n := len(groups); n == 0 || groups[n-1].Letter != letter {
			groups = append(groups, indexGroup{Letter: letter})
		}
		groups[len(groups)-1].Resources = append(groups[len(groups)-1].Resources, it)
	}
	return groups
}
