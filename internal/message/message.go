package message

import (
	"context"
	"fmt"
	"slices"
	"udisend/pkg/span"
)

type (
	Event struct {
		Type    Type
		Payload []byte
	}

	ConnectionSign struct {
		ForConnectionWith []byte
		Sign              []byte
	}

	ClusterBroadcast struct {
	}

	Income struct {
		From  string
		Event Event
	}

	Outcome struct {
		To      string
		Content []byte
	}
)

type Type uint8

const (
	ProvideConnectionSign  Type = 0x00
	ConnectionSignProvided      = 0x01
	MakeOffer                   = 0x02
	SendOffer                   = 0x03
	AnswerOffer                 = 0x04
	SendAsnwer                  = 0x05
	OfferAnswered               = 0x06
	ConnectionEstablished       = 0x09
	ErrReadMessage              = 0x0A
	Disconnected                = 0x0B
	IamShotdown                 = 0x0C
	HeadMemberID                = 0x0D
	ForYou                      = 0x0E
	NewConnection               = 0x0F
	InteractionFailed           = 0x10
)

func (t Type) String() string {
	switch t {
	case ProvideConnectionSign:
		return "ConnectionSignRequested"
	case ConnectionSignProvided:
		return "ConnectionSignProvided"
	case MakeOffer:
		return "MakeOffer"
	case SendOffer:
		return "SendOffer"
	case AnswerOffer:
		return "AnswerOffer"
	case SendAsnwer:
		return "SendAsnwer"
	case OfferAnswered:
		return "OfferAnswered"
	case ConnectionEstablished:
		return "ConnectionEstablished"
	case ErrReadMessage:
		return "ErrReadMessage"
	case Disconnected:
		return "Disconnected"
	case IamShotdown:
		return "IamShotdown"
	case HeadMemberID:
		return "HeadMemberID"
	case ForYou:
		return "ForYou"
	case NewConnection:
		return "NewConnection"
	case InteractionFailed:
		return "InteractionFailed"
	default:
		return fmt.Sprintf("Type(%d)", t)
	}
}

func Inbox(ctx context.Context, income <-chan Income, dispatcher func(ctx context.Context, in Income)) {
	ctx = span.Extend(ctx, "node.Inbox")

	for in := range income {
		dispatcher(ctx, in)
	}
}

func (e Event) Marshal() []byte {
	return slices.Concat([]byte{byte(e.Type)}, e.Payload)
}
