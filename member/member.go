package member

import (
	"crypto/rsa"

	"github.com/gorilla/websocket"
)

type Struct struct {
	ID         string
	publicKey  rsa.PublicKey
	conn       *websocket.Conn
	disconnect func(cause error)
}

func New(ID string, conn *websocket.Conn, disconnect func(cause error)) *Struct {
	return &Struct{
		ID:         ID,
		publicKey:  rsa.PublicKey{},
		conn:       conn,
		disconnect: disconnect,
	}
}

func (s *Struct) DisconnectWithCause(cause error) {
	s.disconnect(cause)
}
