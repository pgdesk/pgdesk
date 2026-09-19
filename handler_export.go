package pgdesk

import (
	"context"
	"encoding/csv"
	"net/http"
	"strconv"

	"github.com/pgdesk/pgdesk/internal/query"
	"github.com/pgdesk/pgdesk/internal/render"
)

func (a *Admin) handleExport(w http.ResponseWriter, r *http.Request) {
	res, ok := a.liveResource(r, r.PathValue("resource"))
	if !ok {
		a.renderError(w, r, http.StatusNotFound, "Unknown resource.")
		return
	}
	if !a.guard(w, r, CapList, res.name, "") {
		return
	}
	scope, err := a.scopeFor(r, res, CapList, "")
	if err != nil {
		a.scopeDenied(w, r, res, err)
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
		Filters:  withScope(lr.filters, scope),
		Search:   lr.search,
		Sort:     lr.sortCol,
		SortDesc: lr.sortDesc,
		Limit:    a.cfg.maxExportRows + 1,
		Offset:   0,
	})
	if err != nil {
		a.serverError(w, r, "build export query", err)
		return
	}

	ctx, cancel := a.exportContext(r)
	defer cancel()
	rows, qerr := a.runQuery(ctx, "export", sql, args)
	if qerr != nil {
		a.dbError(w, r, "export query", qerr)
		return
	}
	defer rows.Close()

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(res.name+".csv"))
	cw := csv.NewWriter(w)

	header := make([]string, len(display))
	for i, c := range display {
		header[i] = csvSafeCell(a.fieldLabel(res, c))
	}
	if err := cw.Write(header); err != nil {
		return
	}
	written := 0
	for rows.Next() {

		if written >= a.cfg.maxExportRows {
			LoggerFromContext(r.Context()).Warn("pgdesk: export truncated at row cap",
				"resource", res.name, "max_export_rows", a.cfg.maxExportRows)
			break
		}
		vals, verr := rows.Values()
		if verr != nil {
			LoggerFromContext(r.Context()).Error("pgdesk: export scan failed", "error", verr)
			break
		}
		rec := make([]string, len(display))
		for i := range display {
			if i < len(vals) {
				rec[i] = csvSafeCell(render.FormatValue(vals[i]))
			}
		}
		if err := cw.Write(rec); err != nil {
			return
		}
		written++
	}
	if err := rows.Err(); err != nil {
		LoggerFromContext(r.Context()).Error("pgdesk: export iterate failed", "error", err)
	}
	cw.Flush()
}

func (a *Admin) exportContext(r *http.Request) (context.Context, context.CancelFunc) {
	d := a.cfg.exportTimeout
	if d < a.cfg.queryTimeout {
		d = a.cfg.queryTimeout
	}
	return context.WithTimeout(r.Context(), d)
}

func csvSafeCell(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}
