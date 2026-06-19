package pgdesk

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
)

// liveResource looks up a registered resource and confirms its table still
// exists in THIS request's catalog snapshot (D1). If a Reload dropped the table
// after registration, the request fails closed with "not found" rather than
// issuing a query against a vanished relation. Using the per-request snapshot
// (not the live pointer) means a Reload mid-request cannot shift the answer.
func (a *Admin) liveResource(r *http.Request, name string) (*Resource, bool) {
	st := stateFromContext(r.Context())
	if st == nil {
		return nil, false
	}
	return st.resource(name)
}

// visibleColumns returns list/detail columns minus hidden ones.
func visibleColumns(cols []*introspect.Column, res *Resource) []*introspect.Column {
	out := make([]*introspect.Column, 0, len(cols))
	for _, c := range cols {
		if fc := res.fields[c.Name]; fc != nil && fc.hidden {
			continue
		}
		out = append(out, c)
	}
	return out
}

// handleList renders the paginated list view (D3 sort/pagination; F6 clamps).
func (a *Admin) handleList(w http.ResponseWriter, r *http.Request) {
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
	// Ensure key columns are fetched so we can build detail links, even if not
	// shown. fetch = display ++ (keyCols not already in display).
	fetch := append([]*introspect.Column(nil), display...)
	keyIdx := make([]int, 0, len(res.keyCols))
	for _, kc := range res.keyCols {
		idx := indexOfColumn(fetch, kc.Name)
		if idx < 0 {
			fetch = append(fetch, kc)
			idx = len(fetch) - 1
		}
		keyIdx = append(keyIdx, idx)
	}

	offset := (lr.page - 1) * lr.pageSize
	sql, args, err := query.BuildList(res.table, query.ListParams{
		Columns:  fetch,
		Filters:  lr.filters,
		Search:   lr.search,
		Sort:     lr.sortCol,
		SortDesc: lr.sortDesc,
		Limit:    lr.pageSize + 1, // fetch one extra to detect a next page
		Offset:   offset,
	})
	if err != nil {
		a.serverError(w, r, "build list query", err)
		return
	}

	ctx, cancel := a.queryContext(r)
	defer cancel()
	rows, qerr := a.runQuery(ctx, "list", sql, args)
	if qerr != nil {
		a.dbError(w, r, "list query", qerr)
		return
	}
	defer rows.Close()

	type rawRow struct {
		cells []any
		key   string
	}
	var raw []rawRow
	for rows.Next() {
		vals, verr := rows.Values()
		if verr != nil {
			a.serverError(w, r, "scan list row", verr)
			return
		}
		keyVals := make([]any, len(keyIdx))
		for i, idx := range keyIdx {
			keyVals[i] = vals[idx]
		}
		seg, kerr := query.EncodeKey(res.keyCols, keyVals)
		if kerr != nil && res.hasKey() {
			a.serverError(w, r, "encode key", kerr)
			return
		}
		raw = append(raw, rawRow{cells: vals[:len(display)], key: seg})
	}
	if rows.Err() != nil {
		a.serverError(w, r, "iterate list rows", rows.Err())
		return
	}

	hasNext := len(raw) > lr.pageSize
	if hasNext {
		raw = raw[:lr.pageSize]
	}

	// Resolve foreign-key labels for the whole page in one query per FK (D4).
	pageCells := make([][]any, len(raw))
	for i, rr := range raw {
		pageCells[i] = rr.cells
	}
	labels := a.resolveFKLabels(r, res, display, pageCells)

	rowViews := make([]rowView, len(raw))
	for ri, rr := range raw {
		cells := make([]cellView, len(display))
		for ci, c := range display {
			cells[ci] = a.buildCell(res, c, rr.cells[ci], labels)
		}
		rowViews[ri] = rowView{Cells: cells, Key: rr.key}
	}

	actions := a.visibleActions(r, res)
	data := listView{
		Base:          a.baseView(r, res.LabelPlural),
		Resource:      a.resourceMeta(res),
		SearchEnabled: len(textColumns(res.searchFields)) > 0,
		Query:         lr.q,
		Headers:       a.sortHeaders(r, res, display, lr),
		Rows:          rowViews,
		HasDetail:     res.hasKey(),
		CanCreate:     res.writable(),
		Actions:       actions,
		HasActions:    len(actions) > 0,
		ExportURL:     a.exportURL(r, res),
		Filters:       a.filterFields(res, lr),
		HasFilters:    len(res.filters) > 0,
		Page:          lr.page,
		HasPrev:       lr.page > 1,
		HasNext:       hasNext,
		PrevURL:       a.pageURL(r, res, lr.page-1),
		NextURL:       a.pageURL(r, res, lr.page+1),
	}
	a.renderPage(w, r, http.StatusOK, "list", data)
}

