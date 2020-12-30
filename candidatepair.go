package ice

import (
	"fmt"
	"github.com/pion/stun"
	"net"
	"sync"
)

// CandidatePairState represent the ICE candidate pair state
type CandidatePairState int

const (
	// CandidatePairStateWaiting means a check has not been performed for
	// this pair
	CandidatePairStateWaiting CandidatePairState = iota + 1

	// CandidatePairStateInProgress means a check has been sent for this pair,
	// but the transaction is in progress.
	CandidatePairStateInProgress

	// CandidatePairStateFailed means a check for this pair was already done
	// and failed, either never producing any response or producing an unrecoverable
	// failure response.
	CandidatePairStateFailed

	// CandidatePairStateSucceeded means a check for this pair was already
	// done and produced a successful result.
	CandidatePairStateSucceeded

	// CandidatePairStateFrozen A check for this pair has not been sent, and it cannot be
	// sent until the pair is unfrozen and moved into the Waiting state.
	CandidatePairStateFrozen
)

func (c CandidatePairState) String() string {
	switch c {
	case CandidatePairStateWaiting:
		return "waiting"
	case CandidatePairStateInProgress:
		return "in-progress"
	case CandidatePairStateFailed:
		return "failed"
	case CandidatePairStateSucceeded:
		return "succeded"
	case CandidatePairStateFrozen:
		return "frozen"
	}
	return "Unknown candidate pair state"
}

type CandidatePair struct {
	local	*LocalCandidate
	remote	*Candidate
	state	CandidatePairState
	priority uint64

	// https://tools.ietf.org/html/rfc8445#section-6.1.2.6
	// Each candidate pair in the checklist has a foundation (the
	// combination of the foundations of the local and remote candidates in
	// the pair)
	foundation string

	checkTrx	*STUNTransaction

	mux		sync.Mutex
}

func (cp *CandidatePair) String() string {
	return fmt.Sprintf("local=%s, remote=%s, priority=%d, foundation: %s, state: %s", cp.local.String(), cp.remote.String(), cp.priority, cp.foundation, cp.state)
}

func newCandidatePair(local *LocalCandidate, remote *Candidate) *CandidatePair {
	ret := &CandidatePair{
		local:      local,
		remote:     remote,
		state:      CandidatePairStateFrozen,
		priority:   0,
		foundation: fmt.Sprintf("%s%s", local.Foundation, remote.Foundation),
		mux:		sync.Mutex{},
	}

	ret.priority = ret.calculatePriority()

	return ret
}

func (cp *CandidatePair) setState(state CandidatePairState) {
	cp.mux.Lock()
	defer cp.mux.Unlock()

	cp.state = state
}

func (cp *CandidatePair) getState() CandidatePairState {
	cp.mux.Lock()
	defer cp.mux.Unlock()

	return cp.state
}

func (cp *CandidatePair) flagSoftFail() {
	cp.mux.Lock()
	defer cp.mux.Unlock()

	if cp.checkTrx != nil {
		cp.checkTrx.setSoftFail(true)
	}
}

func (cp *CandidatePair) stunTrxHandler(trx *STUNTransaction, message *stun.Message, err error) {
	softFail := trx.GetSoftFail()

	if err != nil {
		if softFail {
			//do nothing
		} else {
			cp.setState(CandidatePairStateFailed)
			//do not signal upwards - it will be received during next round of checklisting
		}
	} else {
		cp.setState(CandidatePairStateSucceeded)
	}
}

func (cp *CandidatePair) startCheck() {
	msg, err := stun.Build()

	if err != nil {
		cp.setState(CandidatePairStateFailed)
		return
	}

	addrString := fmt.Sprintf("%s:%d", cp.remote.TransportHost, cp.remote.TransportPort)

	addr, err := net.ResolveUDPAddr("udp", addrString)

	if err != nil {
		cp.setState(CandidatePairStateFailed)
		return
	}

	trx, err := newStunTransactionCheck(cp.local.base, msg, addr)

	if err != nil {
		cp.setState(CandidatePairStateFailed)
		return
	}

	var curriedHandler = func(message *stun.Message, err error) {
		cp.stunTrxHandler(trx, message, err)
	}

	trx.ExecutePacedAsync(curriedHandler)
}

// RFC 5245 - 5.7.2.  Computing Pair Priority and Ordering Pairs
// Let G be the priority for the candidate provided by the controlling
// agent.  Let D be the priority for the candidate provided by the
// controlled agent.
// pair priority = 2^32*MIN(G,D) + 2*MAX(G,D) + (G>D?1:0)
func (cp *CandidatePair) calculatePriority() uint64 {
	agent := cp.local.base.Component().Stream.Agent

	isControlling := agent.isControlling

		var g uint32
		var d uint32
		if isControlling {
			g = cp.local.Priority
			d = cp.remote.Priority
		} else {
			g = cp.remote.Priority
			d = cp.local.Priority
		}

		// Just implement these here rather
		// than fooling around with the math package
		min := func(x, y uint32) uint64 {
			if x < y {
				return uint64(x)
			}
			return uint64(y)
		}
		max := func(x, y uint32) uint64 {
			if x > y {
				return uint64(x)
			}
			return uint64(y)
		}
		cmp := func(x, y uint32) uint64 {
			if x > y {
				return uint64(1)
			}
			return uint64(0)
		}

		// 1<<32 overflows uint32; and if both g && d are
		// maxUint32, this result would overflow uint64
		return (1<<32-1)*min(g, d) + 2*max(g, d) + cmp(g, d)
}
//
//import (
//	"fmt"
//
//	"github.com/pion/stun"
//)
//
//func newCandidatePair(local, remote Candidate, controlling bool) *candidatePair {
//	return &candidatePair{
//		iceRoleControlling: controlling,
//		remote:             remote,
//		local:              local,
//		state:              CandidatePairStateWaiting,
//	}
//}
//
//// candidatePair represents a combination of a local and remote candidate
//type candidatePair struct {
//	iceRoleControlling  bool
//	remote              Candidate
//	local               Candidate
//	bindingRequestCount uint16
//	state               CandidatePairState
//	nominated           bool
//}
//
//func (p *candidatePair) String() string {
//	return fmt.Sprintf("prio %d (local, prio %d) %s <-> %s (remote, prio %d)",
//		p.Priority(), p.local.Priority(), p.local, p.remote, p.remote.Priority())
//}
//
//func (p *candidatePair) Equal(other *candidatePair) bool {
//	if p == nil && other == nil {
//		return true
//	}
//	if p == nil || other == nil {
//		return false
//	}
//	return p.local.Equal(other.local) && p.remote.Equal(other.remote)
//}
//
// RFC 5245 - 5.7.2.  Computing Pair Priority and Ordering Pairs
// Let G be the priority for the candidate provided by the controlling
// agent.  Let D be the priority for the candidate provided by the
// controlled agent.
// pair priority = 2^32*MIN(G,D) + 2*MAX(G,D) + (G>D?1:0)
//func (p *candidatePair) Priority() uint64 {
//}
//
//func (p *candidatePair) Write(b []byte) (int, error) {
//	return p.local.writeTo(b, p.remote)
//}
//
//func (a *Agent) sendSTUN(msg *stun.Response, local, remote Candidate) {
//	_, err := local.writeTo(msg.Raw, remote)
//	if err != nil {
//		a.log.Tracef("failed to send STUN message: %s", err)
//	}
//}
