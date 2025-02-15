package node

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"time"
	"udisend/internal/message"
	"udisend/internal/schedule"
	"udisend/pkg/crypt"
	"udisend/pkg/slice"
)

func (n *Node) Dispatch(in message.Income) {
	log.Printf("Received message from=%s, type=%s, payload=%s", in.From, in.Event.Type.String(), string(in.Event.Payload))

	n.React(in)
	bts := slice.SplitBy(in.Event.Payload, ',')
	switch in.Event.Type {
	case message.ConnectionSignRequested:
		connectWith := string(in.Event.Payload)
		sign, _ := crypt.GenerateConnectionSign(64)
		n.signMap[connectWith] = sign

		connectionCtx, connectionDone := context.WithCancel(context.Background())
		n.mu.Lock()
		n.reacts = append(n.reacts, func(i message.Income) bool {
			bts := slice.SplitBy(i.Event.Payload, ',')
			from := string(bts[0])
			if from != connectWith {
				return false
			}

			if i.Event.Type != message.AnswerOffer {
				return false
			}

			bytes.Compare(i.Event.Payload, in.Event.Payload)
			connectionDone()
			return true
		})
		n.mu.Unlock()

		schedule.After(connectionCtx, time.Minute*5, func() {
			delete(n.signMap, connectWith)
		})

		n.members.SendTo(in.From, message.Event{
			Type:    message.ConnectionSignProvided,
			Payload: slice.ConcatWithDel(',', in.Event.Payload, sign),
		})
	case message.ConnectionSignProvided:
		n.members.SendTo(string(bts[0]), message.Event{
			Type:    message.MakeOffer,
			Payload: slice.ConcatWithDel(',', []byte(in.From), bts[1]),
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

			if bytes.Compare(i.Event.Payload, in.Event.Payload) != 0 {
				return false
			}

			connectionDone()
			return true
		})
		n.mu.Unlock()

		schedule.After(connectionCtx, time.Minute*5, func() {
			n.members.DisconnectiWithCause(string(bts[0]), fmt.Errorf("connection with '%s' has not established", in.From))
		})
	case message.MakeOffer:
		n.createOfferFor(string(bts[0]), bts[1])
	case message.SendOffer:
		n.members.SendTo(string(bts[0]), message.Event{
			Type:    message.AnswerOffer,
			Payload: slice.ConcatWithDel(',', bts[1:]...),
		})
	case message.AnswerOffer:
		n.answerSignal(in.Event)
	case message.SendAsnwer:
		n.members.SendTo(string(bts[0]), message.Event{
			Type:    message.OfferAnswered,
			Payload: slice.ConcatWithDel(',', bts[1:]...),
		})
	case message.OfferAnswered:
		n.handleAnswer(string(bts[0]), string(bts[1]))
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