// handleDetail renders a single row (D6 key decode; O6 CapView).
func (a *Admin) handleDetail(w http.ResponseWriter, r *http.Request) {
	res, ok := a.liveResource(r, r.PathValue("resource"))
	if !ok {
		a.renderError(w, r, http.StatusNotFound, "Unknown resource.")
		return
	}
	if !res.hasKey() {
		a.renderError(w, r, http.StatusNotFound, "This resource has no detail view.")
		return
	}
	if !a.guard(w, r, CapView, res.name, "", a.authorizerFor(res)) {
		return
	}

	keyVals, err := query.DecodeKey(res.keyCols, r.PathValue("key"))
	if err != nil {
		a.renderError(w, r, http.StatusBadRequest, "Invalid record identifier.")
		return
	}

	display := visibleColumns(res.table.Columns(), res)
	row, _, err := a.fetchRow(r, res, display, keyVals)
	if err != nil {
		a.handleFetchError(w, r, err)
		return
	}

	fields := make([]detailField, len(display))
	for i, c := range display {
		fields[i] = detailField{Label: a.fieldLabel(res, c), Value: row[c.Name]}
	}
	data := detailView{
		Base:      a.baseView(r, res.Label),
		Resource:  a.resourceMeta(res),
		Key:       r.PathValue("key"),
		CanEdit:   res.writable(),
		CanDelete: res.writable(),
		Fields:    fields,
	}
	a.renderPage(w, r, http.StatusOK, "detail", data)
}

// handleEditForm renders the edit form pre-filled from the current row (O6
// CapUpdate; O1 loads the version token).
func (a *Admin) handleEditForm(w http.ResponseWriter, r *http.Request) {
	res, ok := a.liveResource(r, r.PathValue("resource"))
	if !ok || !res.writable() {
		a.renderError(w, r, http.StatusNotFound, "This resource cannot be edited.")
		return
	}
	if !a.guard(w, r, CapUpdate, res.name, "", a.authorizerFor(res)) {
		return
	}
	keyVals, err := query.DecodeKey(res.keyCols, r.PathValue("key"))
	if err != nil {
		a.renderError(w, r, http.StatusBadRequest, "Invalid record identifier.")
		return
	}
	display := visibleColumns(res.table.Columns(), res)
	row, version, err := a.fetchRow(r, res, display, keyVals)
	if err != nil {
		a.handleFetchError(w, r, err)
		return
	}
	key := r.PathValue("key")
	data := formView{
		Base:     a.baseView(r, "Edit "+res.Label),
		Resource: a.resourceMeta(res),
		Action:   a.editAction(res, key),
		Key:      key,
		Version:  version,
		Fields:   a.buildFormFields(res, display, row, nil),
	}
	a.renderPage(w, r, http.StatusOK, "form", data)
}

// handleUpdate applies an optimistic-concurrency UPDATE in a transaction, with
// audit in the same tx (O4), CSRF (D5), pg-error mapping (D7), and the 0-rows
// conflict path (O1).
func (a *Admin) handleUpdate(w http.ResponseWriter, r *http.Request) {
	res, ok := a.liveResource(r, r.PathValue("resource"))
	if !ok || !res.writable() {
		a.renderError(w, r, http.StatusNotFound, "This resource cannot be edited.")
		return
	}
	if !a.guard(w, r, CapUpdate, res.name, "", a.authorizerFor(res)) {
		return
	}

	// Bound the body before touching it (F6), then verify CSRF (D5).
	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		a.renderError(w, r, http.StatusBadRequest, "The submitted form was invalid or too large.")
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		LoggerFromContext(r.Context()).Warn("pgdesk: CSRF verification failed",
			"resource", res.name, "error", err)
		a.renderError(w, r, http.StatusForbidden, "Your session could not be verified. Please reload and try again.")
		return
	}

	keyVals, err := query.DecodeKey(res.keyCols, r.PathValue("key"))
	if err != nil {
		a.renderError(w, r, http.StatusBadRequest, "Invalid record identifier.")
		return
	}

	version := r.PostFormValue("_version")
	editable := res.editableColumns()
	setVals := make([]any, len(editable))
	for i, c := range editable {
		setVals[i] = formValueForColumn(r, c)
	}
	returning := res.table.Columns()

	sql, args, err := query.UpdateRow(res.table, editable, setVals, res.keyCols, keyVals, version, res.versionStrategy(), returning)
	if err != nil {
		a.serverError(w, r, "build update", err)
		return
	}

	ctx, cancel := a.queryContext(r)
	defer cancel()

	result, updErr := a.execUpdateTx(ctx, r, res, sql, args, returning, keyVals)
	switch {
	case errors.Is(updErr, errConflict):
		a.renderConflict(w, r, res, keyVals, version)
		return
	case updErr != nil:
		me := mapPgError(updErr, res.constraintMsgs, res.table.UniqueColumns)
		if me.empty() {
			a.dbError(w, r, "update", updErr)
			return
		}
		a.renderFormWithErrors(w, r, res, keyVals, version, me)
		return
	}

	// Success: redirect to the detail page (F4 keeps this under the base path).
	dest := a.safeRedirect(a.cfg.basePath + "/" + res.name + "/" + r.PathValue("key"))
	_ = result
	a.setFlash(w, "info", res.Label+" saved.")
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// errConflict signals the O1 lost-update guard tripped (0 rows affected).
var errConflict = errors.New("pgdesk: optimistic concurrency conflict")

