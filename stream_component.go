package ice

import (
	"fmt"
	"github.com/pion/logging"
	"github.com/pion/stun"
	"github.com/pion/transport/packetio"
	"net"
	"strings"
	"sync"
)

type Component struct {
	ID uint16

	Stream *Stream

	state          ConnectionState
	gatheringState GatheringState

	gatherer *Gatherer

	localCandidates  []*LocalCandidate
	remoteCandidates []Candidate

	data *packetio.Buffer

	log logging.LeveledLogger
	mux sync.Mutex
}

func newGathererForComponent(component *Component) (*Gatherer, error) {
	stream := component.Stream

	gatherConfig := GathererConfig{
		VNet:           nil,
		Logger:         stream.log,
		LoggerFactory:  nil,
		Component:      component,
		STUN:           stream.Agent.stunConfig,
		TURN:           stream.Agent.turnConfig,
		NetworkTypes:   stream.Agent.networkTypes,
		GatheringTypes: stream.Agent.Config.GatheringTypes,
	}

	return NewGatherer(gatherConfig)
}

//TODO: related (RTP/RTCP) component handling
func newComponentLocal(stream *Stream, request ComponentConfiguration) (*Component, error) {
	ret := &Component{
		ID:             request.ID,
		Stream:         stream,
		state:          ConnectionStateNew,
		gatheringState: GatheringStateNew,

		localCandidates:  []*LocalCandidate{},
		remoteCandidates: []Candidate{},
		data:             packetio.NewBuffer(),
		log:              stream.log,
		mux:              sync.Mutex{},
	}

	gatherer, err := newGathererForComponent(ret)

	if err != nil {
		return nil, err
	}

	ret.gatherer = gatherer

	return ret, nil
}

func newComponentRemote(stream *Stream, request SignalComponentRequest) (*Component, error) {
	ret := &Component{
		ID:             request.ID,
		Stream:         stream,
		state:          ConnectionStateNew,
		gatheringState: GatheringStateNew,

		localCandidates:  []*LocalCandidate{},
		remoteCandidates: request.Candidates,
		data:             packetio.NewBuffer(),
		log:              stream.log,
		mux:              sync.Mutex{},
	}

	gatherer, err := newGathererForComponent(ret)

	if err != nil {
		return nil, err
	}

	ret.gatherer = gatherer

	return ret, nil
}

func (c *Component) gather() {
	c.gatherer.Start()
}

func (c *Component) acceptResponse(response SignalComponentRequest) error {
	c.mux.Lock()
	defer c.mux.Unlock()

	c.remoteCandidates = response.Candidates

	return nil
}

func (c *Component) processLocalCandidate(candidate *LocalCandidate) {
	c.mux.Lock()
	defer c.mux.Unlock()

	c.localCandidates = append(c.localCandidates, candidate)

	c.Stream.processLocalCandidate(candidate)
}

func (c *Component) processRemoteCandidate(candidate *Candidate) {
	c.mux.Lock()
	defer c.mux.Unlock()

	c.remoteCandidates = append(c.remoteCandidates, *candidate)

	c.Stream.processRemoteCandidate(candidate)
}

func (c *Component) processGatherState(state GatheringState) {
	c.mux.Lock()
	defer c.mux.Unlock()

	c.gatheringState = state
	c.Stream.processGatherState(c, state)
}

func (c *Component) setState(state ConnectionState) {
	c.mux.Lock()
	defer c.mux.Unlock()

	c.state = state
}

//func (c *Component) eventLoop() {
//	for {
//		evt, notClosed := <- c.Events
//
//		if !notClosed {
//			c.state = ComponentStateClosed
//			c.log.Debugf("interrupting event loop - component closed")
//			return
//		}
//
//		switch typedEvt := evt.(type) {
//		case gathererStateEvent:
//			c.gatheringState = typedEvt.state
//			c.dispatchEvent(componentGatheringEvent{
//				componentEvent: componentEvent{c},
//				state:          typedEvt.state,
//			})
//		case gathererCandidateEvent:
//			c.localCandidates = append(c.localCandidates, typedEvt.Candidate)
//			go c.candidateReceivedSideEffect(typedEvt.Candidate)
//
//			upstreamEvt := componentLocalCandidateEvent{
//				componentEvent: componentEvent{c},
//				Candidate:      typedEvt.Candidate,
//			}
//
//			c.dispatchUpwards(upstreamEvt)
//		case componentPeerCandidateEvent:
//			c.remoteCandidates = append(c.remoteCandidates, typedEvt.Candidate)
//			go c.candidateReceivedSideEffect(typedEvt.Candidate)
//		default:
//			c.log.Errorf("received unknown message on event bus")
//		}
//	}
//}

