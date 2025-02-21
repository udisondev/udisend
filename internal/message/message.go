//go:generate go-enum --noprefix --marshal
package message

import (
	"bytes"
	"fmt"
	"strings"
)

/*
ENUM(

	ForYou,
	EntrypoinMemberID,
	NewConnection,
	GenerateConnectionSign,
	SendConnectionSign,
	MakeOffer,
	SendOffer,
	HandleOffer,
	SendAnswer,
	HandleAnswer,
	ConnectionEstablished,
	Ping,
	Pong,

)
*/
type MessageType string

type Income struct {
	From string
	Message
}

type Message struct {
	Type MessageType
	Text string
}

type Outcome struct {
	To string
	Message
}

type Answer struct {
	From, To, SDP string
}

type ConnectionSign struct {
	From, To, Sign, Stun string
}

type Offer struct {
	From, To, Sign, Stun, SDP string
}

func (m Message) String() string {
	return fmt.Sprintf("Type: %s, Text: %s", m.Type.String(), m.Text)
}

func ParseAnswer(text string) Answer {
	parts := strings.Split(text, "|")
	return Answer{
		From: parts[0],
		To:   parts[1],
		SDP:  parts[2],
	}
}

func ParseOffer(text string) Offer {
	parts := strings.Split(text, "|")
	return Offer{
		From: parts[0],
		To:   parts[1],
		Sign: parts[2],
		Stun: parts[3],
		SDP:  parts[4],
	}

}

func ParseConnectionSign(text string) ConnectionSign {
	parts := strings.Split(text, "|")
	return ConnectionSign{
		From: parts[0],
		To:   parts[1],
		Sign: parts[2],
		Stun: parts[3],
	}
}

func (i Income) String() string {
	return fmt.Sprintf("From: %s, Message: %s", i.From, i.Message)
}

func ParseMessage(text string) (Message, error) {
	del := strings.Index(text, "|")
	t, err := ParseMessageType(text[:del])
	if err != nil {
		return Message{}, ErrInvalidMessageType
	}

	return Message{
		Type: t,
		Text: text[del+1:],
	}, nil
}

func (m *Message) Unmarshal(b []byte) error {
	del := bytes.Index(b, []byte{'|'})
	err := m.Type.UnmarshalText(b[:del])
	if err != nil {
		return err
	}
	if del < len(b)-1 {
		m.Text = string(b[del+1:])
	}

	return nil
}

func (m Message) Marshal() ([]byte, error) {
	b, err := m.Type.MarshalText()
	if err != nil {
		return nil, err
	}
	b = append(b, '|')
	b = append(b, []byte(m.Text)...)
	return b, nil
}
