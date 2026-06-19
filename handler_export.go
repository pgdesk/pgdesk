package pgdesk

import (
	"encoding/csv"
	"net/http"
	"strconv"

	"github.com/pgdesk/pgdesk/internal/query"
	"github.com/pgdesk/pgdesk/internal/render"
)

// handleExport streams the current (filtered/sorted) list as CSV (F7). It runs
// through the same authorizer (CapList), catalog identifier resolution (D3), and
// bounded query as the HTML list — only the presentation differs. Row count is
// capped so an export can't force an unbounded scan (F6).
func (a *Admin) handleExport(w http.ResponseWriter, r *http.Request) {
	res, ok := a.liveResource(r, r.PathValue("resource"))
	if !ok {
		a.renderError(w, r, http.StatusNotFound, "Unknown resource.")
		return
	}
	if !a.guard(w, r, CapList, res.name, "", a.authorizerFor(res)) {
		return
	}
	lr, err := a.parseListRequest(r, res)
	if err != nil {
		a.renderError(w, r, http.StatusBadRequest, "Invalid list parameters.")
		return
	}

	display := visibleColumns(res.listDisplay, res)
	sql, args, err := query.BuildList(res.table, query.ListParams{
		Columns:  display,
		Filters:  lr.filters,
		Search:   lr.search,
		Sort:     lr.sortCol,
		SortDesc: lr.sortDesc,
		Limit:    a.cfg.maxExportRows,
		Offset:   0,
	})
	if err != nil {
		a.serverError(w, r, "build export query", err)
		return
	}

	ctx, cancel := a.queryContext(r)
	defer cancel()
	rows, qerr := a.runQuery(ctx, "export", sql, args)
	if qerr != nil {
		a.dbError(w, r, "export query", qerr)
		return
	}
	defer rows.Close()

	// Headers are set before the first write; a mid-stream DB error can no longer
	// change the status, so such errors are logged and the stream simply ends.
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(res.name+".csv"))
	cw := csv.NewWriter(w)

	header := make([]string, len(display))
	for i, c := range display {
		header[i] = a.fieldLabel(res, c)
	}
	if err := cw.Write(header); err != nil {
		return
	}
	for rows.Next() {
		vals, verr := rows.Values()
		if verr != nil {
			LoggerFromContext(r.Context()).Error("pgdesk: export scan failed", "error", verr)
			break
		}
		rec := make([]string, len(display))
		for i := range display {
			if i < len(vals) {
				rec[i] = render.FormatValue(vals[i])
			}
		}
		if err := cw.Write(rec); err != nil {
			return
		}
	}
	if err := rows.Err(); err != nil {
		LoggerFromContext(r.Context()).Error("pgdesk: export iterate failed", "error", err)
	}
	cw.Flush()
}
