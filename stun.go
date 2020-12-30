package ice

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pion/stun"
)

// bin is shorthand for BigEndian.
var bin = binary.BigEndian

func assertInboundUsername(m *stun.Message, expectedUsername string) error {
	var username stun.Username
	if err := username.GetFrom(m); err != nil {
		return err
	}
	if string(username) != expectedUsername {
		return fmt.Errorf("username mismatch expected(%x) actual(%x)", expectedUsername, string(username))
	}

	return nil
}

func assertInboundMessageIntegrity(m *stun.Message, key []byte) error {
	messageIntegrityAttr := stun.MessageIntegrity(key)
	return messageIntegrityAttr.Check(m)
}

type TrxState int

const (
	TrxStateNew TrxState = iota + 1
	TrxStateQueued
	TrxStateInProgress
	TrxStateCancelled
	TrxStateFailed
	TrxStateSuccess
)

func (c TrxState) String() string {
	switch c {
	case TrxStateNew:
		return "new"
	case TrxStateQueued:
		return "queued"
	case TrxStateInProgress:
		return "in-progress"
	case TrxStateCancelled:
		return "cancel"
	case TrxStateFailed:
		return "failed"
	case TrxStateSuccess:
		return "success"
	}
	return "Unknown trx state"
}

//TODO: RTO
type STUNTransaction struct {
	Message *stun.Message
	Base Base
	Target net.Addr
	Timeout time.Duration
	RTO time.Duration
	StringId string

	softFail	  bool
	state         TrxState
	pacer         *stunPacer
	mux           sync.Mutex
	resultChannel chan *STUNResult
}

type STUNResult struct {
	Response *stun.Message
	Error    error
}

func stunEncodeTrxId(data [stun.TransactionIDSize]byte) string {
	return hex.EncodeToString(data[:])
}

/* During the ICE gathering phase, ICE agents SHOULD calculate the RTO
   value using the following formula:

     RTO = MAX (500ms, Ta * (Num-Of-Cands))

     Num-Of-Cands: the number of server-reflexive and relay candidates

 */
func newStunTransactionGathering(base Base, message *stun.Message, target net.Addr) (*STUNTransaction, error) {
	rto := 500 * time.Millisecond //TODO: proper RTO calculation to be done later

	return newStunTransaction(base, message, target, rto)
}

func newStunTransactionCheck(base Base, message *stun.Message, target net.Addr) (*STUNTransaction, error) {
	rto := 500 * time.Millisecond //just set at 500 - it's a bit more aggressive but spec allows it

	return newStunTransaction(base, message, target, rto)
}

func newStunTransaction(base Base, message *stun.Message, target net.Addr, RTO time.Duration) (*STUNTransaction, error) {
	trx := &STUNTransaction{
		Message:  message,
		Base:     base,
		Target:   target,
		RTO:      RTO,
		Timeout:  2 * RTO, //Let HTO be the transaction timeout, which SHOULD be 2*RTT
		StringId: stunEncodeTrxId(message.TransactionID),
		state:    TrxStateNew,
		pacer:    base.Component().Stream.Agent.stunPacer,

		resultChannel: make(chan *STUNResult, 1), //make it buffered to avoid recv loop locking in case of misbehaving receiver
	}

	return trx, nil
}

func (trx *STUNTransaction) setSoftFail(softFail bool) {
	trx.mux.Lock()
	defer trx.mux.Unlock()

	trx.softFail = softFail
}

func (trx *STUNTransaction) GetSoftFail() bool {
	trx.mux.Lock()
	defer trx.mux.Unlock()

	return trx.softFail
}

func (trx *STUNTransaction) setState(state TrxState) {
	trx.mux.Lock()
	defer trx.mux.Unlock()

	trx.state = state
}

func (trx *STUNTransaction) GetState() TrxState {
	trx.mux.Lock()
	defer trx.mux.Unlock()

	return trx.state
}


