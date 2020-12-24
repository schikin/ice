package ice

import (
	"fmt"
	"strings"
)

const (
	receiveMTU             = 8192
	defaultLocalPreference = 65535

	// ComponentRTP indicates that the candidate is used for RTP
	ComponentRTP uint16 = 1
	// ComponentRTCP indicates that the candidate is used for RTCP
	ComponentRTCP
)

type ResolvableAddress struct {
	Host string
	Port uint32
	Network string
}

type Candidate struct {
	Foundation string
	ComponentId uint16
	Transport string
	TransportHost string
	TransportPort int
	Priority uint32
	Type CandidateType
	RelatedHost string
	RelatedPort int
}

func (c *Candidate) String() string {
	if len(c.RelatedHost) != 0 {
		return fmt.Sprintf("%s %d %s %d %s %d %s %s %d", c.Foundation, c.ComponentId, c.Transport, c.Priority, c.TransportHost, c.TransportPort, c.Type, c.RelatedHost, c.RelatedPort)
	} else {
		return fmt.Sprintf("%s %d %s %d %s %d %s", c.Foundation, c.ComponentId, c.Transport, c.Priority, c.TransportHost, c.TransportPort, c.Type)
	}
}

func (c *Candidate) isMDNS() bool {
	return strings.HasSuffix(c.TransportHost, ".local");
}

type LocalCandidate struct {
	Candidate

	base Base
}

// Candidate represents an ICE candidate
//type Candidate interface {
//	ID() string
//	Component() *Component
//	Address() string
//	LastReceived() time.Time
//	LastSent() time.Time
//	NetworkType() NetworkType
//	Port() int
//	Priority() uint32
//	RelatedAddress() *CandidateRelatedAddress
//	String() string
//	Type() CandidateType
//
//	Equal(other Candidate) bool
//
//	addr() *net.UDPAddr
//
//	close() error
//	seen(outbound bool)
//	start(conn net.PacketConn)
//	writeTo(raw []byte, dst Candidate) (int, error)
//}
