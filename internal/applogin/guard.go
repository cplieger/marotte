package applogin

import (
	"context"
	"slices"

	"github.com/cplieger/access"
	"github.com/cplieger/access/account"
)

// usable holds the method-level facts only marotte knows: the method's allowance, and for OIDC a
// valid canonical origin and an attached provider of the identity's issuer. The library counts
// every other arm.
func usable(s *snapshot, rpValid bool, u *access.User, m access.Method) bool {
	switch m {
	case access.MethodPassword:
		return s.policy.Methods.Password
	case access.MethodPasskey:
		return s.policy.Methods.Passkey
	case access.MethodOIDC:
		return s.policy.Methods.OIDC && rpValid && s.oidc != nil && !s.policy.Provider.isZero() &&
			u != nil && u.OIDC.Issuer == s.policy.Provider.Issuer
	}
	return false
}

// methodUsable is Rules.MethodUsable. It reads only the tx the library passes: inside a job, the
// job's private copy, so a removal grouped after an allowance-off or a provider removal counts it;
// the store itself reads the published state; any other tx fails closed.
func (s *Store) methodUsable(_ context.Context, tx account.Tx, u *access.User, m access.Method) bool {
	switch v := tx.(type) {
	case *txView:
		return v.st == s && usable(v.s, s.rpValid.Load(), u, m)
	case *Store:
		return v == s && usable(s.cur.Load(), s.rpValid.Load(), u, m)
	}
	return false
}

// SetOriginValid records whether the canonical origin is valid, which the OIDC arm of usable reads.
func (s *Store) SetOriginValid(valid bool) { s.rpValid.Store(valid) }

type restoreKey struct{}

// WithRestoreAllowance marks a write that restores access with method m: when its callback
// returns nil having written that credential for the owner, the writer turns m's allowance on in
// the same publication.
func WithRestoreAllowance(ctx context.Context, m access.Method) context.Context {
	return context.WithValue(ctx, restoreKey{}, m)
}

func restoreAllowanceFrom(ctx context.Context) access.Method {
	m, _ := ctx.Value(restoreKey{}).(access.Method)
	return m
}

func applyRestore(tx *txView, m access.Method) error {
	owner := tx.s.owner()
	if owner == nil || !slices.Contains(tx.creds, credWrite{method: m, userID: owner.ID}) {
		return nil
	}
	p := tx.s.policy.clone()
	switch m {
	case access.MethodPassword:
		if p.Methods.Password {
			return nil
		}
		p.Methods.Password = true
	case access.MethodPasskey:
		if p.Methods.Passkey {
			return nil
		}
		p.Methods.Passkey = true
	default:
		return nil
	}
	if err := bumpGeneration(&p); err != nil {
		return err
	}
	tx.setPolicy(&p)
	return nil
}

// checkGuard refuses an own write after which the owner has no password, no passkey and no linked
// and enabled OIDC identity, counted by the library over the job's private copy.
func checkGuard(tx *txView, j *job) error {
	owner := tx.s.owner()
	if owner == nil {
		return nil
	}
	st := account.MethodState{Rules: j.guard.rules(tx.s), RPID: j.guard.rpID}
	if tx.s.oidc != nil {
		st.OIDCIssuer = tx.s.policy.Provider.Issuer
	}
	avail, err := j.guard.count(j.ctx, tx, owner.ID, st)
	if err != nil {
		return err
	}
	if !access.CanDisableMethod("", avail) {
		return ErrNoWorkingMethod
	}
	return nil
}
