package main

func (n *Node) dispatch(message Income) {
	log.Println("New message", message.String())
	switch message.Type {
	case ForYou:
		fmt.Printf("%s: %s\n", message.From, message.Text)
	}
}

