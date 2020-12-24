package ice

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
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

//TODO: RTO
type STUNTransaction struct {
	Message *stun.Message
	Base Base
	Target net.Addr
	Timeout time.Duration
	StringId string

	resultChannel chan *STUNResult
}

type STUNResult struct {
	Response *stun.Message
	Error    error
}

func stunEncodeTrxId(data [stun.TransactionIDSize]byte) string {
	return hex.EncodeToString(data[:]);
}

//this timeout accounts for both IO and pacing enqueue (that is, whatever is the reason for timeout)
func executeStun(base Base, message *stun.Message, target net.Addr, timeout time.Duration) (*stun.Message, error) {
	trx := &STUNTransaction{
		Message:       message,
		Base:          base,
		Target:		   target,
		Timeout:	   timeout,
		StringId:	   stunEncodeTrxId(message.TransactionID),

		resultChannel: make(chan *STUNResult, 1), //make it buffered to avoid recv loop locking in case of misbehaving receiver
	}

	return trx.executePacedSync()
}

func (trx *STUNTransaction) waitForResult() (*stun.Message, error) {
	timeout := trx.Timeout

	timer := time.NewTimer(timeout)

	select {
	case res, ok := <- trx.resultChannel:
		if !ok {
			return nil, fmt.Errorf("transaction cancelled")
		}

		return res.Response, nil
	case <- timer.C:
		trx.cancel() //cleanup agent queue in case it was never dispatched (maybe queue was too long or sth)
		return nil, fmt.Errorf("timeout reached")
	}
}

// enqueue the trx and blocking-wait for result
func (trx *STUNTransaction) executePacedSync() (*stun.Message, error) {
	base := trx.Base

	base.Component().Stream.Agent.stunPacer.trxEnqueue(trx)

	return trx.waitForResult()
}

// enqueue the trx and wait for result in a separate goroutine
func (trx *STUNTransaction) executePacedAsync(handler func(*stun.Message, error)) {
	go func() {
		msg, err := trx.executePacedSync()
		handler(msg,err)
	}()
}

// put directly on dispatched map and execute
func (trx *STUNTransaction) executeImmediatelySync() (*stun.Message, error) {
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

		a.stunPacer.trxFailed(trx.StringId)

		trx.resultChannel <- &STUNResult{
			Response: nil,
			Error:    err,
		}
	}
}

// execute the trx and wait for result in a separate goroutine
func (trx *STUNTransaction) executeImmediatelyAsync(handler func(*stun.Message, error)) {
	go func() {
		msg, err := trx.executeImmediatelySync()
		handler(msg,err)
	}()
}

func (trx *STUNTransaction) cancel() {
	strId := trx.StringId

	trx.Base.Component().Stream.Agent.stunPacer.trxCancel(strId)
}

func stunBind(base Base, serverAddr net.Addr, deadline time.Duration) (*stun.XORMappedAddress, error) {
	//stun.TransactionID is a default setter that will generate the random trx id for us
	req, err := stun.Build(stun.BindingRequest, stun.TransactionID)

	if err != nil {
		return nil, err
	}

	res, err := executeStun(base, req, serverAddr, deadline)

	if err != nil {
		return nil, err
	}

	var addr stun.XORMappedAddress
	if err = addr.GetFrom(res); err != nil {
		return nil, fmt.Errorf("failed to get XOR-MAPPED-ADDRESS response: %v", err)
	}
	return &addr, nil
}