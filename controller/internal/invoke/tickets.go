package invoke

import (
	"errors"
	"time"

	"github.com/antiartificial/aurora/controller/internal/prov"
	"github.com/antiartificial/aurora/controller/ticket"
)

// Tickets verifies and redeems powerbox grant tickets.
type Tickets struct {
	Key    []byte
	Now    func() time.Time // default time.Now
	ledger ticket.Ledger
}

func (t *Tickets) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// checkTickets verifies the request's tickets against the planned grants and
// reclassifies the grants they authorise as powerbox. It spends nothing: the
// returned redeem func spends the tickets, and is called only once every other
// check has passed, so a request refused for another reason does not burn them.
// A ticket that does not verify is an error, never a silent downgrade to a
// gesture. A valid ticket outranks a manifest declaration: it is the more
// specific authority, and the record says so.
func (iv *Invoker) checkTickets(req Request, app string, grants []prov.Grant) (func() *Error, *Error) {
	none := func() *Error { return nil }
	var claims []ticket.Claims
	for _, c := range []struct{ tok, mode, what string }{{req.Ticket, "ro", "input"}, {req.OutputTicket, "rw", "output"}} {
		if c.tok == "" {
			continue
		}
		if iv.Tickets == nil {
			return nil, fail("ticket_unavailable", "this controller has no powerbox key, so it cannot verify tickets", "")
		}
		gi := -1
		for i, g := range grants {
			if g.Mode == c.mode {
				gi = i
			}
		}
		if gi < 0 {
			return nil, fail("bad_request", "a %s ticket was given but this verb has no %s grant", "", c.what, c.what)
		}
		cl, err := ticket.Verify(iv.Tickets.Key, c.tok, iv.Tickets.now())
		if err == nil {
			err = cl.Check(ticket.Want{Path: grants[gi].Path, Mode: grants[gi].Mode, App: app, Verb: req.Verb})
		}
		if err != nil {
			var te *ticket.Error
			errors.As(err, &te)
			return nil, fail(te.Code, "%s ticket refused: %s", te.Msg, c.what, te.Msg)
		}
		grants[gi].Source = prov.SourcePowerbox
		claims = append(claims, cl)
	}
	if len(claims) == 0 {
		return none, nil
	}
	return func() *Error {
		if err := iv.Tickets.ledger.Redeem(iv.Tickets.now(), claims...); err != nil {
			var te *ticket.Error
			errors.As(err, &te)
			return fail(te.Code, "ticket refused: %s", te.Msg, te.Msg)
		}
		return nil
	}, nil
}
