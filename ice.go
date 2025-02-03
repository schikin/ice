package ice

import (
	"fmt"
)

type Option int

const (
	// OptionICE2 ICE2
	OptionICE2 = iota + 1

	// OptionRTPECN RFC6679
	OptionRTPECN
)

type SessionParameters struct {
	Pacing  *int
	Mode    *Mode
	Options []Option
}

type MulticastDNSParams struct {
	Mode MulticastDNSMode
	Name string
}

type Role int

// UnmarshalText implements TextUnmarshaler.
func (r *Role) UnmarshalText(text []byte) error {
	switch string(text) {
	case "controlling":
		*r = RoleControlling
	case "controlled":
		*r = RoleControlled
	default:
		return fmt.Errorf("unknown role %q", text)
	}
	return nil
}

// MarshalText implements TextMarshaler.
func (r *Role) MarshalText() (text []byte, err error) {
	return []byte(r.String()), nil
}

func (r *Role) String() string {
	switch *r {
	case RoleControlling:
		return "controlling"
	case RoleControlled:
		return "controlled"
	default:
		return "unknown"
	}
}

// Possible ICE agent roles.
const (
	RoleControlling Role = iota
	RoleControlled
)

type Standard int

const (
	StandardRFC8445 Standard = iota + 1
	StandardRFC5245
)

func (s Standard) String() string {
	switch s {
	case StandardRFC8445:
		return "RFC8445"
	case StandardRFC5245:
		return "RFC5245"
	default:
		return "unknown"
	}
}

type Mode int

// List of supported modes
const (
	// ModeFull ICE Full
	ModeFull = iota + 1

	// ModeLite ICE Lite
	ModeLite
)
