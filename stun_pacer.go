package ice

import (
	"github.com/pion/logging"
	"github.com/pion/stun"
	"net"
	"sync"
	"time"
)

type stunPacingChangedEvent struct {
	pacingMs int
}
type poisonPill struct {}

type stunPacer struct {
	agent				*Agent
	trxQueue			[]*STUNTransaction
	trxDispatched		map[string]*STUNTransaction

	pacingMs			int
	pacingTicker		*time.Ticker

	events				EventChannel
	started				bool

	log 				logging.LeveledLogger
	mux					sync.Mutex
}

func newStunPacer(agent *Agent, pacing int) *stunPacer {
	return &stunPacer{
		agent:			agent,
		trxQueue: 		[]*STUNTransaction{},
		trxDispatched: 	make(map[string]*STUNTransaction),
		pacingMs:		pacing,

		events:			make(EventChannel),
		started:		false,
		log:			agent.log,
		mux:			sync.Mutex{},
	}
}

func (sp *stunPacer) setPacing(pacing int) {
	sp.mux.Lock()
	defer sp.mux.Unlock()

	sp.pacingMs = pacing
	sp.events <- stunPacingChangedEvent{}
}

func (sp *stunPacer) getPacing() int {
	sp.mux.Lock()
	defer sp.mux.Unlock()

	return sp.pacingMs
}

func (sp *stunPacer) start() {
	sp.mux.Lock()
	defer sp.mux.Unlock()

	if !sp.started {
		sp.pacingTicker = time.NewTicker(time.Duration(sp.pacingMs) * time.Millisecond)
		sp.started = true
		go sp.eventLoop()
	}
}

func (sp *stunPacer) close() {
	sp.events <- poisonPill{}
}

func (sp *stunPacer) eventLoopStopped() {
	sp.mux.Lock()
	defer sp.mux.Unlock()

	sp.started = false
}

func (sp *stunPacer) eventLoop() {
	defer sp.eventLoopStopped()

	for {
		select {
		case <- sp.pacingTicker.C:
			sp.onPacing()
			break
		case untypedEvt := <- sp.events:
			switch typedEvt := untypedEvt.(type) {
			case stunPacingChangedEvent:
				sp.log.Infof("changing pacing to: %dms", typedEvt.pacingMs)

				sp.pacingTicker.Stop()
				sp.pacingTicker = time.NewTicker(time.Duration(typedEvt.pacingMs) * time.Millisecond)
				break
			case poisonPill:
				sp.log.Infof("stopping pacer as per poison pill received")
				return
			default:
				sp.log.Errorf("recv unknown message type")
				break
			}
		}
	}
}

func (sp *stunPacer) onPacing() {
	sp.mux.Lock()

	if len(sp.trxQueue) > 0 {
		trx := sp.trxQueue[0]

		sp.trxQueue = sp.trxQueue[1:]
		sp.trxDispatched[trx.StringId] = trx

		sp.mux.Unlock()
		trx.setState(TrxStateInProgress)
		go trx.transmit()
	} else {
		sp.mux.Unlock()
	}

	//execute out of our mutex control - it's their business how they want to synchronize
	sp.agent.onPacing()
}

func (sp *stunPacer) onInboundStun(base Base, message *stun.Message, remote net.Addr) {
	trxId := stunEncodeTrxId(message.TransactionID)

	switch message.Type.Class {
	case stun.ClassSuccessResponse, stun.ClassErrorResponse:
		sp.mux.Lock()

		trx, ok := sp.trxDispatched[trxId]

		if !ok {
			sp.log.Warnf("received response for non-existent trx: %s->%s, trxId=%s", remote, base.Address(), trxId)
			sp.mux.Unlock()
			return
		}

		delete(sp.trxDispatched, trxId)
		sp.mux.Unlock()

		sp.log.Debugf("received response for trxId=%s", trxId)

		trx.setState(TrxStateSuccess)

		trx.resultChannel <- &STUNResult{
			Response: message,
			Error:    nil,
		}

		break
	case stun.ClassIndication, stun.ClassRequest:
		//maybe run in goroutine for extra safety?
		base.Component().recvStunRequest(base, remote, message)
	}
}

func (sp *stunPacer) trxEnqueue(trx *STUNTransaction) {
	sp.mux.Lock()
	defer sp.mux.Unlock()

	trx.setState(TrxStateQueued)
	sp.trxQueue = append(sp.trxQueue, trx)
}

func (sp *stunPacer) trxExecuteNow(trx *STUNTransaction) {
	sp.mux.Lock()
	defer sp.mux.Unlock()

	trx.setState(TrxStateInProgress)
	sp.trxDispatched[trx.StringId] = trx
	go trx.transmit()
}

//this is a hack to avoid direct state update from side effect
func (sp *stunPacer) trxUndispatch(trxId string) {
	sp.mux.Lock()
	defer sp.mux.Unlock()

	delete(sp.trxDispatched, trxId)
}

func (sp *stunPacer) trxDequeue(trxId string) {
	sp.mux.Lock()
	defer sp.mux.Unlock()

	delete(sp.trxDispatched, trxId)

	for i, trx := range sp.trxQueue {
		if trx.StringId == trxId {
			//this is a bit costly: https://stackoverflow.com/questions/37334119/how-to-delete-an-element-from-a-slice-in-golang
			sp.trxQueue = append(sp.trxQueue[:i], sp.trxQueue[i+1:]...)
			close(trx.resultChannel) //this notifies the receiver that trx was cancelled
			return
		}
	}
}

//TODO: maybe build some tree-based queue that would both allow searching on trxId and keeping ordering according to enqueue order
func (sp *stunPacer) trxCancel(trxId string) {
	sp.mux.Lock()
	defer sp.mux.Unlock()

	delete(sp.trxDispatched, trxId)

	for i, trx := range sp.trxQueue {
		if trx.StringId == trxId {
			//this is a bit costly: https://stackoverflow.com/questions/37334119/how-to-delete-an-element-from-a-slice-in-golang
			sp.trxQueue = append(sp.trxQueue[:i], sp.trxQueue[i+1:]...)
			close(trx.resultChannel) //this notifies the receiver that trx was cancelled

			sp.log.Debugf("STUN transaction was explicitly cancelled: %s", trxId)
			return
		}
	}

	sp.log.Warnf("requested to cancel non-existent STUN transaction: %s", trxId)
}