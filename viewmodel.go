package pgdesk

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
	Level   string
	Message string
}

type resourceMeta struct {
	Name        string
	Label       string
	LabelPlural string
	Description string
}

type resourceNav struct {
	Name        string
	LabelPlural string
}

type indexView struct {
	Base      baseView
	Resources []resourceNav
}

type sortHeader struct {
	Label  string
	URL    string
	Active bool
	Desc   bool
}

type cellView struct {
	Value any
	Label string
	Link  string
}

type rowView struct {
	Cells []cellView
	Key   string
}

type actionMeta struct {
	Name    string
	Label   string
	Confirm string
}

type filterField struct {
	Label      string
	Kind       string
	ParamKey   string
	ParamKeyTo string
	Value      string
	ValueTo    string
	Options    []string
}

type filterChip struct {
	Label     string
	Value     string
	RemoveURL string
}

type listView struct {
	Base            baseView
	Resource        resourceMeta
	SearchEnabled   bool
	Query           string
	Headers         []sortHeader
	Rows            []rowView
	HasDetail       bool
	CanCreate       bool
	Actions         []actionMeta
	HasActions      bool
	ExportURL       string
	InlineFilters   []filterField
	OverflowFilters []filterField
	HasOverflow     bool
	ActiveChips     []filterChip
	ActiveCount     int
	HasFilters      bool
	Page            int
	HasPrev         bool
	HasNext         bool
	PrevURL         string
	NextURL         string
}

type detailField struct {
	Label   string
	Value   any
	FKLabel string
	FKLink  string
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
	ValueString  string
	CurrentValue any
	Error        string
	Readonly     bool
	Required     bool
	Widget       string
	Checked      bool
	Options      []string

	Ref string

	ValueLabel string
}

type formView struct {
	Base      baseView
	Resource  resourceMeta
	Action    string
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
