package ice
//
//import (
//	"fmt"
//	"github.com/pion/stun"
//	"net"
//	"strings"
//	"time"
//)
//
//// OnConnectionStateChange sets a handler that is fired when the connection state changes
//func (a *Agent) OnConnectionStateChange(f func(ConnectionState)) error {
//	return a.run(func(agent *Agent) {
//		agent = f
//	})
//}
//
//// OnSelectedCandidatePairChange sets a handler that is fired when the final candidate
//// pair is selected
//func (a *Agent) OnSelectedCandidatePairChange(f func(Candidate, Candidate)) error {
//	return a.run(func(agent *Agent) {
//		agent.onSelectedCandidatePairChangeHdlr = f
//	})
//}
//
//// OnCandidate sets a handler that is fired when new candidates gathered. When
//// the gathering process complete the last candidate is nil.
//func (a *Agent) OnCandidate(f func(Candidate)) error {
//	return a.run(func(agent *Agent) {
//		agent.onCandidateHdlr = f
//	})
//}
//
//func (a *Agent) onSelectedCandidatePairChange(p *candidatePair) {
//	if p != nil {
//		if a.onSelectedCandidatePairChangeHdlr != nil {
//			a.onSelectedCandidatePairChangeHdlr(p.local, p.remote)
//		}
//	}
//}
//
//func (a *Agent) startConnectivityChecks(isControlling bool, remoteUfrag, remotePwd string) error {
//	switch {
//	case a.haveStarted.Load():
//		return ErrMultipleStart
//	case remoteUfrag == "":
//		return ErrRemoteUfragEmpty
//	case remotePwd == "":
//		return ErrRemotePwdEmpty
//	}
//
//	a.haveStarted.Store(true)
//	a.log.Debugf("Started agent: isControlling? %t, remoteUfrag: %q, remotePwd: %q", isControlling, remoteUfrag, remotePwd)
//
//	return a.run(func(agent *Agent) {
//		agent.isControlling = isControlling
//		agent.remoteUfrag = remoteUfrag
//		agent.remotePwd = remotePwd
//
//		if isControlling {
//			a.selector = &controllingSelector{agent: a, log: a.log}
//		} else {
//			a.selector = &controlledSelector{agent: a, log: a.log}
//		}
//
//		a.selector.Start()
//
//		agent.updateConnectionState(ConnectionStateChecking)
//
//		// TODO this should be dynamic, and grow when the connection is stable
//		a.requestConnectivityCheck()
//		agent.connectivityTicker = time.NewTicker(a.taskLoopInterval)
//	})
//}
//
//func (a *Agent) updateConnectionState(newState ConnectionState) {
//	if a.connectionState != newState {
//		a.log.Infof("Setting new connection state: %s", newState)
//		a.connectionState = newState
//		hdlr := a.onConnectionStateChangeHdlr
//		if hdlr != nil {
//			// Call handler async since we may be holding the agent lock
//			// and the handler may also require it
//			go hdlr(newState)
//		}
//	}
//}
//
//func (a *Agent) setSelectedPair(p *candidatePair) {
//	a.log.Tracef("Set selected candidate pair: %s", p)
//	// Notify when the selected pair changes
//	a.onSelectedCandidatePairChange(p)
//
//	a.selectedPairMutex.Lock()
//	a.selectedPair = p
//	a.selectedPair.nominated = true
//	a.selectedPairMutex.Unlock()
//	a.updateConnectionState(ConnectionStateConnected)
//
//	// Close mDNS Conn. We don't need to do anymore querying
//	// and no reason to respond to others traffic
//	a.closeMulticastConn()
//
//	// Signal connected
//	a.onConnectedOnce.Do(func() { close(a.onConnected) })
//}
//
//func (a *Agent) pingAllCandidates() {
//	for _, p := range a.checklist {
//
//		if p.state == CandidatePairStateWaiting {
//			p.state = CandidatePairStateInProgress
//		} else if p.state != CandidatePairStateInProgress {
//			continue
//		}
//
//		if p.bindingRequestCount > a.maxBindingRequests {
//			a.log.Tracef("max requests reached for pair %s, marking it as failed\n", p)
//			p.state = CandidatePairStateFailed
//		} else {
//			a.selector.PingCandidate(p.local, p.remote)
//			p.bindingRequestCount++
//		}
//	}
//}
//
//func (a *Agent) getBestAvailableCandidatePair() *candidatePair {
//	var best *candidatePair
//	for _, p := range a.checklist {
//		if p.state == CandidatePairStateFailed {
//			continue
//		}
//
//		if best == nil {
//			best = p
//		} else if best.Priority() < p.Priority() {
//			best = p
//		}
//	}
//	return best
//}
//
//func (a *Agent) getBestValidCandidatePair() *candidatePair {
//	var best *candidatePair
//	for _, p := range a.checklist {
//		if p.state != CandidatePairStateSucceeded {
//			continue
//		}
//
//		if best == nil {
//			best = p
//		} else if best.Priority() < p.Priority() {
//			best = p
//		}
//	}
//	return best
//}
//
//func (a *Agent) addPair(local, remote Candidate) *candidatePair {
//	p := newCandidatePair(local, remote, a.isControlling)
//	a.checklist = append(a.checklist, p)
//	return p
//}
//
//func (a *Agent) findPair(local, remote Candidate) *candidatePair {
//	for _, p := range a.checklist {
//		if p.local.Equal(local) && p.remote.Equal(remote) {
//			return p
//		}
//	}
//	return nil
//}
//
//// A task is a
//type task func(*Agent)
//
//func (a *Agent) run(t task) error {
//	err := a.ok()
//	if err != nil {
//		return err
//	}
//
//	select {
//	case <-a.done:
//		return a.getErr()
//	case a.taskChan <- t:
//	}
//	return nil
//}
//
//func (a *Agent) taskLoop() {
//	for {
//		if a.selector != nil {
//			select {
//			case <-a.forceCandidateContact:
//				a.selector.ContactCandidates()
//			case <-a.connectivityTicker.C:
//				a.selector.ContactCandidates()
//			case t := <-a.taskChan:
//				// Run the task
//				t(a)
//
//			case <-a.done:
//				return
//			}
//		} else {
//			select {
//			case t := <-a.taskChan:
//				// Run the task
//				t(a)
//
//			case <-a.done:
//				return
//			}
//		}
//	}
//}
//
//// validateSelectedPair checks if the selected pair is (still) valid
//// Note: the caller should hold the agent lock.
//func (a *Agent) validateSelectedPair() bool {
//	selectedPair, err := a.getSelectedPair()
//	if err != nil {
//		return false
//	}
//
//	if (a.connectionTimeout != 0) &&
//		(time.Since(selectedPair.remote.LastReceived()) > a.connectionTimeout) {
//		a.selectedPairMutex.Lock()
//		a.selectedPair = nil
//		a.selectedPairMutex.Unlock()
//		a.updateConnectionState(ConnectionStateDisconnected)
//		return false
//	}
//
//	return true
//}
//
//// checkKeepalive sends STUN Binding Indications to the selected pair
//// if no packet has been sent on that pair in the last keepaliveInterval
//// Note: the caller should hold the agent lock.
//func (a *Agent) checkKeepalive() {
//	selectedPair, err := a.getSelectedPair()
//	if err != nil {
//		return
//	}
//
//	if (a.keepaliveInterval != 0) &&
//		(time.Since(selectedPair.local.LastSent()) > a.keepaliveInterval) {
//		// we use binding request instead of indication to support refresh consent schemas
//		// see https://tools.ietf.org/html/rfc7675
//		a.selector.PingCandidate(selectedPair.local, selectedPair.remote)
//	}
//}
//
//// AddRemoteCandidate adds a new remote candidate
//func (a *Agent) AddRemoteCandidate(c Candidate) error {
//	// If we have a mDNS Candidate lets fully resolve it before adding it locally
//	if c.Type() == CandidateTypeHost && strings.HasSuffix(c.Address(), ".local") {
//		if a.mDNSMode == MulticastDNSModeDisabled {
//			a.log.Warnf("remote mDNS candidate added, but mDNS is disabled: (%s)", c.Address())
//			return nil
//		}
//
//		hostCandidate, ok := c.(*CandidateHost)
//		if !ok {
//			return ErrAddressParseFailed
//		}
//
//		go a.resolveAndAddMulticastCandidate(hostCandidate)
//		return nil
//	}
//
//	return a.run(func(agent *Agent) {
//		agent.addRemoteCandidate(c)
//	})
//}
//
//func (a *Agent) resolveAndAddMulticastCandidate(c *CandidateHost) {
//	_, src, err := a.mDNSConn.Query(context.TODO(), c.Address())
//	if err != nil {
//		a.log.Warnf("Failed to discover mDNS candidate %s: %v", c.Address(), err)
//		return
//	}
//
//	ip, _, _, _ := parseAddr(src)
//	if ip == nil {
//		a.log.Warnf("Failed to discover mDNS candidate %s: failed to parse IP", c.Address())
//		return
//	}
//
//	if err = c.setIP(ip); err != nil {
//		a.log.Warnf("Failed to discover mDNS candidate %s: %v", c.Address(), err)
//		return
//	}
//
//	if err = a.run(func(agent *Agent) {
//		agent.addRemoteCandidate(c)
//	}); err != nil {
//		a.log.Warnf("Failed to add mDNS candidate %s: %v", c.Address(), err)
//		return
//
//	}
//}
//
//func (a *Agent) requestConnectivityCheck() {
//	select {
//	case a.forceCandidateContact <- true:
//	default:
//	}
//}
//
//// addRemoteCandidate assumes you are holding the lock (must be execute using a.run)
//func (a *Agent) addRemoteCandidate(c Candidate) {
//	set := a.remoteCandidates[c.NetworkType()]
//
//	for _, candidate := range set {
//		if candidate.Equal(c) {
//			return
//		}
//	}
//
//	set = append(set, c)
//	a.remoteCandidates[c.NetworkType()] = set
//
//	if localCandidates, ok := a.localCandidates[c.NetworkType()]; ok {
//		for _, localCandidate := range localCandidates {
//			a.addPair(localCandidate, c)
//		}
//	}
//
//	a.requestConnectivityCheck()
//}
//
//// addCandidate assumes you are holding the lock (must be execute using a.run)
//func (a *Agent) addCandidate(c Candidate) {
//	set := a.localCandidates[c.NetworkType()]
//	for _, candidate := range set {
//		if candidate.Equal(c) {
//			return
//		}
//	}
//
//	set = append(set, c)
//	a.localCandidates[c.NetworkType()] = set
//
//	if remoteCandidates, ok := a.remoteCandidates[c.NetworkType()]; ok {
//		for _, remoteCandidate := range remoteCandidates {
//			a.addPair(c, remoteCandidate)
//		}
//	}
//}
//
//// GetLocalCandidates returns the local candidates
//func (a *Agent) GetLocalCandidates() ([]Candidate, error) {
//	res := make(chan []Candidate)
//
//	err := a.run(func(agent *Agent) {
//		var candidates []Candidate
//		for _, set := range agent.localCandidates {
//			candidates = append(candidates, set...)
//		}
//		res <- candidates
//	})
//	if err != nil {
//		return nil, err
//	}
//
//	return <-res, nil
//}
//
//// GetLocalUserCredentials returns the local user credentials
//func (a *Agent) GetLocalUserCredentials() (frag string, pwd string) {
//	return a.localUfrag, a.localPwd
//}
//
//// Close cleans up the Agent
//func (a *Agent) Close() error {
//	done := make(chan struct{})
//	err := a.run(func(agent *Agent) {
//		defer func() {
//			close(done)
//		}()
//		agent.err.Store(ErrClosed)
//		close(agent.done)
//
//		// Cleanup all candidates
//		for net, cs := range agent.localCandidates {
//			for _, c := range cs {
//				err := c.close()
//				if err != nil {
//					a.log.Warnf("Failed to close candidate %s: %v", c, err)
//				}
//			}
//			delete(agent.localCandidates, net)
//		}
//		for net, cs := range agent.remoteCandidates {
//			for _, c := range cs {
//				err := c.close()
//				if err != nil {
//					a.log.Warnf("Failed to close candidate %s: %v", c, err)
//				}
//			}
//			delete(agent.remoteCandidates, net)
//		}
//		if err := a.buffer.Close(); err != nil {
//			a.log.Warnf("failed to close buffer: %v", err)
//		}
//
//		if a.connectivityTicker != nil {
//			a.connectivityTicker.Stop()
//		}
//
//		a.closeMulticastConn()
//	})
//	if err != nil {
//		return err
//	}
//
//	<-done
//	a.updateConnectionState(ConnectionStateClosed)
//
//	return nil
//}
//
//func (a *Agent) sendBindingRequest(m *stun.Response, local, remote Candidate) {
//	a.log.Tracef("ping STUN from %s to %s\n", local.String(), remote.String())
//
//	if overflow := len(a.pendingBindingRequests) - (maxPendingBindingRequests - 1); overflow > 0 {
//		a.log.Debugf("Discarded %d pending binding requests, pendingBindingRequests is full", overflow)
//		a.pendingBindingRequests = a.pendingBindingRequests[overflow:]
//	}
//
//	useCandidate := m.Contains(stun.AttrUseCandidate)
//
//	a.pendingBindingRequests = append(a.pendingBindingRequests, bindingRequest{
//		transactionID:  m.TransactionID,
//		destination:    remote.addr(),
//		isUseCandidate: useCandidate,
//	})
//
//	a.sendSTUN(m, local, remote)
//}
//
//func (a *Agent) sendBindingSuccess(m *stun.Response, local, remote Candidate) {
//	base := remote
//	if out, err := stun.Build(m, stun.BindingSuccess,
//		&stun.XORMappedAddress{
//			IP:   base.addr().IP,
//			Port: base.addr().Port,
//		},
//		stun.NewShortTermIntegrity(a.localPwd),
//		stun.Fingerprint,
//	); err != nil {
//		a.log.Warnf("Failed to handle inbound ICE from: %s to: %s error: %s", local, remote, err)
//	} else {
//		a.sendSTUN(out, local, remote)
//	}
//}
//
//// Assert that the passed TransactionID is in our pendingBindingRequests and returns the destination
//// If the bindingRequest was valid remove it from our pending cache
//func (a *Agent) handleInboundBindingSuccess(id [stun.TransactionIDSize]byte) (bool, *bindingRequest) {
//	for i := range a.pendingBindingRequests {
//		if a.pendingBindingRequests[i].transactionID == id {
//			validBindingRequest := a.pendingBindingRequests[i]
//			a.pendingBindingRequests = append(a.pendingBindingRequests[:i], a.pendingBindingRequests[i+1:]...)
//			return true, &validBindingRequest
//		}
//	}
//	return false, nil
//}
//
//// handleInbound processes STUN traffic from a remote candidate
//func (a *Agent) handleInbound(m *stun.Response, local Candidate, remote net.Addr) {
//	var err error
//	if m == nil || local == nil {
//		return
//	}
//
//	if m.Type.Method != stun.MethodBinding ||
//		!(m.Type.Class == stun.ClassSuccessResponse ||
//			m.Type.Class == stun.ClassRequest ||
//			m.Type.Class == stun.ClassIndication) {
//		a.log.Tracef("unhandled STUN from %s to %s class(%s) method(%s)", remote, local, m.Type.Class, m.Type.Method)
//		return
//	}
//
//	if a.isControlling {
//		if m.Contains(stun.AttrICEControlling) {
//			a.log.Debug("inbound isControlling && a.isControlling == true")
//			return
//		} else if m.Contains(stun.AttrUseCandidate) {
//			a.log.Debug("useCandidate && a.isControlling == true")
//			return
//		}
//	} else {
//		if m.Contains(stun.AttrICEControlled) {
//			a.log.Debug("inbound isControlled && a.isControlling == false")
//			return
//		}
//	}
//
//	remoteCandidate := a.findRemoteCandidate(local.NetworkType(), remote)
//	if m.Type.Class == stun.ClassSuccessResponse {
//		if err = assertInboundMessageIntegrity(m, []byte(a.remotePwd)); err != nil {
//			a.log.Warnf("discard message from (%s), %v", remote, err)
//			return
//		}
//
//		if remoteCandidate == nil {
//			a.log.Warnf("discard success message from (%s), no such remote", remote)
//			return
//		}
//
//		a.selector.HandleSucessResponse(m, local, remoteCandidate, remote)
//	} else if m.Type.Class == stun.ClassRequest {
//		if err = assertInboundUsername(m, a.localUfrag+":"+a.remoteUfrag); err != nil {
//			a.log.Warnf("discard message from (%s), %v", remote, err)
//			return
//		} else if err = assertInboundMessageIntegrity(m, []byte(a.localPwd)); err != nil {
//			a.log.Warnf("discard message from (%s), %v", remote, err)
//			return
//		}
//
//		if remoteCandidate == nil {
//			ip, port, networkType, ok := parseAddr(remote)
//			if !ok {
//				a.log.Errorf("Failed to create parse remote net.Addr when creating remote prflx candidate")
//				return
//			}
//
//			prflxCandidateConfig := CandidatePeerReflexiveConfig{
//				Network:   networkType.String(),
//				Address:   ip.String(),
//				Port:      port,
//				Component: local.Component(),
//				RelAddr:   "",
//				RelPort:   0,
//			}
//
//			prflxCandidate, err := NewCandidatePeerReflexive(&prflxCandidateConfig)
//			if err != nil {
//				a.log.Errorf("Failed to create new remote prflx candidate (%s)", err)
//				return
//			}
//			remoteCandidate = prflxCandidate
//
//			a.log.Debugf("adding a new peer-reflexive candiate: %s ", remote)
//			a.addRemoteCandidate(remoteCandidate)
//		}
//
//		a.log.Tracef("inbound STUN (Request) from %s to %s", remote.String(), local.String())
//
//		a.selector.HandleBindingRequest(m, local, remoteCandidate)
//	}
//
//	if remoteCandidate != nil {
//		remoteCandidate.seen(false)
//	}
//}
//
//func (a *Agent) getSelectedPair() (*candidatePair, error) {
//	a.selectedPairMutex.RLock()
//	selectedPair := a.selectedPair
//	a.selectedPairMutex.RUnlock()
//
//	if selectedPair == nil {
//		return nil, ErrNoCandidatePairs
//	}
//
//	return selectedPair, nil
//}
//
//func (a *Agent) closeMulticastConn() {
//	if a.mDNSConn != nil {
//		if err := a.mDNSConn.Close(); err != nil {
//			a.log.Warnf("failed to close mDNS Conn: %v", err)
//		}
//	}
//}
//
//// GetCandidatePairsStats returns a list of candidate pair stats
//func (a *Agent) GetCandidatePairsStats() []CandidatePairStats {
//	resultChan := make(chan []CandidatePairStats)
//	err := a.run(func(agent *Agent) {
//		result := make([]CandidatePairStats, 0, len(agent.checklist))
//		for _, cp := range agent.checklist {
//			stat := CandidatePairStats{
//				Timestamp:         time.Now(),
//				LocalCandidateID:  cp.local.ID(),
//				RemoteCandidateID: cp.remote.ID(),
//				state:             cp.state,
//				Nominated:         cp.nominated,
//				// PacketsSent uint32
//				// PacketsReceived uint32
//				// BytesSent uint64
//				// BytesReceived uint64
//				// LastPacketSentTimestamp time.Time
//				// LastPacketReceivedTimestamp time.Time
//				// FirstRequestTimestamp time.Time
//				// LastRequestTimestamp time.Time
//				// LastResponseTimestamp time.Time
//				// TotalRoundTripTime float64
//				// CurrentRoundTripTime float64
//				// AvailableOutgoingBitrate float64
//				// AvailableIncomingBitrate float64
//				// CircuitBreakerTriggerCount uint32
//				// RequestsReceived uint64
//				// RequestsSent uint64
//				// ResponsesReceived uint64
//				// ResponsesSent uint64
//				// RetransmissionsReceived uint64
//				// RetransmissionsSent uint64
//				// ConsentRequestsSent uint64
//				// ConsentExpiredTimestamp time.Time
//			}
//			result = append(result, stat)
//		}
//		resultChan <- result
//	})
//	if err != nil {
//		a.log.Errorf("error getting candidate pairs stats %v", err)
//		return []CandidatePairStats{}
//	}
//	return <-resultChan
//}
//
//// GetLocalCandidatesStats returns a list of local candidates stats
//func (a *Agent) GetLocalCandidatesStats() []CandidateStats {
//	resultChan := make(chan []CandidateStats)
//	err := a.run(func(agent *Agent) {
//		result := make([]CandidateStats, 0, len(agent.localCandidates))
//		for networkType, localCandidates := range agent.localCandidates {
//			for _, c := range localCandidates {
//				stat := CandidateStats{
//					Timestamp:     time.Now(),
//					ID:            c.ID(),
//					NetworkType:   networkType,
//					IP:            c.Address(),
//					Port:          c.Port(),
//					CandidateType: c.Type(),
//					Priority:      c.Priority(),
//					// URL string
//					RelayProtocol: "udp",
//					// Deleted bool
//				}
//				result = append(result, stat)
//			}
//		}
//		resultChan <- result
//	})
//	if err != nil {
//		a.log.Errorf("error getting candidate pairs stats %v", err)
//		return []CandidateStats{}
//	}
//	return <-resultChan
//}
//
//// GetRemoteCandidatesStats returns a list of remote candidates stats
//func (a *Agent) GetRemoteCandidatesStats() []CandidateStats {
//	resultChan := make(chan []CandidateStats)
//	err := a.run(func(agent *Agent) {
//		result := make([]CandidateStats, 0, len(agent.remoteCandidates))
//		for networkType, localCandidates := range agent.remoteCandidates {
//			for _, c := range localCandidates {
//				stat := CandidateStats{
//					Timestamp:     time.Now(),
//					ID:            c.ID(),
//					NetworkType:   networkType,
//					IP:            c.Address(),
//					Port:          c.Port(),
//					CandidateType: c.Type(),
//					Priority:      c.Priority(),
//					// URL string
//					RelayProtocol: "udp",
//				}
//				result = append(result, stat)
//			}
//		}
//		resultChan <- result
//	})
//	if err != nil {
//		a.log.Errorf("error getting candidate pairs stats %v", err)
//		return []CandidateStats{}
//	}
//	return <-resultChan
//}
//
//type
//


