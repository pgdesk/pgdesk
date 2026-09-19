package pgdesk

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
	"github.com/pgdesk/pgdesk/internal/render"
)

const optionsSegment = "options.json"

type fkOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type fkOptionsResponse struct {
	Options   []fkOption `json:"options"`
	Truncated bool       `json:"truncated"`

	Searchable bool `json:"searchable"`
}

func (a *Admin) handleOptions(w http.ResponseWriter, r *http.Request) {
	res, ok := a.liveResource(r, r.PathValue("resource"))
	if !ok {
		a.jsonError(w, r, http.StatusNotFound, "Unknown resource.")
		return
	}

	if PrincipalFromContext(r.Context()) == nil {

		a.jsonError(w, r, http.StatusUnauthorized, "Authentication is required.")
		return
	}
	if !a.can(r, CapList, res.name, "") {
		a.jsonError(w, r, http.StatusForbidden, "You are not permitted to list this resource.")
		return
	}

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

	params := query.ListParams{
		Columns: cols,
		Filters: scope,
		Sort:    label,
		Limit:   a.cfg.maxOptions + 1,
	}
	searchCols := optionSearchColumns(res, label)
	if term := strings.TrimSpace(r.URL.Query().Get("q")); term != "" && len(searchCols) > 0 {

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

func (a *Admin) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		LoggerFromContext(r.Context()).Error("pgdesk: marshalling JSON response",
			"error", err, "request_id", RequestIDFromContext(r.Context()))
		http.Error(w, `{"error":"Internal server error."}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		LoggerFromContext(r.Context()).Warn("pgdesk: writing JSON response", "error", err)
	}
}

func (a *Admin) jsonError(w http.ResponseWriter, r *http.Request, status int, message string) {
	a.writeJSON(w, r, status, map[string]string{"error": message})
}

func (a *Admin) jsonServerError(w http.ResponseWriter, r *http.Request, stage string, err error) {
	LoggerFromContext(r.Context()).Error("pgdesk: "+stage,
		"error", err, "request_id", RequestIDFromContext(r.Context()))
	a.jsonError(w, r, http.StatusInternalServerError, "Internal server error.")
}

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
