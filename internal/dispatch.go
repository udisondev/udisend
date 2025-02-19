package node

import (
	"fmt"
	"log"
	"udisend/internal/message"
)

func (n *Node) dispatch(in message.Income) {
	log.Println("New message", in.String())
	switch in.Type {
	case message.ForYou:
		fmt.Printf("%s: %s\n", in.From, in.Text)
	case message.NewConnection:
		n.requestSignsFor(in.From)
	}
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

