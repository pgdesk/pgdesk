package pgdesk

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestTotalPagesFor(t *testing.T) {
	cases := []struct {
		total    int64
		pageSize int
		want     int
	}{
		{0, 50, 1},
		{-5, 50, 1},
		{1, 50, 1},
		{50, 50, 1},
		{51, 50, 2},
		{100, 50, 2},
		{101, 50, 3},
		{10, 0, 1},
	}
	for _, tc := range cases {
		if got := totalPagesFor(tc.total, tc.pageSize); got != tc.want {
			t.Errorf("totalPagesFor(%d, %d) = %d, want %d", tc.total, tc.pageSize, got, tc.want)
		}
	}
}

func TestPageNumbers(t *testing.T) {
	cases := []struct {
		name       string
		page       int
		totalPages int
		window     int
		want       []int
	}{
		{"empty result", 1, 0, 5, nil},
		{"single page", 1, 1, 5, []int{1}},
		{"fewer pages than window", 2, 3, 5, []int{1, 2, 3}},
		{"window equals total", 3, 5, 5, []int{1, 2, 3, 4, 5}},
		{"middle", 10, 20, 5, []int{1, 8, 9, 10, 11, 12, 20}},
		{"near start clamps window", 2, 20, 5, []int{1, 2, 3, 4, 5, 20}},
		{"start clamps", 1, 20, 5, []int{1, 2, 3, 4, 5, 20}},
		{"near end clamps window", 19, 20, 5, []int{1, 16, 17, 18, 19, 20}},
		{"end clamps", 20, 20, 5, []int{1, 16, 17, 18, 19, 20}},
		{"page beyond end clamps into window", 99, 20, 5, []int{1, 16, 17, 18, 19, 20}},
		{"zero page clamps to first", 0, 20, 5, []int{1, 2, 3, 4, 5, 20}},
		{"window one", 10, 20, 1, []int{1, 10, 20}},
		{"zero window treated as one", 10, 20, 0, []int{1, 10, 20}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pageNumbers(tc.page, tc.totalPages, tc.window)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("pageNumbers(%d, %d, %d) = %v, want %v", tc.page, tc.totalPages, tc.window, got, tc.want)
			}
		})
	}
}

func TestPageLinksInsertEllipsis(t *testing.T) {
	a := &Admin{cfg: defaultConfig()}
	res := &Resource{name: "users"}
	r := httptest.NewRequest("GET", "/admin/users", nil)

	got := a.pageLinks(r, res, 10, 20, 5)
	want := []pageLink{
		{Page: 1, URL: "/admin/users"},
		{Ellipsis: true},
		{Page: 8, URL: "/admin/users?page=8"},
		{Page: 9, URL: "/admin/users?page=9"},
		{Page: 10, URL: "/admin/users?page=10", Current: true},
		{Page: 11, URL: "/admin/users?page=11"},
		{Page: 12, URL: "/admin/users?page=12"},
		{Ellipsis: true},
		{Page: 20, URL: "/admin/users?page=20"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pageLinks = %+v, want %+v", got, want)
	}
}

func TestPageLinksPreserveQuery(t *testing.T) {
	a := &Admin{cfg: defaultConfig()}
	res := &Resource{name: "users"}
	r := httptest.NewRequest("GET", "/admin/users?f_status=active&sort=-email&page=3", nil)

	links := a.pageLinks(r, res, 3, 10, 5)
	for _, l := range links {
		if l.Ellipsis || l.Current {
			continue
		}
		if !strings.Contains(l.URL, "f_status=active") || !strings.Contains(l.URL, "sort=-email") {
			t.Errorf("page link dropped filters/sort: %s", l.URL)
		}
	}
}

func TestNumberedPagerFor(t *testing.T) {
	on := testAdmin(t, func(c *config) { c.numberedPagination = true })
	off := testAdmin(t)

	follow := &Resource{name: "users"}
	onRes := &Resource{name: "users"}
	onRes.NumberedPagination(true)
	offRes := &Resource{name: "users"}
	offRes.NumberedPagination(false)

	if on.numberedPagerFor(follow) != true || off.numberedPagerFor(follow) != false {
		t.Error("an unset resource should follow the global default")
	}
	if !off.numberedPagerFor(onRes) {
		t.Error("a resource override should enable numbering when the default is off")
	}
	if on.numberedPagerFor(offRes) {
		t.Error("a resource override should disable numbering when the default is on")
	}
}
