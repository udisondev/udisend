package dispatcher

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"
	"udisend/internal/member"
	"udisend/internal/message"
	"udisend/internal/schedule"
	"udisend/pkg/crypt"
	"udisend/pkg/slice"
)

type Struct struct {
	members *member.Set
	mu      sync.Mutex
	reacts  []func(message.Income) bool
}

func New(members *member.Set) *Struct {
	return &Struct{
		members: members,
		mu:      sync.Mutex{},
		reacts:  []func(message.Income) bool{},
	}
}

func (d *Struct) Dispatch(in message.Income) {
	d.React(in)

	switch in.Event.Type {
	case message.ConnectionSignRequested:
		connectionSignTo := append(in.Event.Payload, ',')
		connectionSign, _ := crypt.GenerateConnectionSign(64)
		d.members.SendTo(in.From, message.Event{
			Type:    message.ConnectionSignProvided,
			Payload: slices.Concat(connectionSignTo, connectionSign),
		})

	case message.ConnectionSignProvided:
		del := slices.Index(in.Event.Payload, ',')
		signReceiver := string(in.Event.Payload[:del])

		d.members.SendTo(signReceiver, message.Event{
			Type:    message.DoConnect,
			Payload: in.Event.Payload[del+1:],
		})

		connectionCtx, connectionDone := context.WithCancel(context.Background())
		d.mu.Lock()
		d.reacts = append(d.reacts, func(i message.Income) bool {
			if i.From != in.From {
				return false
			}

			if i.Event.Type != message.ConnectionEstablished {
				return false
			}

			bytes.Compare(i.Event.Payload, in.Event.Payload)
			connectionDone()
			return true
		})
		d.mu.Unlock()

		schedule.After(connectionCtx, time.Minute*5, func() {
			d.members.DisconnectiWithCause(signReceiver, fmt.Errorf("connection with '%s' has not established", in.From))
		})
	}
}

func (d *Struct) React(in message.Income) {
	executed := []int{}
	for i, r := range d.reacts {
		if ok := r(in); ok {
			executed = append(executed, i)
		}
	}

	d.mu.Lock()
	d.reacts = slice.RemoveIndexes(d.reacts, executed)
	d.mu.Unlock()
}