func resolveNetAddr(remote net.Addr) (net.IP, int, error) {
	switch addr := remote.(type) {
	case *net.UDPAddr:
		return addr.IP, addr.Port, nil
	case *net.TCPAddr:
		return addr.IP, addr.Port, nil
	default:
		return nil, 0, fmt.Errorf("unknown address type")
	}
}

func (c *Component) buildBindingResponse(remote net.Addr, message *stun.Message) (*stun.Message, error) {
	ip, port, err := resolveNetAddr(remote)

	if err != nil {
		return nil, fmt.Errorf("STUN request processing internal failure: %v", err)
	}

	return stun.Build(message, stun.BindingSuccess,
		&stun.XORMappedAddress{
			IP:   ip,
			Port: port,
		},
		stun.NewShortTermIntegrity(c.Stream.getLocalCredentials().Pwd),
		stun.Fingerprint)
}

/*
	https://tools.ietf.org/html/rfc8445#section-7.3

	It is possible (and in fact very likely) that the
	initiating agent will receive a Binding request prior to receiving
	the candidates from its peer.  If this happens, the agent MUST
	immediately generate a response (including computation of the mapped
	address as described in Section 7.3.1.2).  The agent has sufficient
	information at this point to generate the response;

*/
func (c *Component) respondBindingEarly(base base, remote net.Addr, message *stun.Message) error {
	out, err := c.buildBindingResponse(remote, message)

	if err != nil {
		c.log.Warnf("failed to handle inbound STUN from: %s to: %s error: %s", remote, base.Connection().LocalAddr(), err)
		return err
	}

	return c.sendStun(base, remote, out)
}

func (c *Component) sendStun(base base, remote net.Addr, message *stun.Message) error {
	message.Encode()
	_, err := base.Connection().WriteTo(message.Raw, remote)

	return err
}

/*
	https://tools.ietf.org/html/rfc8445#section-7.3.1.3

	follows full procedure for inbound request processing
*/
func (c *Component) respondBindingFull(base base, remote net.Addr, message *stun.Message) error {
	response, err := c.buildBindingResponse(remote, message)

	if err != nil {
		c.log.Warnf("failed to handle inbound STUN from: %s to: %s error: %s", remote, base.Connection().LocalAddr(), err)
		return err
	}

	prflxCandidate, err := c.discoverPeerReflexive(base, remote, message)

	if err != nil {
		c.log.Warnf("failed to discover peer reflexive candidate - processing will continue: %v", err)
	}

	pair, err := c.generateTriggeredCheck(base, remote, prflxCandidate)

	if err != nil {
		c.log.Errorf("failed to generate triggered check - responding with error: %v", err)
		c.sendBindingError(base, remote, message, stun.CodeServerError, "Internal error while generating triggered check")
		return err
	}

	err = c.checkNominatedFlag(message, pair)

	if err != nil {
		c.log.Errorf("nomination flag check failed - responding with error: %v", err)
		c.sendBindingError(base, remote, message, stun.CodeBadRequest, "Illegal usage of ")
		return err
	}

	return c.sendStun(base, remote, response)
}

