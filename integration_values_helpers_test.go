//go:build integration

package pgdesk_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func formValue(t *testing.T, body, name string) string {
	t.Helper()
	pats := []string{
		fmt.Sprintf(`name="%s" type="text" value="([^"]*)"`, regexp.QuoteMeta(name)),
		fmt.Sprintf(`type="hidden" name="%s" value="([^"]*)"`, regexp.QuoteMeta(name)),
		fmt.Sprintf(`(?s)<textarea[^>]*name="%s"[^>]*>(.*?)</textarea>`, regexp.QuoteMeta(name)),
		fmt.Sprintf(`name="%s"[^>]*class="pg-json">([^<]*)<`, regexp.QuoteMeta(name)),
		fmt.Sprintf(`name="%s">([^<]*)<`, regexp.QuoteMeta(name)),
	}
	for _, p := range pats {
		if m := regexp.MustCompile(p).FindStringSubmatch(body); m != nil {
			return html(m[1])
		}
	}
	t.Fatalf("no form input found for %q in:\n%s", name, body)
	return ""
}

func formRow(t *testing.T, body, name string) string {
	t.Helper()
	marker := fmt.Sprintf(`id="f_%s"`, name)
	for _, row := range strings.Split(body, `<div class="pg-form-row`)[1:] {
		if strings.Contains(row, marker) {
			return row
		}
	}
	t.Fatalf("no form row found for %q in:\n%s", name, body)
	return ""
}

var entities = regexp.MustCompile(`&(amp|quot|#34|#39|lt|gt);`)

func html(s string) string {
	return entities.ReplaceAllStringFunc(s, func(e string) string {
		switch e {
		case "&amp;":
			return "&"
		case "&quot;", "&#34;":
			return `"`
		case "&#39;":
			return "'"
		case "&lt;":
			return "<"
		case "&gt;":
			return ">"
		}
		return e
	})
}
