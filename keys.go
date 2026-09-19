package pgdesk

import (
	"fmt"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

type Keys struct {
	cols []*introspect.Column
	vals [][]any
}

func (k Keys) Len() int { return len(k.vals) }

func (k Keys) Int64s() ([]int64, error) {
	col, err := k.single("Int64s", introspect.CatNumeric)
	if err != nil {
		return nil, err
	}
	out := make([]int64, len(k.vals))
	for i, tuple := range k.vals {
		n, ok := tuple[0].(int64)
		if !ok {
			return nil, fmt.Errorf("pgdesk: Keys.Int64s: key %q value is %T, not an integer: %w", col.Name, tuple[0], ErrKeyTypeMismatch)
		}
		out[i] = n
	}
	return out, nil
}

func (k Keys) Strings() ([]string, error) {
	col, err := k.single("Strings", introspect.CatText, introspect.CatUUID, introspect.CatEnum)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(k.vals))
	for i, tuple := range k.vals {
		s, ok := tuple[0].(string)
		if !ok {
			return nil, fmt.Errorf("pgdesk: Keys.Strings: key %q value is %T, not a string: %w", col.Name, tuple[0], ErrKeyTypeMismatch)
		}
		out[i] = s
	}
	return out, nil
}

func (k Keys) Raw() [][]any { return k.vals }

func (k Keys) Column(name string) ([]any, error) {
	idx := -1
	for i, c := range k.cols {
		if c.Name == name {
			idx = i
			break
		}
	}
	if idx == -1 {
		names := make([]string, len(k.cols))
		for i, c := range k.cols {
			names[i] = c.Name
		}
		return nil, fmt.Errorf("pgdesk: Keys.Column: %q is not a key column (have %v)", name, names)
	}
	out := make([]any, len(k.vals))
	for i, tuple := range k.vals {
		out[i] = tuple[idx]
	}
	return out, nil
}

func (k Keys) single(method string, allow ...introspect.TypeCategory) (*introspect.Column, error) {
	if len(k.cols) != 1 {
		return nil, fmt.Errorf("pgdesk: Keys.%s needs a single-column key, but the resource key has %d columns: %w; use Raw", method, len(k.cols), ErrKeyShapeMismatch)
	}
	col := k.cols[0]
	for _, c := range allow {
		if col.Category == c {
			return col, nil
		}
	}
	return nil, fmt.Errorf("pgdesk: Keys.%s: key column %q is %s: %w; use a matching accessor or Raw", method, col.Name, col.DataType, ErrKeyTypeMismatch)
}