/*
   7.3.1.3.  Learning Peer-Reflexive Candidates

   If the source transport address of the request does not match any
   existing remote candidates, it represents a new peer-reflexive remote
   candidate.  This candidate is constructed as follows:

   o  The type is peer reflexive.

   o  The priority is the value of the PRIORITY attribute in the Binding
      request.

   o  The foundation is an arbitrary value, different from the
      foundations of all other remote candidates.  If any subsequent
      candidate exchanges contain this peer-reflexive candidate, it will
      signal the actual foundation for the candidate.

   o  The component ID is the component ID of the local candidate to
      which the request was sent.

   This candidate is added to the list of remote candidates.  However,
   the ICE agent does not pair this candidate with any local candidates.
*/
func (c *Component) discoverPeerReflexive(base base, remote net.Addr, message *stun.Message) (*Candidate, error) {
	remoteIp, remotePort, err := resolveNetAddr(remote)

	if err != nil {
		return nil, err
	}

	localIp, localPort, _ := resolveNetAddr(base.Connection().LocalAddr())

	remoteCandidates := c.Stream.checklist.getRemoteCandidates()

	for _, candidate := range remoteCandidates {
		if candidate.TransportHost == remoteIp.String() && candidate.TransportPort == remotePort {
			c.log.Debugf("found matching candidate for inbound request: %s - no peer reflexive candidate will be created", candidate.String())
			return nil, nil
		}
	}

	priorityBytes, err := message.Get(stun.AttrPriority)

	if err != nil {
		c.log.Warnf("received STUN request without PRIORITY attribute. no peer-reflexive candidate can be constructed: %v", err)
		return nil, err
	}

	priority := bin.Uint32(priorityBytes)
	network := base.NetworkType().NetworkShort()

	cb := &Candidate{
		Foundation:    c.Stream.Agent.foundationGenerator.generate(network, &localIp, &remoteIp, CandidateTypePeerReflexive), //this is a bit non-conforming to standard but much easier to implement
		ComponentId:   c.ID,
		Transport:     network,
		TransportHost: remoteIp.String(),
		TransportPort: remotePort,
		Priority:      priority,
		Type:          CandidateTypePeerReflexive,
		RelatedHost:   localIp.String(),
		RelatedPort:   localPort,
	}

	c.Stream.checklist.processPeerReflexiveCandidate(cb)

	return cb, nil
}

/*
	https://tools.ietf.org/html/rfc8445#section-7.3.1.4
	7.3.1.4.  Triggered Checks
*/
func (c *Component) generateTriggeredCheck(base base, remote net.Addr, prflxCandidate *Candidate) (*CandidatePair, error) {
	existingPair := c.Stream.checklist.lookupPair(base.Connection().LocalAddr(), remote)

	if existingPair != nil {
		c.log.Debugf("found existing pair for the transport address - no prflx pair will be created: %s", existingPair.String())

		pairState := existingPair.getState()

		switch pairState {
		case CandidatePairStateSucceeded:
			c.log.Debugf("found pair is SUCCEEDED - nothing else to be done with regard to triggered check")
			break
		case CandidatePairStateInProgress:
			c.log.Debugf("found pair is IN-PROGRESS - flagging as soft-failed, setting to waiting and putting on triggered check list")
			existingPair.flagSoftFail()
			existingPair.setState(CandidatePairStateWaiting)

			c.Stream.checklist.pushTriggeredQueue(existingPair)

			break
		default:
			c.log.Debugf("found pair is '%s' - pushing to triggered checklist")

			existingPair.setState(CandidatePairStateWaiting)
			c.Stream.checklist.pushTriggeredQueue(existingPair)
		}

		return existingPair, nil
	}

	if prflxCandidate == nil {
		return nil, fmt.Errorf("have to construct peer-reflexive pair but no peer-reflexive candidate was provided")
	}

	localCandidates := c.Stream.checklist.getLocalCandidates()
	var localCandidate *LocalCandidate

	for _, cand := range localCandidates {
		if cand.Type != CandidateTypeHost && cand.Type != CandidateTypeRelay {
			continue
		}

		if cand.base.Equals(base) {
			localCandidate = cand
			break
		}
	}

	if localCandidate == nil {
		return nil, fmt.Errorf("failed to find appropriate local candidate for prflx pair")
	}

	newPair := newCandidatePair(localCandidate, prflxCandidate)
	newPair.setState(CandidatePairStateWaiting)
	c.Stream.checklist.addAndPushToTriggeredQueue(newPair)

	return newPair, nil
}

/*
	https://tools.ietf.org/html/rfc8445#section-7.3.1.5
	7.3.1.5.  Updating the Nominated Flag
*/
func (c *Component) checkNominatedFlag(message *stun.Message, pair *CandidatePair) error {

	return nil
}

