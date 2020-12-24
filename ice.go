package ice

import (
	"fmt"
)

type ICERole int

const (
	// StreamStateNew This agent is controlling
	ICERoleControlling = iota + 1

	// ICERoleControlled This agent is controlled
	ICERoleControlled
)

type ICEOption int

const (
	// StreamStateNew ICE2
	ICEOptionICE2 = iota + 1

	// ICEOptionRTPECN RFC6679
	ICEOptionRTPECN
)

type SessionParameters struct {
	Pacing *int
	Mode *ICEMode
	Options []ICEOption
}

type MulticastDNSParams struct {
	Mode	MulticastDNSMode
	Name	string
}

// Role represents ICE agent role, which can be controlling or controlled.
type Role byte

// UnmarshalText implements TextUnmarshaler.
func (r *Role) UnmarshalText(text []byte) error {
	switch string(text) {
	case "controlling":
		*r = Controlling
	case "controlled":
		*r = Controlled
	default:
		return fmt.Errorf("unknown role %q", text)
	}
	return nil
}

// MarshalText implements TextMarshaler.
func (r Role) MarshalText() (text []byte, err error) {
	return []byte(r.String()), nil
}

func (r Role) String() string {
	switch r {
	case Controlling:
		return "controlling"
	case Controlled:
		return "controlled"
	default:
		return "unknown"
	}
}

// Possible ICE agent roles.
const (
	Controlling Role = iota
	Controlled
)

type ICEStandard int

const (
	ICEStandardRFC8445 ICEStandard = iota + 1
	ICEStandardRFC5245
)

func (s ICEStandard) String() string {
	switch s {
	case ICEStandardRFC8445:
		return "RFC8445"
	case ICEStandardRFC5245:
		return "RFC5245"
	default:
		return "unknown"
	}
}

type ICEMode int

// List of supported modes
const (
	// ICEModeFull ICE Full
	ICEModeFull = iota + 1

	// ICEModeLite ICE Lite
	ICEModeLite
)

