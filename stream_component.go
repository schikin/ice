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

//TODO: side effects serialization/controlled execution

type componentEvent struct {
	Component *Component
}

type componentStateEvent struct {
	componentEvent
	State ConnectionState
}

type componentGatheringEvent struct {
	componentEvent
	State GatheringState
}

type componentLocalCandidateEvent struct {
	componentEvent
	Candidate Candidate
}

type componentPeerCandidateEvent struct {
	componentEvent
	Candidate Candidate
}

type Component struct {
	ID			   uint16

	Stream         *Stream

	state          ConnectionState
	gatheringState GatheringState

	gatherer		*Gatherer

	localCandidates []*LocalCandidate
	remoteCandidates []Candidate

	data *packetio.Buffer

	log 	logging.LeveledLogger
	mux		sync.Mutex
}

func newGathererForComponent(component *Component) (*Gatherer, error) {
	stream := component.Stream

	gatherConfig := GathererConfig{
		VNet:           nil,
		Logger:         stream.log,
		LoggerFactory:  nil,
		Component: 		component,
		STUN:           stream.Agent.stunConfig,
		TURN:           stream.Agent.turnConfig,
		NetworkTypes:   stream.Agent.networkTypes,
		GatheringTypes: stream.Agent.Config.GatheringTypes,
	}

	return NewGatherer(gatherConfig)
}

//TODO: related (RTP/RTCP) component handling
func newComponentLocal(stream *Stream, request LocalComponentRequest) (*Component, error){
	ret := &Component{
		ID:             request.ID,
		Stream:         stream,
		state:          ConnectionStateNew,
		gatheringState: GatheringStateNew,

		localCandidates:  []*LocalCandidate{},
		remoteCandidates: []Candidate{},
		data:             packetio.NewBuffer(),
		log:              stream.log,
		mux:			  sync.Mutex{},
	}

	gatherer, err := newGathererForComponent(ret)

	if err != nil {
		return nil, err
	}

	ret.gatherer = gatherer

	return ret, nil
}

func newComponentRemote(stream *Stream, request RemoteComponentRequest) (*Component, error){
	ret := &Component{
		ID:             request.ID,
		Stream:         stream,
		state:          ConnectionStateNew,
		gatheringState: GatheringStateNew,

		localCandidates:  []*LocalCandidate{},
		remoteCandidates: request.Candidates,
		data:             packetio.NewBuffer(),
		log:              stream.log,
		mux:			  sync.Mutex{},
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

func (c *Component) acceptResponse(response RemoteComponentRequest) error {
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

	c.log.Debugf("component state: %s -> %s", c.state, state);

	c.state = state
	c.dispatchEvent(componentStateEvent{
		componentEvent: componentEvent{c},
		State:         state,
	})
}

func (c *Component) dispatchEvent(evt Event) {
	c.Stream.dispatchEvent(evt)
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

func resolveRemoteAddr(remote net.Addr) (net.IP, int, error) {
	switch addr := remote.(type) {
	case *net.UDPAddr:
		return addr.IP, addr.Port, nil
	case *net.TCPAddr:
		return addr.IP, addr.Port, nil
	default:
		return nil, 0, fmt.Errorf("unknown address type")
	}
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
func (c *Component) respondToEarly(base Base, remote net.Addr, message *stun.Message) {
	ip, port, err := resolveRemoteAddr(remote)

	if err != nil {
		c.log.Errorf("STUN request processing internal failure: ", err)
		return
	}

	if out, err := stun.Build(message, stun.BindingSuccess,
		&stun.XORMappedAddress{
			IP:   ip,
			Port: port,
		},
		stun.NewShortTermIntegrity(c.Stream.getLocalCredentials().Pwd),
		stun.Fingerprint,
	); err != nil {
		c.log.Warnf("failed to handle inbound ICE from: %s to: %s error: %s", remote, base.Address(), err)
	} else {
		c.sendStun(base, remote, out)
	}
}

func (c *Component) sendStun(base Base, remote net.Addr, message *stun.Message) {
	message.Encode()
	go base.write(message.Raw, remote)
}

/*
	https://tools.ietf.org/html/rfc8445#section-7.3.1.3

	follows the rest of procedures for
 */
func (c *Component) respondBindingFull(base Base, remote net.Addr, message *stun.Message) {
	ip, port, err := resolveRemoteAddr(remote)

	if err != nil {
		c.log.Errorf("STUN request processing internal failure: ", err)
		return
	}

	if out, err := stun.Build(message, stun.BindingSuccess,
		&stun.XORMappedAddress{
			IP:   ip,
			Port: port,
		},
		stun.NewShortTermIntegrity(c.Stream.getLocalCredentials().Pwd),
		stun.Fingerprint,
	); err != nil {
		c.log.Warnf("failed to handle inbound ICE from: %s to: %s error: %s", remote, base.Address(), err)
	} else {
		out.Encode()
		go base.write(out.Raw, remote)
	}
}

func (c *Component) recvStunRequest(base Base, remote net.Addr, message *stun.Message) {
	c.log.Debugf("STUN request recv: %s", message)

	attrControl := AttrControl{}

	err := attrControl.GetFrom(message)

	if err == nil {
		c.log.Errorf("no ICE-CONTROL attribute in stun request")
		return
	}

	ok, controlAttribute := c.Stream.Agent.resolveRoleConflict(attrControl)

	if !ok {
		errMessage, err := stun.Build(stun.BindingError,controlAttribute)

		if err != nil {
			c.log.Errorf("failed to build STUN error response: %v", err)
			return
		}

		c.sendStun(base, remote, errMessage)

		c.log.Debugf("interrupting further processing due to the role conflict detected")
		return
	}

	response, err := stun.Build(message, controlAttribute)

	if err != nil {
		c.log.Errorf("failed to build STUN response: %v", err)
		return
	}

	if c.Stream.getRemoteCredentials() == nil {
		c.respondToEarly(base, remote, response)
	} else {
		//validate the credentials
		credsErr := c.validateRemoteCredentials(message)

		if credsErr != nil {
			c.log.Infof("invalid credentials - dropping inbound STUN request: %v", credsErr)

			errMessage, err := stun.Build(message, stun.BindingError, stun.ErrorCodeAttribute{Code: stun.CodeUnauthorized, Reason: []byte("Integrity check failed")})

			if err != nil {
				c.log.Errorf("failed to build STUN error response: %v", err)
				return
			}

			c.sendStun(base, remote, errMessage)

			return
		}

		c.respondBindingFull()
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

func (c *Component) recvData(base Base, remote net.Addr, buffer []byte) {
	//if !c.validatePeerAddr(candidate, addr) {
	//	c.log.Warnf("received data failed remote addr validation for candidate %s", candidate)
	//	return
	//}
	c.log.Debugf("data package recv, size=%d", len(buffer))
}

func (c *Component) close() {
	for _, lc := range c.localCandidates {
		lc.base.close()
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
