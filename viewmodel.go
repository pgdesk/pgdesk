package pgdesk

// View models passed to templates. They contain only display-ready values; all
// dynamic values are escaped by html/template at render time (F1). Keeping these
// as explicit structs (no reflection in the request path) makes the data that
// reaches a template auditable.

type baseView struct {
	Title     string
	SiteTitle string
	BasePath  string
	Principal string
	Nav       []navItem
	Flash     []flashMsg
}

type navItem struct {
	Label  string
	URL    string
	Active bool
}

type flashMsg struct {
	Level   string // "info" | "error"
	Message string
}

type resourceMeta struct {
	Name        string
	Label       string
	LabelPlural string
}

type resourceNav struct {
	Name        string
	LabelPlural string
}

type indexView struct {
	Base      baseView
	Resources []resourceNav
}

// sortHeader is a clickable list column header carrying the URL that applies (or
// toggles) sorting by that column while preserving the other query parameters.
type sortHeader struct {
	Label  string
	URL    string
	Active bool
	Desc   bool
}

// cellView is one rendered list cell. For a foreign-key column, Label holds the
// batched-lookup label (D4) and Link the referenced detail URL (empty when the
// referenced table is not a registered resource).
type cellView struct {
	Value any
	Label string
	Link  string
}

type rowView struct {
	Cells []cellView
	Key   string
}

// filterField is one control in the list filter form.
type actionMeta struct {
	Name    string
	Label   string
	Confirm string
}

type filterField struct {
	Label      string
	Kind       string // "select" | "bool" | "text" | "daterange"
	ParamKey   string
	ParamKeyTo string   // second param for "daterange"
	Value      string   // current value
	ValueTo    string   // current 'to' value for "daterange"
	Options    []string // for "select"/"bool"
}

type listView struct {
	Base          baseView
	Resource      resourceMeta
	SearchEnabled bool
	Query         string
	Headers       []sortHeader
	Rows          []rowView
	HasDetail     bool
	CanCreate     bool
	Actions       []actionMeta
	HasActions    bool
	ExportURL     string
	Filters       []filterField
	HasFilters    bool
	Page          int
	HasPrev       bool
	HasNext       bool
	PrevURL       string
	NextURL       string
}

type detailField struct {
	Label string
	Value any
}

type detailView struct {
	Base      baseView
	Resource  resourceMeta
	Key       string
	CanEdit   bool
	CanDelete bool
	Fields    []detailField
}

type formField struct {
	Name         string
	Label        string
	Value        any
	ValueString  string // current value as a string, for <select> matching
	CurrentValue any    // populated on a concurrent-edit conflict (O1)
	Error        string
	Readonly     bool
	Required     bool
	Widget       string
	Checked      bool
	Options      []string
}

type formView struct {
	Base      baseView
	Resource  resourceMeta
	Action    string // form POST target (…/{key}/edit or …/new)
	IsCreate  bool
	Key       string
	Version   string
	Conflict  bool
	FormError string
	Fields    []formField
}

type errorView struct {
	Base       baseView
	Status     int
	StatusText string
	Message    string
	RequestID  string
}
