package node

import (
	"context"
	"fmt"
	"log"
	"time"
	"udisend/internal/message"
)

func (n *Node) dispatch(in message.Income) {
	log.Println("New message", in.String())
	for _, r := range n.reacts {
		if r(in) {

		}
	}
	switch in.Type {
	case message.ForYou:
		fmt.Printf("%s: %s\n", in.From, in.Text)
	case message.NewConnection:
		n.requestSignsFor(in.From)
	case message.SendConnectionSign:
		n.handleSendConnectionSign(in)
	case message.MakeOffer:
		n.handleMakeOffer(in)
	case message.SendOffer:
		n.handleSendOffer(in)
	case message.HandleOffer:
		n.handleHandleOffer(in)
	case message.SendAnswer:
		n.handleSendAswer(in)
	case message.HandleAnswer:
		n.handleHandleAsnwer(in)
	case message.ConnectionEstablished:
		n.handleConnectionEstableshed(in)
	}
}

func (n *Node) handleConnectionEstableshed(in message.Income) {
	panic("unimplemented")
}

func (n *Node) handleHandleAsnwer(in message.Income) {
	panic("unimplemented")
}

func (n *Node) handleSendAswer(in message.Income) {
	panic("unimplemented")
}

func (n *Node) handleHandleOffer(in message.Income) {
	panic("unimplemented")
}

func (n *Node) handleSendOffer(in message.Income) {
	panic("unimplemented")
}

func (n *Node) handleMakeOffer(in message.Income) {
	panic("unimplemented")
}

func (n *Node) handleSendConnectionSign(in message.Income) {
	connSign := message.ParseConnectionSign(in.Text)

	n.Send(message.Outcome{
		To: connSign.To,
		Message: message.Message{
			Type: message.MakeOffer,
			Text: in.Text,
		},
	})

	signCtx, cancelDisconnect := context.WithCancel(context.Background())

	go func() {
		select {
		case <-signCtx.Done():
		case <-time.After(5 * time.Minute):
			n.disconnectMember(connSign.To)
		}
	}()

	n.reacts = append(n.reacts, func(checked message.Income) bool {
		if checked.Message.Type != message.ConnectionEstablished {
			return false
		}
		if checked.From != connSign.From {
			return false
		}
		if checked.Text != connSign.To {
			return false
		}

		cancelDisconnect()
		return true
	})
}

func (n *Node) requestSignsFor(ID string) {
	n.members.Range(func(key, value any) bool {
		membID := key.(string)
		if membID == ID {
			return true
		}

		m := value.(*Member)
		m.send <- message.Message{
			Type: message.ProvideConnectionSign,
			Text: ID,
		}
		return true
	})

}

func (n *Node) disconnectMember(ID string) {
	v, ok := n.members.Load(ID)
	if !ok {
		return
	}
	n.members.Delete(ID)
	m := v.(*Member)
	close(m.send)
}
