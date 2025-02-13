package message

import (
	"context"
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
	DoConnect                    = 2
	IceOffered                   = 3
	IceAnswered                  = 4
	ConnectionEstablished        = 5
	ErrReadMessage               = 6
	Disconnected                 = 7
	IamShotdown                  = 8
	HeadMemberID                     = 9
)

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
