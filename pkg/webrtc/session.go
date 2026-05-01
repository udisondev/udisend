package webrtc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	pionwebrtc "github.com/pion/webrtc/v4"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/signaling"
)

// ICEServer mirrors pion's ICEServer minus URL parsing, so callers can
// compose it from presence records.
type ICEServer struct {
	URLs       []string
	Username   string
	Credential string
}

// Config configures NewSession.
type Config struct {
	Identity   *identity.Identity
	PeerPublic identity.PublicIdentity
	Channel    *signaling.Channel
	Initiator  bool
	ICEServers []ICEServer
	Logger     *slog.Logger
}

// MessageHandler is called for every DataChannel message.
type MessageHandler func(payload []byte)

// TrackHandler is called for incoming remote media tracks.
type TrackHandler func(track *pionwebrtc.TrackRemote, receiver *pionwebrtc.RTPReceiver)

// StateHandler reports peer-connection state changes (Connected,
// Disconnected, Failed, Closed).
type StateHandler func(state pionwebrtc.PeerConnectionState)

// HandshakeTimeout caps how long Run will wait for both sides to finish
// the SDP/ICE dance.
const HandshakeTimeout = 15 * time.Second

// Session represents a WebRTC peer connection plus its bookkeeping for
// SDP signing, ICE gathering and DataChannel readiness.
type Session struct {
	cfg Config
	pc  *pionwebrtc.PeerConnection
	api *pionwebrtc.API

	dataChannel *pionwebrtc.DataChannel
	dataReady   chan struct{}

	onMessage MessageHandler
	onTrack   TrackHandler
	onState   StateHandler

	closeOnce sync.Once
	closed    chan struct{}
}

// NewSession builds the underlying PeerConnection wired with the supplied
// ICE servers. Run drives the SDP/ICE handshake.
func NewSession(cfg Config) (*Session, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	media := &pionwebrtc.MediaEngine{}
	if err := media.RegisterDefaultCodecs(); err != nil {
		return nil, fmt.Errorf("webrtc: register codecs: %w", err)
	}
	api := pionwebrtc.NewAPI(pionwebrtc.WithMediaEngine(media))

	pcConf := pionwebrtc.Configuration{
		ICEServers: convertICE(cfg.ICEServers),
	}
	pc, err := api.NewPeerConnection(pcConf)
	if err != nil {
		return nil, fmt.Errorf("webrtc: new peer connection: %w", err)
	}
	s := &Session{
		cfg:       cfg,
		pc:        pc,
		api:       api,
		dataReady: make(chan struct{}),
		closed:    make(chan struct{}),
	}

	pc.OnConnectionStateChange(func(state pionwebrtc.PeerConnectionState) {
		cfg.Logger.Debug("webrtc: state", "state", state)
		if s.onState != nil {
			s.onState(state)
		}
	})

	pc.OnTrack(func(track *pionwebrtc.TrackRemote, receiver *pionwebrtc.RTPReceiver) {
		if s.onTrack != nil {
			s.onTrack(track, receiver)
		}
	})

	if cfg.Initiator {
		dc, err := pc.CreateDataChannel("udisend", &pionwebrtc.DataChannelInit{
			Ordered: ptrBool(true),
		})
		if err != nil {
			_ = pc.Close()
			return nil, fmt.Errorf("webrtc: create datachannel: %w", err)
		}
		s.attachDataChannel(dc)
	} else {
		pc.OnDataChannel(s.attachDataChannel)
	}

	return s, nil
}

// SetMessageHandler / SetTrackHandler / SetStateHandler register callbacks.
func (s *Session) SetMessageHandler(fn MessageHandler) { s.onMessage = fn }
func (s *Session) SetTrackHandler(fn TrackHandler)     { s.onTrack = fn }
func (s *Session) SetStateHandler(fn StateHandler)     { s.onState = fn }

// PeerConnection returns the underlying pion PeerConnection so callers
// can add tracks (AddTrack) before Run.
func (s *Session) PeerConnection() *pionwebrtc.PeerConnection { return s.pc }

func (s *Session) attachDataChannel(dc *pionwebrtc.DataChannel) {
	s.dataChannel = dc
	dc.OnOpen(func() {
		select {
		case <-s.dataReady:
		default:
			close(s.dataReady)
		}
	})
	dc.OnMessage(func(msg pionwebrtc.DataChannelMessage) {
		if s.onMessage != nil {
			s.onMessage(msg.Data)
		}
	})
}

