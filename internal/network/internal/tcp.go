package network

import (
	"context"
	"crypto/rsa"
	"udisend/pkg/logger"
	"udisend/pkg/span"

	"github.com/gorilla/websocket"
)

type TCP struct {
	Mesh    string
	Conn    *websocket.Conn
	PubAuth *rsa.PublicKey
}

func (t *TCP) Interact(outbox <-chan Signal) <-chan Income {
	ctx := span.Init("TCP.Interact Mesh=%s", t.Mesh)
	inbox := make(chan Income)

	readCtx, stopReading := context.WithCancel(context.Background())
	go func() {
		defer func() {
			recover()
			logger.Debugf(ctx, "Stop reading")
			stopReading()
		}()

		logger.Debugf(ctx, "Start listening outbox...")
		for s := range outbox {
			w, err := t.Conn.NextWriter(websocket.BinaryMessage)
			if err != nil {
				logger.Errorf(ctx, "t.Conn.NextWriter: %v", err)
				break
			}

			_, err = w.Write(s.Marshal())
			if err != nil {
				logger.Errorf(ctx, "w.Write: %v", err)
				break
			}

			if err := w.Close(); err != nil {
				logger.Errorf(ctx, "w.Close: %v", err)
				break
			}
		}
	}()

	go func() {
		defer func() {
			recover()
			logger.Debugf(ctx, "Closing inbox")
			close(inbox)
		}()
		logger.Debugf(ctx, "Start enriching inbox...")
	loop:
		for {
			select {
			case <-readCtx.Done():
				break loop
			default:
				_, b, err := t.Conn.ReadMessage()
				if err != nil {
					if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
						break loop
					}
					logger.Errorf(ctx, "t.Conn.ReadMessage: %v", err)
					continue
				}

				var s Signal
				err = s.Unmarshal(b)
				if err != nil {
					logger.Errorf(ctx, "s.Unmarshal: %v", err)
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
