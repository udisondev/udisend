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
	ProvideConnectionSign,
	SendConnectionSign,
	MakeOffer,
	SendOffer,
	HandleOffer,
	SendAnswer,
	HandleAnswer,
	ConnectionEstablished,

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

type ConnectionSign struct {
	From, To, Sign string
}

func ParseConnectionSign(text string) ConnectionSign {
	parts := strings.Split(text, "|")
	return ConnectionSign{
		From: parts[0],
		To:  parts[1],
		Sign: parts[2],
	}
}

func (i Income) String() string {
	return fmt.Sprintf("From: %s, Type: %s, Text: %s", i.From, i.Type.String(), i.Text)
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
	m.Text = string(b[del+1:])
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
