package pgdesk

import (
	"context"
	"net/http"
	"time"

	"github.com/pgdesk/pgdesk/internal/query"
)

// defaultPaginationWindow is how many numbered page links a pager shows around
// the current page before collapsing the rest into first/last and ellipses.
const defaultPaginationWindow = 5

// pageLink is one entry in a numbered pager. When Ellipsis is true the entry
// stands for a gap between pages and has no URL.
type pageLink struct {
	Page     int
	URL      string
	Current  bool
	Ellipsis bool
}

// numberedPagerFor reports whether res should show numbered page links,
// applying the per-resource override over the global default.
func (a *Admin) numberedPagerFor(res *Resource) bool {
	if res.numberedPager != nil {
		return *res.numberedPager
	}
	return a.cfg.numberedPagination
}

// totalPagesFor is the number of pages needed to hold total rows, and is never
// less than 1 so that "Page 1 of 1" still reads sensibly for an empty result.
func totalPagesFor(total int64, pageSize int) int {
	if total <= 0 || pageSize <= 0 {
		return 1
	}
	return int((total + int64(pageSize) - 1) / int64(pageSize))
}

// pageNumbers returns, in ascending order, the page numbers a pager should link
// to: a window of consecutive pages around the current one, plus the first and
// last pages when the range is longer. pageLinks renders each gap between
// consecutive numbers as an ellipsis.
//
// A page past the end of the result set is clamped into range: the rows can
// shrink between the count and the request.
func pageNumbers(page, totalPages, window int) []int {
	if totalPages < 1 {
		return nil
	}
	if window < 1 {
		window = 1
	}

	cur := page
	if cur < 1 {
		cur = 1
	}
	if cur > totalPages {
		cur = totalPages
	}

	if totalPages <= window {
		out := make([]int, totalPages)
		for i := range out {
			out[i] = i + 1
		}
		return out
	}

	half := window / 2
	start := cur - half
	end := start + window - 1
	if start < 1 {
		start, end = 1, window
	}
	if end > totalPages {
		start, end = totalPages-window+1, totalPages
	}

	out := make([]int, 0, window+2)
	if start > 1 {
		out = append(out, 1)
	}
	for p := start; p <= end; p++ {
		out = append(out, p)
	}
	if end < totalPages {
		out = append(out, totalPages)
	}
	return out
}

// pageLinks turns pageNumbers into render-ready links, inserting an ellipsis
// entry wherever consecutive numbers skip a page.
func (a *Admin) pageLinks(r *http.Request, res *Resource, page, totalPages, window int) []pageLink {
	nums := pageNumbers(page, totalPages, window)
	if len(nums) == 0 {
		return nil
	}
	links := make([]pageLink, 0, len(nums)+2)
	for i, n := range nums {
		if i > 0 && n-nums[i-1] > 1 {
			links = append(links, pageLink{Ellipsis: true})
		}
		links = append(links, pageLink{
			Page:    n,
			URL:     a.pageURL(r, res, n),
			Current: n == page,
		})
	}
	return links
}

// countListRows counts the rows the current list query matches, using the same
// filters and search as the page itself.
//
// ctx carries its own deadline: the caller runs this after the page query has
// already spent its share of the request timeout, which on a slow list would
// otherwise leave the count cancelled before it starts.
func (a *Admin) countListRows(ctx context.Context, res *Resource, filters []query.Filter, search *query.SearchSpec) (int64, error) {
	sql, args, err := query.BuildCount(res.table, query.ListParams{Filters: filters, Search: search})
	if err != nil {
		return 0, err
	}
	start := time.Now()
	var n int64
	err = a.db.QueryRow(ctx, sql, args...).Scan(&n)
	a.cfg.metrics.ObserveQuery("count", time.Since(start), err)
	return n, err
}