// SendData ships payload over the DataChannel. Blocks until the channel
// is open or ctx fires.
func (s *Session) SendData(ctx context.Context, payload []byte) error {
	select {
	case <-s.dataReady:
	case <-s.closed:
		return errors.New("webrtc: session closed")
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.dataChannel.Send(payload)
}

// DataReady returns a channel that is closed once the DataChannel is open.
func (s *Session) DataReady() <-chan struct{} { return s.dataReady }

// Close terminates the session.
func (s *Session) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.closed)
		if s.dataChannel != nil {
			_ = s.dataChannel.Close()
		}
		err = s.pc.Close()
	})
	return err
}

// Run drives the offer/answer exchange to completion. After Run returns
// nil, the DataChannel may still need a moment to open; wait on
// DataReady() before sending.
func (s *Session) Run(ctx context.Context) error {
	if s.cfg.Initiator {
		return s.runInitiator(ctx)
	}
	return s.runResponder(ctx)
}

func (s *Session) runInitiator(ctx context.Context) error {
	offer, err := s.pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("webrtc: create offer: %w", err)
	}
	gather := pionwebrtc.GatheringCompletePromise(s.pc)
	if err := s.pc.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("webrtc: set local: %w", err)
	}
	select {
	case <-gather:
	case <-ctx.Done():
		return ctx.Err()
	}
	full := s.pc.LocalDescription()
	if full == nil {
		return errors.New("webrtc: no local description after gathering")
	}

	signed := SignedSDP{Kind: SDPTypeOffer, SDP: full.SDP}
	signed.Sign(s.cfg.Identity)
	blob, err := signed.MarshalBinary()
	if err != nil {
		return err
	}
	if err := s.cfg.Channel.Send(ctx, blob); err != nil {
		return fmt.Errorf("webrtc: send offer: %w", err)
	}

	// Wait for answer.
	answerBlob, err := s.cfg.Channel.Recv(ctx)
	if err != nil {
		return fmt.Errorf("webrtc: receive answer: %w", err)
	}
	var answer SignedSDP
	if err := answer.UnmarshalBinary(answerBlob); err != nil {
		return err
	}
	if err := answer.Verify(s.cfg.PeerPublic); err != nil {
		return err
	}
	if answer.Kind != SDPTypeAnswer {
		return fmt.Errorf("%w: kind=%d", ErrUnknownKind, answer.Kind)
	}
	desc := pionwebrtc.SessionDescription{Type: pionwebrtc.SDPTypeAnswer, SDP: answer.SDP}
	if err := s.pc.SetRemoteDescription(desc); err != nil {
		return fmt.Errorf("webrtc: set remote (answer): %w", err)
	}
	return nil
}

func (s *Session) runResponder(ctx context.Context) error {
	offerBlob, err := s.cfg.Channel.Recv(ctx)
	if err != nil {
		return fmt.Errorf("webrtc: receive offer: %w", err)
	}
	var offer SignedSDP
	if err := offer.UnmarshalBinary(offerBlob); err != nil {
		return err
	}
	if err := offer.Verify(s.cfg.PeerPublic); err != nil {
		return err
	}
	if offer.Kind != SDPTypeOffer {
		return fmt.Errorf("%w: kind=%d", ErrUnknownKind, offer.Kind)
	}
	desc := pionwebrtc.SessionDescription{Type: pionwebrtc.SDPTypeOffer, SDP: offer.SDP}
	if err := s.pc.SetRemoteDescription(desc); err != nil {
		return fmt.Errorf("webrtc: set remote (offer): %w", err)
	}
	answer, err := s.pc.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("webrtc: create answer: %w", err)
	}
	gather := pionwebrtc.GatheringCompletePromise(s.pc)
	if err := s.pc.SetLocalDescription(answer); err != nil {
		return fmt.Errorf("webrtc: set local: %w", err)
	}
	select {
	case <-gather:
	case <-ctx.Done():
		return ctx.Err()
	}
	full := s.pc.LocalDescription()
	if full == nil {
		return errors.New("webrtc: no local description after gathering")
	}
	signed := SignedSDP{Kind: SDPTypeAnswer, SDP: full.SDP}
	signed.Sign(s.cfg.Identity)
	blob, err := signed.MarshalBinary()
	if err != nil {
		return err
	}
	if err := s.cfg.Channel.Send(ctx, blob); err != nil {
		return fmt.Errorf("webrtc: send answer: %w", err)
	}
	return nil
}

func convertICE(servers []ICEServer) []pionwebrtc.ICEServer {
	out := make([]pionwebrtc.ICEServer, 0, len(servers))
	for _, s := range servers {
		ice := pionwebrtc.ICEServer{URLs: s.URLs}
		if s.Username != "" {
			ice.Username = s.Username
			ice.Credential = s.Credential
		}
		out = append(out, ice)
	}
	return out
}

func ptrBool(v bool) *bool { return &v }
