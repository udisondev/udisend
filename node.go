// Copyright 2013 The Gorilla WebSocket Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"errors"
	"fmt"
	"sync"
)

var (
	ErrMemberNotFound = errors.New("member not found")
)

// Node maintains the set of active clients and broadcasts messages to the
// clients.
type Node struct {
	// Registered members.
	members sync.Map

	// Inbound messages from the clients.
	inbox chan Income

	// Register requests from the clients.
	register chan *Member

	// Unregister requests from clients.
	unregister chan string

}

func newNode() *Node {
	return &Node{
		inbox:      make(chan Income),
		register:   make(chan *Member),
		unregister: make(chan string),
	}
}

func (n *Node) run() {
	for {
		select {
		case member := <-n.register:
			n.members.Store(member.id, member)
		case memberID := <-n.unregister:
			if v, ok := n.members.Load(memberID); ok {
				m := v.(*Member)
				n.members.Delete(memberID)
				close(m.send)
			}
		case message := <-n.inbox:
			n.dispatch(message)
		}
	}
}

func (n *Node) dispatch(message Income) {
	switch message.Type {
	case ForYou:
		fmt.Printf("%s: %s", message.From, message.Text)
	}
}

func (n *Node) send(out Outcome) error {
	v, ok := n.members.Load(out.To)
	if !ok {
		return ErrMemberNotFound
	}
	m := v.(*Member)
	m.send <- out.Message 
	return nil
}