func (trx *STUNTransaction) waitForResult() (*stun.Message, error) {
	timeout := trx.Timeout

	timeoutTimer := time.NewTimer(timeout)
	rtoTimer := time.NewTimer(trx.RTO)

	comp := trx.Base.Component()

	for {
		select {
		case res, ok := <-trx.resultChannel:
			if !ok {
				trx.setState(TrxStateCancelled)
				return nil, fmt.Errorf("transaction cancelled")
			}

			return res.Response, nil
		case <-rtoTimer.C:
			state := trx.GetState()
			softFail := trx.GetSoftFail()

			switch state {
			case TrxStateInProgress:
				if softFail {
					comp.log.Debugf("trxId=%s - no retransmitting due to softfail flag set", trx.StringId)
				} else {
					comp.log.Debugf("trxId=%s - retransmitting due to RTO timer", trx.StringId)
					go trx.transmit()
				}

				break
			default:
				comp.log.Debugf("trxId=%s - RTO timer reached but wrong state - returning error")
				trx.pacer.trxDequeue(trx.StringId) //it might still be in queue
				return nil, fmt.Errorf("unexpected state during RTO: %s", state)
			}

		case <-timeoutTimer.C:
			trx.pacer.trxDequeue(trx.StringId) //cleanup agent queue in case it was never dispatched (maybe queue was too long or sth)
			trx.setState(TrxStateFailed)
			return nil, fmt.Errorf("timeout reached")
		}
	}
}

// enqueue the trx and blocking-wait for result
func (trx *STUNTransaction) ExecutePacedSync() (*stun.Message, error) {
	base := trx.Base

	base.Component().Stream.Agent.stunPacer.trxEnqueue(trx)

	return trx.waitForResult()
}

// enqueue the trx and wait for result in a separate goroutine
func (trx *STUNTransaction) ExecutePacedAsync(handler func(*stun.Message, error)) {
	go func() {
		msg, err := trx.ExecutePacedSync()
		handler(msg,err)
	}()
}

// put directly on dispatched map and execute
func (trx *STUNTransaction) ExecuteImmediatelySync() (*stun.Message, error) {
	base := trx.Base

	base.Component().Stream.Agent.stunPacer.trxExecuteNow(trx)

	return trx.waitForResult()
}

func (trx *STUNTransaction) transmit() {
	trx.Message.Encode()

	a := trx.Base.Component().Stream.Agent

	a.log.Debugf("sending STUN trxId=%s %s:%d->%s", trx.StringId, trx.Base.Address(), trx.Base.Port(), trx.Target)
	_, err := trx.Base.write(trx.Message.Raw, trx.Target)

	if err != nil {
		a.log.Debugf("IO failed for STUN trxId=%s: %v", trx.StringId, err)

		trx.pacer.trxUndispatch(trx.StringId)
		trx.setState(TrxStateFailed)

		trx.resultChannel <- &STUNResult{
			Response: nil,
			Error:    err,
		}
	}
}

// execute the trx and wait for result in a separate goroutine
func (trx *STUNTransaction) ExecuteImmediatelyAsync(handler func(*stun.Message, error)) {
	go func() {
		msg, err := trx.ExecuteImmediatelySync()
		handler(msg,err)
	}()
}

func (trx *STUNTransaction) cancel() {
	strId := trx.StringId

	trx.Base.Component().Stream.Agent.stunPacer.trxCancel(strId)
}

func sendGatheringBind(base Base, serverAddr net.Addr) (*stun.XORMappedAddress, error) {
	//stun.TransactionID is a default setter that will generate the random trx id for us
	req, err := stun.Build(stun.BindingRequest, stun.TransactionID)

	if err != nil {
		return nil, err
	}

	trx, err := newStunTransactionGathering(base, req, serverAddr)

	if err != nil {
		return nil, err
	}

	res, err := trx.ExecutePacedSync()

	if err != nil {
		return nil, err
	}

	var addr stun.XORMappedAddress
	if err = addr.GetFrom(res); err != nil {
		return nil, fmt.Errorf("failed to get XOR-MAPPED-ADDRESS response: %v", err)
	}
	return &addr, nil
}