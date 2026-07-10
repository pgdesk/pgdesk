package pgdesk

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDateRangeLabel(t *testing.T) {
	cases := []struct{ from, to, want string }{
		{"2026-01-01", "2026-02-01", "2026-01-01 → 2026-02-01"},
		{"2026-01-01", "", "≥ 2026-01-01"},
		{"", "2026-02-01", "≤ 2026-02-01"},
	}
	for _, c := range cases {
		if got := dateRangeLabel(c.from, c.to); got != c.want {
			t.Errorf("dateRangeLabel(%q, %q) = %q, want %q", c.from, c.to, got, c.want)
		}
	}
}

func TestSplitFilters(t *testing.T) {
	mk := func(n int) []filterField {
		out := make([]filterField, n)
		for i := range out {
			out[i] = filterField{Label: "f"}
		}
		return out
	}
	cases := []struct{ total, wantInline, wantOverflow int }{
		{0, 0, 0},
		{4, 4, 0},                // at the threshold: no overflow disclosure
		{5, listFilterPinned, 2}, // just over: pin 3, spill the rest
		{9, listFilterPinned, 9 - listFilterPinned},
	}
	for _, c := range cases {
		inline, overflow := splitFilters(mk(c.total))
		if len(inline) != c.wantInline || len(overflow) != c.wantOverflow {
			t.Errorf("splitFilters(%d) => inline=%d overflow=%d, want inline=%d overflow=%d",
				c.total, len(inline), len(overflow), c.wantInline, c.wantOverflow)
		}
	}
}

func TestFilterChip(t *testing.T) {
	a := &Admin{cfg: &config{basePath: "/admin"}}
	res := &Resource{name: "users"}
	r := httptest.NewRequest("GET", "/admin/users?f_status=active&page=3&sort=email", nil)

	// An active select filter yields a chip whose RemoveURL drops its own param
	// and resets pagination, while preserving unrelated params (sort).
	chip, ok := a.filterChip(r, res, filterField{Label: "Status", Kind: "select", ParamKey: "f_status", Value: "active"})
	if !ok {
		t.Fatal("expected an active chip")
	}
	if chip.Label != "Status" || chip.Value != "active" {
		t.Errorf("unexpected chip %+v", chip)
	}
	if strings.Contains(chip.RemoveURL, "f_status") {
		t.Errorf("RemoveURL should clear f_status, got %q", chip.RemoveURL)
	}
	if strings.Contains(chip.RemoveURL, "page=") {
		t.Errorf("RemoveURL should reset pagination, got %q", chip.RemoveURL)
	}
	if !strings.Contains(chip.RemoveURL, "sort=email") {
		t.Errorf("RemoveURL should preserve unrelated params, got %q", chip.RemoveURL)
	}

	// A filter with no value produces no chip.
	if _, ok := a.filterChip(r, res, filterField{Label: "Status", Kind: "select", ParamKey: "f_status"}); ok {
		t.Error("an empty filter must not produce a chip")
	}

	// A daterange clears both bounds and renders an open-ended label.
	chip, ok = a.filterChip(r, res, filterField{
		Label: "Created", Kind: "daterange",
		ParamKey: "f_created__gt", ParamKeyTo: "f_created__lt", Value: "2026-01-01",
	})
	if !ok || chip.Value != "≥ 2026-01-01" {
		t.Errorf("daterange chip = %+v, ok=%v", chip, ok)
	}
}
