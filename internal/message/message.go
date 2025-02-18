//go:generate go-enum --marshal --noprefix
package message

import (
	"context"
	"strings"
	"udisend/pkg/span"
)

type (
	Event struct {
		Type Type
		Text string
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
		To   string
		Text string
	}
)

func NewEvent(t Type) Event {
	return Event{Type: t}
}

func (e Event) String() string {
	return strings.Join([]string{e.Type.String(), e.Text}, "|")
}

func (e Event) Marshal() []byte {
	return []byte(e.String())
}

func (e Event) SplitText() []string {
	return strings.Split(e.Text, "|")
}

func (e Event) AddText(parts ...string) Event {
	joined := strings.Join(parts, "|")
	if e.Text != "" {
		e.Text = e.Text + "|" + joined
	} else {
		e.Text = joined
	}

	return e
}

func ParseEvent(s string) (Event, error) {
	arr := strings.Split(s, "|")
	t, err := ParseType(arr[0])
	if err != nil {
		return Event{}, err
	}
	return Event{
		Type: t,
		Text: arr[1],
	}, nil
}

// ENUM(
//
//	ProvideConnectionSign
//	ConnectionSignProvide
//	MakeOffer
//	SendOffer
//	AnswerOffer
//	SendAsnwer
//	OfferAnswered
//	ConnectionEstablished
//	ErrReadMessage
//	Disconnected
//	IamShotdown
//	HeadMemberID
//	ForYou
//	NewConnection
//	InteractionFailed
//
// )
type Type string

func Inbox(ctx context.Context, income <-chan Income, dispatcher func(ctx context.Context, in Income)) {
	ctx = span.Extend(ctx, "node.Inbox")

	for in := range income {
		dispatcher(ctx, in)
	}
}
