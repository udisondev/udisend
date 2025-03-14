package network

type (
	Signal struct {
		Type    SignalType
		Payload []byte
	}

	Income struct {
		From   string
		Signal Signal
	}
)

type SignalType uint8

const (
	SignalTypeChallenge      SignalType = 0x00
	SignalTypeSolveChallenge            = 0x01
	SignalTypeTestChallenge             = 0x02
)

func (s Signal) Marshal() []byte {
	out := make([]byte, 1+len(s.Payload))
	out[0] = byte(s.Type)
	copy(out, s.Payload)
	return out
}

func (s *Signal) Unmarshal(b []byte) error {
	s.Type = SignalType(b[0])
	s.Payload = b
	return nil
}
