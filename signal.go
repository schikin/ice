package ice

import "sync"

type SignalSession struct {
	Options            SessionParameters
	SessionCredentials *Credentials
	Streams            []SignalStream
}

type SignalStream struct {
	ID string

	EndOfCandidates bool
	UseTrickle      bool

	StreamCredentials *Credentials
	Components        []SignalComponent
}

type SignalComponent struct {
	ID               uint16
	Candidates       []Candidate
	RelatedComponent uint16
}

type SignalTrickleCandidate struct {
	Candidate Candidate
	StreamId  string
}

type SignalTrickleFinished struct {
	StreamId string
}

type SignalHandler interface {
	HandleDescription(description SignalSession) error
	HandleTrickleCandidate(candidate SignalTrickleCandidate) error
	HandleEndOfCandidates(signal SignalTrickleFinished) error
}

type streamTrickleQueue struct {
	handle SignalHandler
}

type agentTrickleQueue struct {
	handler SignalHandler

	candidates []*SignalTrickleCandidate
	eoc        []*SignalTrickleFinished

	mux sync.Mutex
}

func newTrickleQueue() *trickleQueue {
	return &trickleQueue{
		offer:     make(chan *SignalSession, 1),
		candidate: make(chan *SignalTrickleCandidate, signalCandidatesBufferSize),
		eoc:       make(chan *SignalTrickleFinished, signalEocBufferSize),
	}
}

//func (sq *trickleQueue) flush() ([]*SignalSession, []*SignalTrickleCandidate, []*SignalTrickleFinished) {
//	var candidates []*SignalTrickleCandidate
//	var eocs []*SignalTrickleFinished
//	var offers []*SignalSession
//
//	for {
//		select {
//		case candidate := <-sq.candidate:
//			candidates = append(candidates, candidate)
//			break
//		case eoc := <-sq.eoc:
//			eocs = append(eocs, eoc)
//			break
//		case offer := <-sq.offer:
//			offers = append(offers, offer)
//			break
//		default:
//
//		}
//	}
//
//	return offers, candidates, eocs
//}
//
//type testSignalChannel struct {
//	events      EventChannel
//	localAgent  *Agent
//	remoteAgent *Agent
//}
//
//func (t testSignalChannel) Events() EventChannel {
//	return t.events
//}
//
//func (t testSignalChannel) HandleDescription(description SignalSession) error {
//	t.remoteAgent.signalHandler.Events() <- description
//
//	return nil
//}
//
//func (t testSignalChannel) HandleTrickleCandidate(candidate SignalTrickleCandidate) error {
//	t.remoteAgent.signalHandler.Events() <- candidate
//
//	return nil
//}
//
//func (t testSignalChannel) HandleEndOfCandidates(signal SignalTrickleFinished) error {
//	t.remoteAgent.signalHandler.Events() <- signal
//
//	return nil
//}
//
//func newTestSignalChannel(local *Agent, remote *Agent) *testSignalChannel {
//	return &testSignalChannel{
//		events:      make(EventChannel),
//		localAgent:  local,
//		remoteAgent: remote,
//	}
//}
