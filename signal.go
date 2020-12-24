package ice

const (
	signalCandidatesBufferSize = 2048
	signalEocBufferSize = 2048
)

type SignalOffer struct {
	Request	RemoteSessionRequest
}

type SignalCandidate struct {
	Candidate Candidate
	StreamId  string
}

type SignalEndOfCandidates struct {
	StreamId  string
}

type SignalChannel interface {
	Events()					EventChannel
	SendDescription(description SignalOffer) error
	SendCandidate(candidate SignalCandidate) error
	SendEndOfCandidates(signal SignalEndOfCandidates) error
}

type signalQueue struct {
	offer chan *SignalOffer
	candidate chan *SignalCandidate
	eoc chan *SignalEndOfCandidates
}

func newSignalQueue() *signalQueue {
	return &signalQueue{
		offer:     make(chan *SignalOffer, 1),
		candidate: make(chan *SignalCandidate, signalCandidatesBufferSize),
		eoc:       make(chan *SignalEndOfCandidates, signalEocBufferSize),
	}
}

func (sq *signalQueue) flush() ([]*SignalOffer, []*SignalCandidate, []*SignalEndOfCandidates) {
	var candidates []*SignalCandidate
	var eocs []*SignalEndOfCandidates
	var offers []*SignalOffer

	for {
		select {
		case candidate := <- sq.candidate:
			candidates = append(candidates, candidate)
			break
		case eoc := <- sq.eoc:
			eocs = append(eocs, eoc)
			break
		case offer := <- sq.offer:
			offers = append(offers, offer)
			break
		default:

		}
	}

	return offers, candidates, eocs
}

type testSignalChannel struct {
	events		EventChannel
	localAgent	*Agent
	remoteAgent	*Agent
}

func (t testSignalChannel) Events() EventChannel {
	return t.events
}

func (t testSignalChannel) SendDescription(description SignalOffer) error {
	t.remoteAgent.signalChannel.Events() <- description

	return nil
}

func (t testSignalChannel) SendCandidate(candidate SignalCandidate) error {
	t.remoteAgent.signalChannel.Events() <- candidate

	return nil
}

func (t testSignalChannel) SendEndOfCandidates(signal SignalEndOfCandidates) error {
	t.remoteAgent.signalChannel.Events() <- signal

	return nil
}

func newTestSignalChannel(local *Agent, remote *Agent) *testSignalChannel {
	return &testSignalChannel{
		events:      make(EventChannel),
		localAgent:  local,
		remoteAgent: remote,
	};
}