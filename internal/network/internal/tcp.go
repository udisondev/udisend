package network

import (
	"context"
	"crypto/rsa"

	"github.com/gorilla/websocket"
)

type TCP struct {
	Mesh    string
	Conn    *websocket.Conn
	PubAuth *rsa.PublicKey
}

func (t *TCP) Interact(outbox <-chan Signal) <-chan Income {
	inbox := make(chan Income)

	readCtx, stopReading := context.WithCancel(context.Background())
	go func() {
		defer stopReading()

		for s := range outbox {
			w, err := t.Conn.NextWriter(websocket.BinaryMessage)
			if err != nil {
				break
			}

			_, err = w.Write(s.Marshal())
			if err != nil {
			}

			if err := w.Close(); err != nil {
				break
			}
		}
	}()

	go func() {
		defer close(inbox)
	loop:
		for {
			select {
			case <-readCtx.Done():
				break loop
			default:
				_, b, err := t.Conn.ReadMessage()
				if err != nil {
					if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					}
					break loop
				}

				var s Signal
				err = s.Unmarshal(b)
				if err != nil {
					break loop
				}

				inbox <- Income{From: t.Mesh, Signal: s}
			}
		}
	}()

	return inbox
}

func (t *TCP) ID() string {
	return t.Mesh
}
