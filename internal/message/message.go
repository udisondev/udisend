package message

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"sync"
)

type Event struct {
	Type    Type
	Payload []byte
}

type ConnectionSign struct {
	ForConnectionWith []byte
	Sign              []byte
}

type ClusterBroadcast struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

type Income struct {
	From  string
	Event Event
}

type Outcome struct {
	To    string
	Event Event
}

type Outbox struct {
	mu        sync.Mutex
	receivers map[string]chan Event
}

type InboxDispatcher func(in Income)

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

func NewOutbox() *Outbox {
	return &Outbox{mu: sync.Mutex{}, receivers: make(map[string]chan Event)}
}

func NewClusterBroadcast() *ClusterBroadcast {
	return &ClusterBroadcast{mu: sync.Mutex{}, subs: make(map[chan Event]struct{})}
}

func Inbox(income <-chan Income, dispatch InboxDispatcher) {
	for in := range income {
		dispatch(in)
	}
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

func (cb *ClusterBroadcast) Chan(ctx context.Context) (chan<- Event, func(ctx context.Context) <-chan Event) {
	broadcast := make(chan Event)
	go func() {
		for {
			select {
			case <-ctx.Done():
				close(broadcast)
				return
			case e := <-broadcast:
				for sub := range cb.subs {
					sub <- e
				}
			}
		}
	}()
	return broadcast, func(ctx context.Context) <-chan Event {
		sub := make(chan Event)
		cb.mu.Lock()
		cb.subs[sub] = struct{}{}
		cb.mu.Unlock()

		go func() {
			<-ctx.Done()
			cb.mu.Lock()
			if _, ok := cb.subs[sub]; ok {
				delete(cb.subs, sub)
			}
			cb.mu.Unlock()
			close(sub)
		}()

		return sub
	}

}

func (o *Outbox) Chan(ctx context.Context) (chan<- Outcome, func(ctx context.Context, nickname string) <-chan Event) {
	outbox := make(chan Outcome)
	go func() {
		for {
			select {
			case <-ctx.Done():
				close(outbox)
				return
			case e := <-outbox:
				if receiverOutbox, ok := o.receivers[e.To]; ok {
					receiverOutbox <- e.Event
				}
			}
		}
	}()
	return outbox, func(ctx context.Context, nickname string) <-chan Event {
		sub := make(chan Event)
		o.mu.Lock()
		o.receivers[nickname] = sub
		o.mu.Unlock()

		go func() {
			<-ctx.Done()
			o.mu.Lock()
			if _, ok := o.receivers[nickname]; ok {
				delete(o.receivers, nickname)
			}
			o.mu.Unlock()
			close(sub)
		}()

		return sub
	}
}