// withTx runs fn inside a single transaction with a SET LOCAL statement_timeout
// backstop (O2). It commits on success and rolls back on any error or panic, so
// no partial write escapes a cancelled or failed request (O3). The host owns the
// pool; this owns only the transaction.
func (a *Admin) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	// Statement-timeout backstop. The value is our own integer, never user input.
	ms := a.cfg.queryTimeout.Milliseconds()
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = "+strconv.FormatInt(ms, 10)); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

// bestEffortAudit runs the out-of-band audit logger after a successful commit. A
// failure is logged but never affects the response (O4 documents the tradeoff).
func (a *Admin) bestEffortAudit(ctx context.Context, ev AuditEvent) {
	if a.cfg.audit == nil {
		return
	}
	if err := a.cfg.audit.LogAudit(ctx, ev); err != nil {
		LoggerFromContext(ctx).Error("pgdesk: best-effort audit failed", "error", err)
	}
}

// execUpdateTx runs the UPDATE and audit in one transaction (O4). A 0-row result
// means another transaction changed the row first (O1) → errConflict.
func (a *Admin) execUpdateTx(ctx context.Context, r *http.Request, res *Resource, sql string, args []any, returning []*introspect.Column, keyVals []any) (map[string]any, error) {
	var after map[string]any
	err := a.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		after, err = scanOneRow(rows, returning)
		rows.Close()
		if err != nil {
			return err
		}
		if after == nil {
			return errConflict
		}
		if a.cfg.txAudit != nil {
			ev := a.buildAuditEvent(r, res, AuditUpdate, keyVals, nil, after)
			if err := a.cfg.txAudit.LogAuditTx(ctx, tx, ev); err != nil {
				return err // audit failure rolls back the mutation
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	a.bestEffortAudit(ctx, a.buildAuditEvent(r, res, AuditUpdate, keyVals, nil, after))
	return after, nil
}

// fetchRow loads a single row's columns plus its version token (O1).
func (a *Admin) fetchRow(r *http.Request, res *Resource, cols []*introspect.Column, keyVals []any) (map[string]any, string, error) {
	sql, args, err := query.SelectRow(res.table, cols, res.keyCols, keyVals, res.versionStrategy())
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := a.queryContext(r)
	defer cancel()
	rows, err := a.runQuery(ctx, "detail", sql, args)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	if !rows.Next() {
		if rows.Err() != nil {
			return nil, "", rows.Err()
		}
		return nil, "", errNotFound
	}
	vals, err := rows.Values()
	if err != nil {
		return nil, "", err
	}
	// First selected column is the version token (SelectRow prepends it).
	version := ""
	if len(vals) > 0 {
		version = FormatVersion(vals[0])
	}
	m := make(map[string]any, len(cols))
	for i, c := range cols {
		m[c.Name] = vals[i+1]
	}
	return m, version, nil
}

var errNotFound = errors.New("pgdesk: row not found")

func (a *Admin) handleFetchError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errNotFound) {
		a.renderError(w, r, http.StatusNotFound, "Record not found.")
		return
	}
	a.dbError(w, r, "fetch row", err)
}

// runQuery executes a read query with metrics timing (O5).
func (a *Admin) runQuery(ctx context.Context, op, sql string, args []any) (pgx.Rows, error) {
	start := time.Now()
	rows, err := a.db.Query(ctx, sql, args...)
	a.cfg.metrics.ObserveQuery(op, time.Since(start), err)
	return rows, err
}

// serverError logs full detail server-side (keyed by request ID) and renders a
// generic 500 — internals never reach the browser (F5).
func (a *Admin) serverError(w http.ResponseWriter, r *http.Request, stage string, err error) {
	LoggerFromContext(r.Context()).Error("pgdesk: request failed",
		"stage", stage, "error", err, "request_id", RequestIDFromContext(r.Context()))
	a.renderError(w, r, http.StatusInternalServerError, "An unexpected error occurred.")
}
