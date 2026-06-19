package pgdesk

// Widget is a hint for how a field should be rendered in a form. It is advisory;
// the renderer falls back to a type-appropriate default when unset.
type Widget string

const (
	WidgetAuto     Widget = ""         // choose based on column type
	WidgetText     Widget = "text"     // single-line text input
	WidgetTextarea Widget = "textarea" // multi-line text
	WidgetCheckbox Widget = "checkbox" // boolean
	WidgetSelect   Widget = "select"   // enum / FK
	WidgetJSON     Widget = "json"     // JSON editor (bootstrapped safely, F1)
	WidgetDateTime Widget = "datetime" // timestamp picker
)

// fieldConfig holds per-column display overrides accumulated on a Resource. It
// is unexported; operators configure it through Resource setter methods. Zero
// values mean "use the introspected default."
type fieldConfig struct {
	label    string
	readonly bool
	hidden   bool
	required bool // required beyond NOT NULL (UX pre-flight only, D7)
	redact   bool // omit value from audit before/after snapshots (O4)
	widget   Widget
}
