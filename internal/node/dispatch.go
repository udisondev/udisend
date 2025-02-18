package node

import (
	"context"
	"fmt"
	"log"
	"time"
	"udisend/internal/message"
	"udisend/internal/schedule"
	"udisend/pkg/check/logger"
	"udisend/pkg/crypt"
	"udisend/pkg/slice"
	"udisend/pkg/span"
)

func (n *Node) Dispatch(ctx context.Context, in message.Income) {
	ctx = span.Extend(ctx, "node.Dispatch")
	logger.Debug(
		ctx,
		"Received message",
		"from",
		in.From,
		"type",
		in.Event.Type.String(),
		"payload",
		in.Event.Text,
	)

	inValues := in.Event.SplitText()
	n.React(in)
	switch in.Event.Type {
	case message.InteractionFailed:
		{
			n.members.DisconnectiWithCause(in.From, "error reading")
		}
	case message.NewConnection:
		n.members.ConnectWithOther(ctx, in.From)
	case message.ProvideConnectionSign:
		sign, _ := crypt.GenerateConnectionSign(64)
		n.signMap[inValues[0]] = sign

		connectionCtx, connectionDone := context.WithCancel(context.Background())
		n.mu.Lock()
		n.reacts = append(n.reacts, func(i message.Income) bool {
			values := i.Event.SplitText()
			if inValues[0] != values[0] {
				return false
			}

			if i.Event.Type != message.OfferAnswered {
				return false
			}

			connectionDone()

			return true
		})
		n.mu.Unlock()

		schedule.After(connectionCtx, time.Minute*5, func() {
			delete(n.signMap, inValues[0])
		})

		n.members.SendTo(
			ctx,
			in.From,
			message.NewEvent(message.ConnectionSignProvided).
				AddText(inValues[0]).
				AddText(sign),
		)

	case message.ConnectionSignProvided:
		n.members.SendTo(
			ctx,
			inValues[0],
			message.NewEvent(message.MakeOffer).
				AddText(in.From).
				AddText(inValues[1]),
		)

		connectionCtx, connectionDone := context.WithCancel(context.Background())
		n.mu.Lock()
		n.reacts = append(n.reacts, func(i message.Income) bool {
			if i.From != in.From {
				return false
			}

			if i.Event.Type != message.ConnectionEstablished {
				return false
			}

			values := i.Event.SplitText()

			if inValues[0] != values[0] {
				return false
			}

			connectionDone()
			return true
		})
		n.mu.Unlock()

		schedule.After(connectionCtx, time.Minute*5, func() {
			n.members.DisconnectiWithCause(
				inValues[0],
				fmt.Sprintf("connection with '%s' has not established", in.From),
			)
		})
	case message.MakeOffer:
		n.createOfferFor(ctx, inValues[0], inValues[1])
	case message.SendOffer:
		n.members.SendTo(
			ctx,
			inValues[0],
			message.NewEvent(message.AnswerOffer).
				AddText(inValues[1:]...),
		)
	case message.AnswerOffer:
		n.answerSignal(ctx, in.Event)
	case message.SendAsnwer:
		n.members.SendTo(
			ctx,
			inValues[0],
			message.NewEvent(message.OfferAnswered).
				AddText(inValues[1:]...),
		)
	case message.OfferAnswered:
		n.handleAnswer(ctx, inValues[0], inValues[1])
	case message.ForYou:
		log.Printf("%s: %s\n", in.From, string(in.Event.Text))
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
