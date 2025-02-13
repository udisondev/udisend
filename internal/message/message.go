package message

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"slices"
	"sync"
	"udisend/internal/signal"
	"udisend/pkg/crypt"
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

type IncomeDispatcher struct {
	outbox  chan<- Outcome
	signals chan<- signal.Interface
}

func NewIncomeDispatcher(toOutbox func(Outcome), toSignal func(signal.Interface))

type OutboxDispatcher func(out Outcome)

type Type int8

const (
	ErrReadMessage          Type = -2
	Disconnected                 = -1
	ConnectionSignRequested      = 0
	ConnectionSignProvided       = 1
	DoConnect                    = 2
	IceOffered                   = 3
	IceAnswered                  = 4
)

func Inbox(income <-chan Income) (<-chan Outcome, <-chan signal.Interface) {
	outbox := make(chan Outcome)
	signals := make(chan signal.Interface)

	go func() {
		defer close(outbox)
		defer close(signals)

		for in := range income {
			switch in.Event.Type {
			case ConnectionSignRequested:
				connectionSignTo := append(in.Event.Payload, ',')
				connectionSign, _ := crypt.GenerateConnectionSign(64)
				outbox <- Outcome{
					To: in.From,
					Event: Event{
						Type:    ConnectionSignProvided,
						Payload: slices.Concat(connectionSignTo, connectionSign),
					},
				}
				signals <- signal.WaitOffer{
					Who:  in.From,
					With: string(connectionSignTo),
				}

			case ConnectionSignProvided:
				del := slices.Index(in.Event.Payload, ',')
				outbox <- Outcome{
					To: string(in.Event.Payload[:del]),
					Event: Event{
						Type:    DoConnect,
						Payload: in.Event.Payload[del+1:],
					},
				}
			}
		}
	}()

	return outbox, signals
}

func (i *IncomeDispatcher) Dispatch(in Income) {
}

func (e Event) Marshal() ([]byte, error) {
	buf := new(bytes.Buffer)
	// Записываем поле Type, используя LittleEndian (или BigEndian, если нужно)
	if err := binary.Write(buf, binary.LittleEndian, e.Type); err != nil {
		return nil, fmt.Errorf("ошибка записи Type: %w", err)
	}
	// Записываем payload как есть
	if _, err := buf.Write(e.Payload); err != nil {
		return nil, fmt.Errorf("ошибка записи Payload: %w", err)
	}
	return buf.Bytes(), nil
}

func Broadcast(in <-chan Event) func(ctx context.Context) <-chan Event {
	mu := sync.Mutex{}
	subs := make(map[chan Event]struct{})

	go func() {
		for e := range in {
			for box := range subs {
				box <- e
			}
		}

		mu.Lock()
		for ch := range subs {
			close(ch)
		}

		subs = nil
		mu.Unlock()
	}()

	return func(ctx context.Context) <-chan Event {
		out := make(chan Event)
		mu.Lock()
		subs[out] = struct{}{}
		mu.Unlock()

		go func() {
			<-ctx.Done()
			mu.Lock()
			if _, ok := subs[out]; ok {
				delete(subs, out)
			}
			mu.Unlock()
			close(out)
		}()

		return out
	}

}

func Outbox(outbox <-chan Outcome) func(ctx context.Context, nickname string) <-chan Event {
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

	return func(ctx context.Context, nickname string) <-chan Event {
		sub := make(chan Event)
		mu.Lock()
		receivers[nickname] = sub
		mu.Unlock()

		go func() {
			<-ctx.Done()
			mu.Lock()
			if _, ok := receivers[nickname]; ok {
				delete(receivers, nickname)
			}
			mu.Unlock()
			close(sub)
		}()

		return sub

	}
}
