package network

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"udisend/pkg/logger"
	"udisend/pkg/span"

	"github.com/gorilla/websocket"
)

type TCP struct {
	Mesh    string
	Conn    *websocket.Conn
	PubAuth *ecdsa.PublicKey
}

var (
	newline = []byte{'\n'}
	space   = []byte{' '}
)

func (t *TCP) Interact(outbox <-chan Signal) <-chan Income {
	ctx := span.Init("tcp.Interact <ID:%s>", t.Mesh)
	inbox := make(chan Income)

	readCtx, stopReading := context.WithCancel(context.Background())
	go func() {
		defer stopReading()

		for s := range outbox {
			w, err := t.Conn.NextWriter(websocket.TextMessage)
			if err != nil {
				logger.Errorf(ctx, "Error receive new writer")
				return
			}

			_, err = w.Write(s.Marshal())
			if err != nil {
				logger.Errorf(ctx, "w.Write: %v", err)
			}

			if err := w.Close(); err != nil {
				logger.Errorf(ctx, "w.Close: %v", err)
				return
			}
		}
	}()

	go func() {
		defer close(inbox)
		for {
			select {
			case <-readCtx.Done():
				return
			default:
				_, b, err := t.Conn.ReadMessage()
				if err != nil {
					if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
						logger.Errorf(ctx, "m.conn.ReadMessage: %v", err)
					}
					return
				}

				var s Signal
				b = bytes.TrimSpace(bytes.Replace(b, newline, space, -1))
				err = s.Unmarshal(b)
				if err != nil {
					logger.Errorf(ctx, "in.Unmarshal: %v", err)
					return
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
