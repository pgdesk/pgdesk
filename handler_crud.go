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
		Filters:  withScope(lr.filters, scope),
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
	inlineFilters, overflowFilters, chips := a.filterViews(r, res, lr)
	data := listView{
		Base:            a.baseView(r, res.LabelPlural),
		Resource:        a.resourceMeta(res),
		SearchEnabled:   len(textColumns(res.searchFields)) > 0,
		Query:           lr.q,
		Headers:         a.sortHeaders(r, res, display, lr),
		Rows:            rowViews,
		HasDetail:       res.hasKey() && a.can(r, CapView, res.name, ""),
		CanCreate:       res.writable() && a.can(r, CapCreate, res.name, ""),
		Actions:         actions,
		HasActions:      len(actions) > 0,
		ExportURL:       a.exportURL(r, res),
		InlineFilters:   inlineFilters,
		OverflowFilters: overflowFilters,
		HasOverflow:     len(overflowFilters) > 0,
		ActiveChips:     chips,
		ActiveCount:     len(chips),
		HasFilters:      len(res.filters) > 0,
		Page:            lr.page,
		HasPrev:         lr.page > 1,
		HasNext:         hasNext,
		PrevURL:         a.pageURL(r, res, lr.page-1),
		NextURL:         a.pageURL(r, res, lr.page+1),
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
	if !a.guard(w, r, CapView, res.name, "") {
		return
	}
	scope, err := a.scopeFor(r, res, CapView, "")
	if err != nil {
		a.scopeDenied(w, r, res, err)
		return
	}

	keyVals, err := query.DecodeKey(res.keyCols, r.PathValue("key"))
	if err != nil {
		a.renderError(w, r, http.StatusBadRequest, "Invalid record identifier.")
		return
	}

	display := visibleColumns(res.table.Columns(), res)
	row, _, err := a.fetchRow(r, res, display, keyVals, scope)
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
		CanEdit:   res.writable() && a.can(r, CapUpdate, res.name, ""),
		CanDelete: res.writable() && a.can(r, CapDelete, res.name, ""),
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
	if !a.guard(w, r, CapUpdate, res.name, "") {
		return
	}
	scope, err := a.scopeFor(r, res, CapUpdate, "")
	if err != nil {
		a.scopeDenied(w, r, res, err)
		return
	}
	keyVals, err := query.DecodeKey(res.keyCols, r.PathValue("key"))
	if err != nil {
		a.renderError(w, r, http.StatusBadRequest, "Invalid record identifier.")
		return
	}
	display := visibleColumns(res.table.Columns(), res)
	row, version, err := a.fetchRow(r, res, display, keyVals, scope)
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
	if !a.guard(w, r, CapUpdate, res.name, "") {
		return
	}
	scope, err := a.scopeFor(r, res, CapUpdate, "")
	if err != nil {
		a.scopeDenied(w, r, res, err)
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
	// Pin equality-scoped columns to their scope value so a scoped operator cannot
	// move a row out of their scope by editing it (O6).
	setCols, setVals := enforceScope(editable, setVals, scope)
	returning := res.table.Columns()

	sql, args, err := query.UpdateRow(res.table, setCols, setVals, res.keyCols, keyVals, version, res.versionStrategy(), returning, scope)
	if err != nil {
		a.serverError(w, r, "build update", err)
		return
	}

	ctx, cancel := a.queryContext(r)
	defer cancel()

	_, updErr := a.execUpdateTx(ctx, r, res, sql, args, returning, keyVals, scope)
	switch {
	case errors.Is(updErr, errNotFound):
		a.renderError(w, r, http.StatusNotFound, "Record not found.")
		return
	case errors.Is(updErr, errConflict):
		a.renderConflict(w, r, res, keyVals, version, scope)
		return
	case updErr != nil:
		me := mapPgError(updErr, res.constraintMsgs, res.table.UniqueColumns)
		if me.empty() {
			a.dbError(w, r, "update", updErr)
			return
		}
		a.renderFormWithErrors(w, r, res, keyVals, version, me, scope)
		return
	}

	// Success: redirect to the detail page (F4 keeps this under the base path).
	dest := a.safeRedirect(a.cfg.basePath + "/" + res.name + "/" + r.PathValue("key"))
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
// is ambiguous: the row may have changed under us (O1), or it may lie outside the
// principal's scope (O6). rowInScope tells the two apart on the failure path only,
// inside the same transaction, so a forbidden edit reports 404 rather than telling
// the operator to reload and retry forever.
func (a *Admin) execUpdateTx(ctx context.Context, r *http.Request, res *Resource, sql string, args []any, returning []*introspect.Column, keyVals []any, scope []query.Filter) (map[string]any, error) {
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
			return a.zeroRowReason(ctx, tx, res, keyVals, scope)
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
func (a *Admin) fetchRow(r *http.Request, res *Resource, cols []*introspect.Column, keyVals []any, scope []query.Filter) (map[string]any, string, error) {
	sql, args, err := query.SelectRow(res.table, cols, res.keyCols, keyVals, res.versionStrategy(), scope)
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

// withScope returns the request filters ANDed with the principal's row
// constraints, without aliasing either input.
func withScope(filters, scope []query.Filter) []query.Filter {
	if len(scope) == 0 {
		return filters
	}
	out := make([]query.Filter, 0, len(filters)+len(scope))
	out = append(out, filters...)
	return append(out, scope...)
}

// enforceScope pins every equality-scoped column to its constraint value on a
// write, overriding whatever the operator submitted and adding the column if it
// was absent. This makes INSERT and UPDATE obey the same row scope the WHERE
// clause enforces on reads: a scoped operator can neither create a row outside
// their scope nor move one out of it.
//
// Only equality (Eq) constraints pin a value. A Ne or In constraint has no single
// value to write, so it is left to the read/WHERE side (which still stops an
// UPDATE from targeting an out-of-scope row); a host that must also stop such a
// column from being written marks it Readonly. Inputs are not aliased.
func enforceScope(cols []*introspect.Column, vals []any, scope []query.Filter) ([]*introspect.Column, []any) {
	outCols := append([]*introspect.Column(nil), cols...)
	outVals := append([]any(nil), vals...)
	have := make(map[string]int, len(outCols))
	for i, c := range outCols {
		have[c.Name] = i
	}
	for _, f := range scope {
		if f.Op != query.OpEq || len(f.Values) != 1 {
			continue
		}
		if i, ok := have[f.Col.Name]; ok {
			outVals[i] = f.Values[0]
			continue
		}
		outCols = append(outCols, f.Col)
		outVals = append(outVals, f.Values[0])
		have[f.Col.Name] = len(outCols) - 1
	}
	return outCols, outVals
}

// zeroRowReason explains why a scoped UPDATE or DELETE matched nothing. It probes
// for the row within scope but WITHOUT the version guard: a row that still does
// not appear is one the principal cannot reach (404, which also refuses to
// confirm the row exists); a row that does appear was changed by someone else
// (409). The probe runs only after the mutation has already failed, in the same
// transaction, so its answer cannot go stale.
func (a *Admin) zeroRowReason(ctx context.Context, tx pgx.Tx, res *Resource, keyVals []any, scope []query.Filter) error {
	sql, args, err := query.ExistsRow(res.table, res.keyCols, keyVals, scope)
	if err != nil {
		return err
	}
	var one int
	if err := tx.QueryRow(ctx, sql, args...).Scan(&one); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		return err
	}
	return errConflict
}

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
