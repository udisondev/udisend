package event

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"udisend/member"
)

type Event struct {
	Type    Type
	Payload []byte
}

type Type int8

const (
	Disconnected           Type = -1
	ConnectionSignProvided      = 0
	IceOffered                  = 1
	IceAnswered                 = 2
)

type Income struct {
	Member *member.Struct
	Event  Event
}

func (e Event) Marshal() ([]byte, error) {
	buf := new(bytes.Buffer)
	// Записываем поле Type, используя LittleEndian (или BigEndian, если нужно)
	if err := binary.Write(buf, binary.LittleEndian, e.Type); err != nil {
		return nil, fmt.Errorf("ошибка записи Type: %w", err)
	}
	// Записываем payload как есть
	if _, err := buf.Write(e.Payload); err != nil {
		return nil, fmt.Errorf("ошибка записи Payload: %w", err)
	}
	return buf.Bytes(), nil
}
