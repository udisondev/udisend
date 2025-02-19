// Copyright 2013 The Gorilla WebSocket Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	ErrMemberNotFound = errors.New("member not found")
)

// Node maintains the set of active clients and broadcasts messages to the
// clients.
type Node struct {
	memberID string

	entryPoint string
	// Registered members.
	members sync.Map

	// Inbound messages from the clients.
	inbox chan Income

	// Register requests from the clients.
	register chan *Member

	// Unregister requests from clients.
	unregister chan string
}

func newNode(memberID string) *Node {
	return &Node{
		memberID:   memberID,
		inbox:      make(chan Income),
		register:   make(chan *Member),
		unregister: make(chan string),
	}
}

func (n *Node) run() {
	for {
		select {
		case member := <-n.register:
			log.Println("New member", "ID", member.id)
			n.members.Store(member.id, member)
		case memberID := <-n.unregister:
			log.Println("Member disconnected", "ID", memberID)
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
	log.Println("New message", message.String())
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

func (n *Node) attachHead() {
	h := http.Header{}
	h.Add("memberID", *memberID)
	u := url.URL{Scheme: "ws", Host: *entryPoint, Path: "/ws"}

	log.Printf("connecting to %s", u.String())
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), h)
	if err != nil {
		panic(err)
	}

	defer conn.Close()

	var memberID string
	conn.SetReadLimit(maxMessageSize)
	conn.SetReadDeadline(time.Now().Add(pongWait))

	for {
		_, b, err := conn.ReadMessage()
		log.Printf("received raw=%s\n", string(b))

		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			return
		}
		var message Message
		b = bytes.TrimSpace(bytes.Replace(b, newline, space, -1))
		err = message.Unmarshal(b)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			return
		}

		if message.Type != EntrypoinMemberID {
			continue
		}

		memberID = message.Text
		break
	}

	client := &Member{id: memberID, node: n, conn: conn, send: make(chan Message, 256)}
	client.node.register <- client

	go client.writePump()
	go client.readPump()
}
