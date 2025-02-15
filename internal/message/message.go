package message

import (
	"context"
	"fmt"
	"slices"
	"sync"
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
		To    string
		Event Event
	}
)

type Type uint8

const (
	ConnectionSignRequested Type = 0
	ConnectionSignProvided       = 1
	MakeOffer                    = 2
	SendOffer                    = 3
	AnswerOffer                  = 4
	SendAsnwer                   = 5
	OfferAnswered               = 6
	ConnectionEstablished        = 7
	ErrReadMessage               = 8
	Disconnected                 = 9
	IamShotdown                  = 10
	HeadMemberID                 = 11
)

func (t Type) String() string {
	switch t {
	case ConnectionSignRequested:
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
	default:
		return fmt.Sprintf("Type(%d)", t)
	}
}

func Inbox(income <-chan Income, dispatcher func(in Income)) {
	for in := range income {
		dispatcher(in)
	}
}

func (e Event) Marshal() []byte {
	return slices.Concat([]byte{byte(e.Type)}, e.Payload)
}

func Outbox(outbox <-chan Outcome) func(ctx context.Context, memberID string) <-chan Event {
	mu := sync.Mutex{}
	receivers := make(map[string]chan Event)

	go func() {
		for e := range outbox {
			if receiverOutbox, ok := receivers[e.To]; ok {
				receiverOutbox <- e.Event
			}
		}

		mu.Lock()
		for _, ch := range receivers {
			close(ch)
		}

		receivers = nil
		mu.Unlock()
	}()

	return func(ctx context.Context, memberID string) <-chan Event {
		sub := make(chan Event)
		mu.Lock()
		receivers[memberID] = sub
		mu.Unlock()

		go func() {
			<-ctx.Done()
			mu.Lock()
			if _, ok := receivers[memberID]; ok {
				delete(receivers, memberID)
			}
			mu.Unlock()
			close(sub)
		}()

		return sub

	}
}
