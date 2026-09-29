package pgdesk

// Widget is the form input used for a column.
type Widget string

// Widgets for Resource.Widget. WidgetAuto chooses from the column type.
const (
	WidgetAuto     Widget = ""
	WidgetText     Widget = "text"
	WidgetTextarea Widget = "textarea"
	WidgetCheckbox Widget = "checkbox"
	WidgetSelect   Widget = "select"
	WidgetFK       Widget = "fk"
	WidgetJSON     Widget = "json"
	WidgetDateTime Widget = "datetime"
)

type fieldConfig struct {
	label    string
	readonly bool
	hidden   bool
	required bool
	redact   bool
	widget   Widget
}
