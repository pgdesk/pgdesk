package pgdesk

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
	"github.com/pgdesk/pgdesk/internal/render"
)

// optionsSegment is the literal last path segment of the picker's endpoint. It is
// named so the router, the JSON-refusal check, and the template all agree.
const optionsSegment = "options.json"

// fkOption is one selectable row in a foreign-key picker: Value is the key the
// form submits, Label is what the operator reads.
type fkOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// fkOptionsResponse is the picker endpoint's contract. Truncated tells the client
// the cap was reached, so it can say "narrow your search" instead of implying the
// list is complete.
type fkOptionsResponse struct {
	Options   []fkOption `json:"options"`
	Truncated bool       `json:"truncated"`
	// Searchable reports whether this resource has any column a ?q= term can be
	// matched against. When false the term is NOT applied -- the response is the
	// first bounded page -- and the client must say so rather than present
	// unfiltered rows that look like search results.
	Searchable bool `json:"searchable"`
}

// handleOptions answers a foreign-key picker's search over THIS resource: it
// returns at most maxOptions (key, label) pairs matching ?q=. It is mounted on the
// referenced resource rather than on the referencing column, which is what makes
// its authorization obvious -- offering a row as an option is listing it, so the
// route requires CapList on this resource and applies this resource's own row
// scope. An operator who may edit orders but may not list users gets a 403 here
// and a plain text input in the form; they can never enumerate users through the
// picker.
//
// The response is JSON on every path, including refusals: the client is a fetch()
// and must be able to tell "denied" from "no matches" without parsing an HTML
// error page.
func (a *Admin) handleOptions(w http.ResponseWriter, r *http.Request) {
	res, ok := a.liveResource(r, r.PathValue("resource"))
	if !ok {
		a.jsonError(w, r, http.StatusNotFound, "Unknown resource.")
		return
	}
	// Authorization comes before anything about the resource's shape is reported.
	// Answering the key-cardinality check first would let an unauthenticated caller
	// tell a single-column key from a composite one by the 404-vs-401 split, which
	// is schema information they have not earned.
	if PrincipalFromContext(r.Context()) == nil {
		// Never redirect a fetch() to the login page: it would resolve to HTML and
		// look like a successful (empty) result.
		a.jsonError(w, r, http.StatusUnauthorized, "Authentication is required.")
		return
	}
	if !a.can(r, CapList, res.name, "") {
		a.jsonError(w, r, http.StatusForbidden, "You are not permitted to list this resource.")
		return
	}
	// A picker submits one key in one form field, so a keyless resource -- or a
	// composite key, which no single field can carry -- has nothing to offer.
	if len(res.keyCols) != 1 {
		a.jsonError(w, r, http.StatusNotFound, "This resource has no single-column key to pick.")
		return
	}
	scope, err := a.scopeFor(r, res, CapList, "")
	if err != nil {
		LoggerFromContext(r.Context()).Error("pgdesk: option scope failed",
			"resource", res.name, "error", err)
		a.jsonError(w, r, http.StatusForbidden, "You are not permitted to list this resource.")
		return
	}

	pk := res.keyCols[0]
	label := labelColumn(res, pk)
	cols := []*introspect.Column{pk}
	if label.Name != pk.Name {
		cols = append(cols, label)
	}
	// Fetch one more than the cap so a full result is distinguishable from an
	// exact-fit one, exactly as the export does.
	params := query.ListParams{
		Columns: cols,
		Filters: scope,
		Sort:    label,
		Limit:   a.cfg.maxOptions + 1,
	}
	searchCols := optionSearchColumns(res, label)
	if term := strings.TrimSpace(r.URL.Query().Get("q")); term != "" && len(searchCols) > 0 {
		// escapeLike keeps the term data rather than syntax: a % an operator types
		// matches a literal %, it does not widen the scan.
		params.Search = &query.SearchSpec{Cols: searchCols, Term: "%" + escapeLike(term) + "%"}
	}

	sql, args, err := query.BuildList(res.table, params)
	if err != nil {
		a.jsonServerError(w, r, "build options query", err)
		return
	}
	ctx, cancel := a.queryContext(r)
	defer cancel()
	rows, qerr := a.runQuery(ctx, "fk_options", sql, args)
	if qerr != nil {
		a.jsonDBError(w, r, "options query", qerr)
		return
	}
	defer rows.Close()

	out := fkOptionsResponse{
		Options:    make([]fkOption, 0, a.cfg.maxOptions),
		Searchable: len(searchCols) > 0,
	}
	for rows.Next() {
		vals, verr := rows.Values()
		if verr != nil {
			a.jsonServerError(w, r, "scan options", verr)
			return
		}
		if len(out.Options) == a.cfg.maxOptions {
			out.Truncated = true
			break
		}
		// The key is rendered exactly as the form pre-fills it, so selecting an
		// option and typing the same key by hand submit the same bytes.
		out.Options = append(out.Options, fkOption{
			Value: render.FormatValue(vals[0]),
			Label: render.FormatValue(vals[len(vals)-1]),
		})
	}
	if rerr := rows.Err(); rerr != nil {
		a.jsonDBError(w, r, "options stream", rerr)
		return
	}
	a.writeJSON(w, r, http.StatusOK, out)
}

