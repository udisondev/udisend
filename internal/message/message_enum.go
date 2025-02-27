// Code generated by go-enum DO NOT EDIT.
// Version:
// Revision:
// Build Date:
// Built By:

package message

import (
	"errors"
	"fmt"
)

const (
	// Private is a MessageType of type Private.
	Private MessageType = "Private"
	// DoVerify is a MessageType of type DoVerify.
	DoVerify MessageType = "DoVerify"
	// ProvidePubKey is a MessageType of type ProvidePubKey.
	ProvidePubKey MessageType = "ProvidePubKey"
	// PubKeyProvided is a MessageType of type PubKeyProvided.
	PubKeyProvided MessageType = "PubKeyProvided"
	// SolveChallenge is a MessageType of type SolveChallenge.
	SolveChallenge MessageType = "SolveChallenge"
	// TestChallenge is a MessageType of type TestChallenge.
	TestChallenge MessageType = "TestChallenge"
	// NewConnection is a MessageType of type NewConnection.
	NewConnection MessageType = "NewConnection"
	// GenerateConnectionSign is a MessageType of type GenerateConnectionSign.
	GenerateConnectionSign MessageType = "GenerateConnectionSign"
	// SendConnectionSign is a MessageType of type SendConnectionSign.
	SendConnectionSign MessageType = "SendConnectionSign"
	// MakeOffer is a MessageType of type MakeOffer.
	MakeOffer MessageType = "MakeOffer"
	// SendOffer is a MessageType of type SendOffer.
	SendOffer MessageType = "SendOffer"
	// HandleOffer is a MessageType of type HandleOffer.
	HandleOffer MessageType = "HandleOffer"
	// SendAnswer is a MessageType of type SendAnswer.
	SendAnswer MessageType = "SendAnswer"
	// HandleAnswer is a MessageType of type HandleAnswer.
	HandleAnswer MessageType = "HandleAnswer"
	// ConnectionEstablished is a MessageType of type ConnectionEstablished.
	ConnectionEstablished MessageType = "ConnectionEstablished"
	// Ping is a MessageType of type Ping.
	Ping MessageType = "Ping"
	// Pong is a MessageType of type Pong.
	Pong MessageType = "Pong"
)

var ErrInvalidMessageType = errors.New("not a valid MessageType")

// String implements the Stringer interface.
func (x MessageType) String() string {
	return string(x)
}

// IsValid provides a quick way to determine if the typed value is
// part of the allowed enumerated values
func (x MessageType) IsValid() bool {
	_, err := ParseMessageType(string(x))
	return err == nil
}

var _MessageTypeValue = map[string]MessageType{
	"Private":                Private,
	"DoVerify":               DoVerify,
	"ProvidePubKey":          ProvidePubKey,
	"PubKeyProvided":         PubKeyProvided,
	"SolveChallenge":         SolveChallenge,
	"TestChallenge":          TestChallenge,
	"NewConnection":          NewConnection,
	"GenerateConnectionSign": GenerateConnectionSign,
	"SendConnectionSign":     SendConnectionSign,
	"MakeOffer":              MakeOffer,
	"SendOffer":              SendOffer,
	"HandleOffer":            HandleOffer,
	"SendAnswer":             SendAnswer,
	"HandleAnswer":           HandleAnswer,
	"ConnectionEstablished":  ConnectionEstablished,
	"Ping":                   Ping,
	"Pong":                   Pong,
}

// ParseMessageType attempts to convert a string to a MessageType.
func ParseMessageType(name string) (MessageType, error) {
	if x, ok := _MessageTypeValue[name]; ok {
		return x, nil
	}
	return MessageType(""), fmt.Errorf("%s is %w", name, ErrInvalidMessageType)
}

// MarshalText implements the text marshaller method.
func (x MessageType) MarshalText() ([]byte, error) {
	return []byte(string(x)), nil
}

// UnmarshalText implements the text unmarshaller method.
func (x *MessageType) UnmarshalText(text []byte) error {
	tmp, err := ParseMessageType(string(text))
	if err != nil {
		return err
	}
	*x = tmp
	return nil
}
