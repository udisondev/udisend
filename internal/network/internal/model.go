package network

import (
	"bytes"
	"errors"
)

type (
	Signal struct {
		Type    SignalType
		Payload []byte
	}

	Income struct {
		From   string
		Signal Signal
	}

	Invite struct {
		To   string
		From string
		Sign []byte
	}

	Offer struct {
		From string
		Sign []byte
		SDP  []byte
	}

	Answer struct {
		From string
		SDP  []byte
	}
)

type SignalType uint8

const (
	SignalTypeChallenge      SignalType = 0x00
	SignalTypeSolveChallenge            = 0x01
	SignalTypeTestChallenge             = 0x02
	SignalTypeNeedInvite                = 0x03
	SignalTypeInvite                    = 0x04
	SingalTypeOffer                     = 0x05
	SignalTypeAnswer                    = 0x06
)

func (s SignalType) String() string {
	switch s {
	case SignalTypeChallenge:
		return "Challenge"
	case SignalTypeSolveChallenge:
		return "SolveChallenge"
	case SignalTypeTestChallenge:
		return "TestChallenge"
	case SignalTypeNeedInvite:
		return "NeedInvite"
	case SignalTypeInvite:
		return "Invite"
	case SingalTypeOffer:
		return "Offer"
	case SignalTypeAnswer:
		return "Answer"
	default:
		return ""
	}
}

func (s Signal) Marshal() []byte {
	out := make([]byte, 1+len(s.Payload))
	out[0] = byte(s.Type)
	copy(out[1:], s.Payload)
	return out
}

func (s *Signal) Unmarshal(b []byte) error {
	s.Type = SignalType(b[0])
	s.Payload = b[1:]
	return nil
}

func (i Invite) Marshal() []byte {
	totalLen := len(i.To) + len(i.From) + len(i.Sign) + 2
	out := make([]byte, totalLen)
	pos := 0

	pos += copy(out[pos:], i.To)
	out[pos] = '|'
	pos++
	pos += copy(out[pos:], i.From)
	out[pos] = '|'
	pos++
	copy(out[pos:], i.Sign)

	return out
}

func (i *Invite) Unmarshal(data []byte) error {
	if len(data) < 2 {
		return errors.New("invalid data: too short")
	}

	firstSep := bytes.IndexByte(data, '|')
	if firstSep == -1 {
		return errors.New("invalid data: missing first separator")
	}

	i.To = string(data[:firstSep])

	if firstSep+1 >= len(data) {
		return errors.New("invalid data: missing From and Sign")
	}

	rest := data[firstSep+1:]
	secondSep := bytes.IndexByte(rest, '|')
	if secondSep == -1 {
		return errors.New("invalid data: missing second separator")
	}

	i.From = string(rest[:secondSep])

	if secondSep+1 > len(rest) {
		i.Sign = nil
	} else {
		i.Sign = rest[secondSep+1:]
	}

	return nil
}

func (o Offer) Marshal() []byte {
	totalLen := len(o.From) + len(o.Sign) + len(o.SDP) + 2
	out := make([]byte, totalLen)
	pos := 0

	pos += copy(out[pos:], o.From)
	out[pos] = '|'
	pos++
	pos += copy(out[pos:], o.Sign)
	out[pos] = '|'
	pos++
	copy(out[pos:], o.SDP)

	return out
}

func (o *Offer) Unmarshal(data []byte) error {

	if len(data) < 2 {
		return errors.New("invalid data: too short")
	}

	firstSep := bytes.IndexByte(data, '|')
	if firstSep == -1 {
		return errors.New("invalid data: missing first separator")
	}

	o.From = string(data[:firstSep])

	if firstSep+1 >= len(data) {
		return errors.New("invalid data: missing Sign and SDP")
	}

	rest := data[firstSep+1:]
	secondSep := bytes.IndexByte(rest, '|')
	if secondSep == -1 {
		return errors.New("invalid data: missing second separator")
	}

	o.Sign = rest[:secondSep]

	if secondSep+1 > len(rest) {
		o.SDP = nil
	} else {
		o.SDP = rest[secondSep+1:]
	}

	return nil
}

func (a Answer) Marshal() []byte {
	totalLen := len(a.From) + len(a.SDP) + 1
	out := make([]byte, totalLen)
	pos := 0

	pos += copy(out[pos:], a.From)
	out[pos] = '|'
	pos++
	copy(out[pos:], a.SDP)

	return out
}

func (a *Answer) Unmarshal(data []byte) error {
	if len(data) < 1 {
		return errors.New("invalid data: too short")
	}

	sep := bytes.IndexByte(data, '|')
	if sep == -1 {
		return errors.New("invalid data: missing separator")
	}

	a.From = string(data[:sep])

	if sep+1 > len(data) {
		a.SDP = nil
	} else {
		a.SDP = data[sep+1:]
	}

	return nil
}