// optionSearchColumns picks the columns a picker search matches: the referenced
// resource's own SearchFields when it declares them (so the host controls what is
// searchable), else the label column. Only text columns are used -- ILIKE against
// a non-text column is a PostgreSQL error, so including one would turn a search
// into a 500.
func optionSearchColumns(res *Resource, label *introspect.Column) []*introspect.Column {
	var out []*introspect.Column
	for _, c := range res.searchFields {
		if c.Category == introspect.CatText {
			out = append(out, c)
		}
	}
	if len(out) == 0 && label.Category == introspect.CatText {
		out = append(out, label)
	}
	return out
}

// --- JSON responses ---

// writeJSON renders a JSON body. It marshals to memory first so a mid-encode
// failure cannot leave a half-written body under an already-sent 200.
func (a *Admin) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		LoggerFromContext(r.Context()).Error("pgdesk: marshalling JSON response",
			"error", err, "request_id", RequestIDFromContext(r.Context()))
		http.Error(w, `{"error":"Internal server error."}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Responses are scoped to the principal, so they must never be cached by a
	// shared or browser cache.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		LoggerFromContext(r.Context()).Warn("pgdesk: writing JSON response", "error", err)
	}
}

// jsonError writes an operator-safe message with no internal detail, mirroring
// renderError's discipline for HTML.
func (a *Admin) jsonError(w http.ResponseWriter, r *http.Request, status int, message string) {
	a.writeJSON(w, r, status, map[string]string{"error": message})
}

// jsonServerError logs the cause and returns a generic 500, so an internal error
// never reaches the client.
func (a *Admin) jsonServerError(w http.ResponseWriter, r *http.Request, stage string, err error) {
	LoggerFromContext(r.Context()).Error("pgdesk: "+stage,
		"error", err, "request_id", RequestIDFromContext(r.Context()))
	a.jsonError(w, r, http.StatusInternalServerError, "Internal server error.")
}

// jsonDBError classifies a database failure the same way dbError does for HTML: a
// deadline or a saturated pool is a retryable 503, anything else a 500.
func (a *Admin) jsonDBError(w http.ResponseWriter, r *http.Request, stage string, err error) {
	if isDeadline(err) {
		LoggerFromContext(r.Context()).Warn("pgdesk: request deadline or pool saturation",
			"stage", stage, "error", err, "request_id", RequestIDFromContext(r.Context()))
		w.Header().Set("Retry-After", "1")
		a.jsonError(w, r, http.StatusServiceUnavailable, "The server is busy. Please retry in a moment.")
		return
	}
	a.jsonServerError(w, r, stage, err)
}