func (c *Component) sendBindingError(base base, remote net.Addr, message *stun.Message, code stun.ErrorCode, reason string) error {
	errMessage, err := stun.Build(message, stun.BindingError, stun.ErrorCodeAttribute{Code: code, Reason: []byte(reason)})

	if err != nil {
		c.log.Errorf("failed to build STUN error response: %v", err)
		return err
	}

	return c.sendStun(base, remote, errMessage)
}

func (c *Component) processInboundStunRequest(base base, remote net.Addr, message *stun.Message) error {
	c.log.Debugf("STUN request recv: %s", message)

	attrControl := AttrControl{}

	err := attrControl.GetFrom(message)

	if err == nil {
		c.log.Errorf("no ICE-CONTROL attribute in stun request")
		return err
	}

	ok, controlAttribute := c.Stream.Agent.resolveRoleConflict(attrControl)

	if !ok {
		errMessage, err := stun.Build(stun.BindingError, controlAttribute)

		if err != nil {
			c.log.Errorf("failed to build STUN error response: %v", err)
			return err
		}

		err = c.sendStun(base, remote, errMessage)

		c.log.Debugf("interrupting further processing due to the role conflict detected")
		return err
	}

	response, err := stun.Build(message, controlAttribute)

	if err != nil {
		c.log.Errorf("failed to build STUN response: %v", err)
		return err
	}

	if c.Stream.getRemoteCredentials() == nil {
		return c.respondBindingEarly(base, remote, response)
	} else {
		//validate the credentials
		credentialsErr := c.validateRemoteCredentials(message)

		if credentialsErr != nil {
			c.log.Infof("invalid credentials - dropping inbound STUN request: %v", credentialsErr)

			return c.sendBindingError(base, remote, message, stun.CodeUnauthorized, "Integrity check failed")
		}

		return c.respondBindingFull(base, remote, response)
	}

}

func (c *Component) validateRemoteCredentials(message *stun.Message) error {
	attr, err := message.Get(stun.AttrUsername)
	if err != nil {
		return fmt.Errorf("failed to parse USERNAME attribute from STUN message")
	}

	creds := string(attr)

	uSplit := strings.Split(creds, ":")

	if len(uSplit) != 2 {
		return fmt.Errorf("USERNAME invalid - must contain a colon-separated value")
	}

	localUfrag := uSplit[0]
	remoteUfrag := uSplit[1]

	if localUfrag != c.Stream.getLocalCredentials().UFrag {
		return fmt.Errorf("local UFrag mismatch")
	}

	if remoteUfrag != c.Stream.getRemoteCredentials().UFrag {
		return fmt.Errorf("remote UFrag mismatch")
	}

	integrity := stun.NewShortTermIntegrity(c.Stream.getLocalCredentials().Pwd)

	return integrity.Check(message)
}

func (c *Component) receiveData(buffer []byte) {
	//if !c.validatePeerAddr(candidate, addr) {
	//	c.log.Warnf("received data failed remote addr validation for candidate %s", candidate)
	//	return
	//}
	_, err := c.data.Write(buffer)

	if err != nil {
		c.log.Debugf("failed to receive data: %v", err)
	}
}

func (c *Component) close() {
	for _, lc := range c.localCandidates {
		_ = lc.base.Connection().Close()
	}
}

//func (c *Component) findRemoteCandidate(network NetworkType, addr net.Addr) Candidate {
//	var ip net.IP
//	var port int
//
//	switch casted := addr.(type) {
//	case *net.UDPAddr:
//		ip = casted.IP
//		port = casted.Port
//	case *net.TCPAddr:
//		ip = casted.IP
//		port = casted.Port
//	default:
//		c.log.Warnf("unsupported address type %T", addr)
//		return nil
//	}
//
//	set := c.remoteCandidates
//	for _, r := range set {
//		if r.Address() == ip.String() && r.Port() == port && r.NetworkType() == network {
//			return r
//		}
//	}
//	return nil
//}
//
////Validates that peer address matches one of the remote candidates
////TODO: this will be called upon receiving every data bit so needs to be much more efficient in lookup - hashmap or sth
//func (c *Component) validatePeerAddr(local Candidate, remote net.Addr) bool {
//	remoteCandidate := c.findRemoteCandidate(local.NetworkType(), remote)
//	if remoteCandidate == nil {
//		return false
//	}
//
//	remoteCandidate.seen(false)
//	return true
//}
