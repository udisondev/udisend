package node

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"time"
	"udisend/internal/message"
	"udisend/internal/schedule"
	"udisend/pkg/crypt"
	"udisend/pkg/slice"
)

func (n *Node) Dispatch(in message.Income) {
	n.React(in)

	switch in.Event.Type {
	case message.ConnectionSignRequested:
		connectionSignTo := append(in.Event.Payload, ',')
		connectionSign, _ := crypt.GenerateConnectionSign(64)
		n.members.SendTo(in.From, message.Event{
			Type:    message.ConnectionSignProvided,
			Payload: slices.Concat(connectionSignTo, connectionSign),
		})

	case message.ConnectionSignProvided:
		del := slices.Index(in.Event.Payload, ',')
		signReceiver := string(in.Event.Payload[:del])

		n.members.SendTo(signReceiver, message.Event{
			Type:    message.DoConnect,
			Payload: in.Event.Payload[del+1:],
		})

		connectionCtx, connectionDone := context.WithCancel(context.Background())
		n.mu.Lock()
		n.reacts = append(n.reacts, func(i message.Income) bool {
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
		n.mu.Unlock()

		schedule.After(connectionCtx, time.Minute*5, func() {
			n.members.DisconnectiWithCause(signReceiver, fmt.Errorf("connection with '%s' has not established", in.From))
		})
	case message.DoConnect:

		
	}
}

func (n *Node) React(in message.Income) {
	executed := []int{}
	for i, r := range n.reacts {
		if ok := r(in); ok {
			executed = append(executed, i)
		}
	}

	n.mu.Lock()
	n.reacts = slice.RemoveIndexes(n.reacts, executed)
	n.mu.Unlock()
}
