package clientcore

import (
	"sync/atomic"

	"github.com/getlantern/broflake/common"
)

// activation holds the ID of the current stretch of proxy mode, from
// start to stop. Every egress slot sends it on each WebSocket it opens,
// so the egress can count one activation however many consumers the
// widget serves and however often they churn.
type activation struct {
	cur atomic.Pointer[string]
}

// begin mints a new ID unless one is already active. Called on start,
// so a new activation starts only after end, and a repeated start
// keeps counting as the same one.
func (a *activation) begin() {
	if a == nil {
		return
	}
	id := common.NewActivationID()
	a.cur.CompareAndSwap(nil, &id)
}

// end clears the ID. A slot that dials after stop sends none, which the
// egress serves normally and does not count.
func (a *activation) end() {
	if a == nil {
		return
	}
	a.cur.Store(nil)
}

// id returns the current ID, or "" outside an activation.
func (a *activation) id() string {
	if a == nil {
		return ""
	}
	if p := a.cur.Load(); p != nil {
		return *p
	}
	return ""
}
