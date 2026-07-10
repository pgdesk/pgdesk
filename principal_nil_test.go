package pgdesk

import (
	"context"
	"testing"
)

// nilProbePrincipal has pointer-receiver methods that panic on a nil receiver.
// Attaching (*nilProbePrincipal)(nil) as a Principal wraps a nil pointer in a
// non-nil interface; if any fail-closed gate ever mistook it for an authenticated
// operator, SubjectID or DisplayName would run on the nil receiver and panic. A
// clean pass therefore proves the guard prevented the call.
type nilProbePrincipal struct{ id string }

func (p *nilProbePrincipal) SubjectID() string {
	if p == nil {
		panic("SubjectID called on nil principal")
	}
	return p.id
}

func (p *nilProbePrincipal) DisplayName() string {
	if p == nil {
		panic("DisplayName called on nil principal")
	}
	return p.id
}

// A typed-nil pointer principal must be indistinguishable from an absent one: both
// deny. PrincipalFromContext is the single normalization point, so this table
// covers every shape of "who is this" that a host can hand us.
func TestPrincipalFromContextNormalizesNil(t *testing.T) {
	real := &nilProbePrincipal{id: "alice"}

	cases := []struct {
		name   string
		ctx    context.Context
		wantP  Principal
		absent bool
	}{
		{
			name:   "typed-nil pointer principal is treated as absent",
			ctx:    WithPrincipal(context.Background(), (*nilProbePrincipal)(nil)),
			absent: true,
		},
		{
			name:   "genuine nil: no WithPrincipal call",
			ctx:    context.Background(),
			absent: true,
		},
		{
			name:  "real non-nil principal is returned as-is",
			ctx:   WithPrincipal(context.Background(), real),
			wantP: real,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PrincipalFromContext(tc.ctx)
			if tc.absent {
				if got != nil {
					t.Fatalf("PrincipalFromContext() = %#v, want nil (absent)", got)
				}
				return
			}
			if got != tc.wantP {
				t.Fatalf("PrincipalFromContext() = %#v, want %#v", got, tc.wantP)
			}
		})
	}
}

// A typed-nil pointer principal must be denied by the authorization choke point,
// even under AllowAll. If the guard failed, permitted would either call a method
// on the nil receiver (panic) or wrongly report authorized (true).
func TestPermittedDeniesTypedNilPrincipal(t *testing.T) {
	ctx := WithPrincipal(context.Background(), (*nilProbePrincipal)(nil))

	attrs := Attributes{
		Principal:  PrincipalFromContext(ctx),
		Capability: CapList,
		Resource:   "users",
	}

	ok, err := permitted(ctx, AllowAll, attrs)
	if err != nil {
		t.Fatalf("permitted() error = %v, want nil", err)
	}
	if ok {
		t.Fatal("permitted() = true for a typed-nil principal, want false (fail closed)")
	}
}
