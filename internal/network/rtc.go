package network

import (
	"bytes"
	"crypto/rand"
	"time"
	. "udisend/internal/network/internal"
	"udisend/pkg/closer"
	"udisend/pkg/crypt"
	"udisend/pkg/logger"
	"udisend/pkg/span"

	"github.com/pion/webrtc/v4"
)

func generateInvite(n *Network, in Income) {
	ctx := span.Init("generateInvite")
	slot := n.bookSlot()
	if slot == nil {
		logger.Warnf(ctx, "Has no free slot")
		n.broadcastWithExclude(in.Signal, in.From)
		return
	}

	var err error
	defer func() {
		if err != nil {
			slot.Free()
		} else {
			closer.Add(func() error {
				slot.Free()
				return nil
			})
		}
	}()

	sign := make([]byte, 32)
	rand.Read(sign)
	logger.Debugf(ctx, "Sign generated")
	secret := make([]byte, 32)
	rand.Read(secret)
	pubKey, err := crypt.ExtractPublicKey(string(in.Signal.Payload))
	if err != nil {
		logger.Errorf(ctx, "crypt.ExtractPublicKey: %v", err)
		return
	}
	payload, err := crypt.EncryptMessage(
		Invite{
			To:     in.From,
			From:   n.mesh,
			Sign:   sign,
			Secret: secret,
		}.Marshal(),
		pubKey,
		n.privateKey)
	if err != nil {
		logger.Errorf(ctx, "crypt.EncryptMessage: %v", err)
		return
	}
	logger.Debugf(ctx, "Payload encrypted")

	n.addReaction(
		time.Second*20,
		func(offerMsg Income) bool {
			if offerMsg.Signal.Type != SingalTypeOffer {
				return false
			}
			logger.Debugf(nil, "Received offer")
			decryptedPayload, err := crypt.DecryptMessage(offerMsg.Signal.Payload, n.privateKey)
			if err != nil {
				logger.Infof(ctx, "Supose the offer is not mine")
				return false
			}
			var offer Offer
			offer.Unmarshal(decryptedPayload)
			if offer.From != string(in.Signal.Payload) {
				logger.Warnf(ctx, "I'am not waiting an offer from %s", offer.From)
				return false
			}
			if !bytes.Equal(sign, offer.Sign) {
				logger.Warnf(ctx, "Invalid sign!")
				return true
			}

			go func() {
				ctx := span.Init("creating answer")
				config := webrtc.Configuration{
					ICEServers: []webrtc.ICEServer{
						{
							URLs: []string{
								"stun:stun.l.google.com:19302",
							},
						},
					},
				}

				pc, err := webrtc.NewPeerConnection(config)
				defer func() {
					if err == nil {
						return
					}
					slot.Free()
					pc.Close()
				}()
				if err != nil {
					logger.Errorf(ctx, "webrtc.NewPeerConnection: %v", err)
					return
				}

				logger.Debugf(ctx, "Configuring pc...")
				pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
					ctx := span.Extend(ctx, "pc.OnConnectionStateChange")
					if state == webrtc.PeerConnectionStateClosed {
						logger.Debugf(ctx, "closed")
					}
					logger.Debugf(nil, "changed to=%s", state)
				})
				pc.OnDataChannel(func(dataChannel *webrtc.DataChannel) {
					ctx := span.Extend(ctx, "pc.OnDataChannel")
					dataChannel.OnOpen(func() {
						ctx := span.Extend(ctx, "dataChannel.OnOpen")
						logger.Debugf(ctx, "Opened!")
						slot.AddConn(&OfferICE{
							PC:   pc,
							DC:   dataChannel,
							Mesh: offer.From,
						}, true)
						dataChannel.Send(Signal{
							Type:    SignalTypeConnectionSecret,
							Payload: secret,
						}.Marshal())
						logger.Debugf(ctx, "Secret was sent!")
					})
				})

				pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
					ctx := span.Extend(ctx, "pc.OnICEConnectionStateChange")

					if state == webrtc.ICEConnectionStateDisconnected {
						logger.Warnf(ctx, "disconnected")
						return
					}

					logger.Debugf(ctx, "changed to=%s", state)
				})

				sd := webrtc.SessionDescription{}
				encryptedSDP, err := crypt.EncryptRSA([]byte(sd.SDP), pubKey)
				if err != nil {
					logger.Errorf(ctx, "crypt.EncryptMessage: %v", err)
					return
				}

				err = pc.SetRemoteDescription(sd)
				if err != nil {
					logger.Errorf(ctx, "pc.SetRemoteDesctiption: %v", err)
					return
				}

				answer, err := pc.CreateAnswer(nil)
				if err != nil {
					logger.Errorf(ctx, "pc.CreateAnswer: %v", err)
					return
				}

				gatherComplete := webrtc.GatheringCompletePromise(pc)

				err = pc.SetLocalDescription(answer)
				if err != nil {
					logger.Errorf(ctx, "pc.SetLocalDescription: %v", err)
					return
				}

				<-gatherComplete
				n.broadcastWithExclude(Signal{
					Type: SignalTypeAnswer,
					Payload: Answer{
						From: n.mesh,
						To:   string(in.Signal.Payload),
						SDP:  encryptedSDP,
					}.Marshal()})
				logger.Debugf(ctx, "Answer was sent!")
			}()

			return true
		})

	n.broadcastWithExclude(Signal{
		Type:    SignalTypeInvite,
		Payload: payload,
	})

	logger.Debugf(ctx, "Invite was sent!")
}

func makeOffer(n *Network, in Income) {
	ctx := span.Init("makeOffer")
	slot := n.bookSlot()
	if slot == nil {
		logger.Errorf(ctx, "Has no free slot!")
		return
	}
	var err error
	defer func() {
		if err == nil {
			return
		}
		slot.Free()
	}()

	decryptedPayload, err := crypt.DecryptMessage(in.Signal.Payload, n.privateKey)
	if err != nil {
		logger.Debugf(ctx, "crypt.DectyptMeeage: %v", err)
		return
	}

	var invite Invite
	err = invite.Unmarshal(decryptedPayload)
	if err != nil {
		logger.Debugf(ctx, "invite.Unmarshal")
		return
	}

	pubKey, err := crypt.ExtractPublicKey(invite.From)
	if err != nil {
		logger.Debugf(ctx, "crypt.ExtractPublicKey: %v", err)
		return
	}

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{
					"stun:stun.l.google.com:19302",
				},
			},
		},
	}

	logger.Debugf(ctx, "Peer connection configurating...")
	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		logger.Errorf(ctx, "webrtc.NewPeerConnection: %v", err)
		return
	}
	defer func() {
		if err == nil {
			return
		}
		pc.Close()
	}()

	dc, err := pc.CreateDataChannel("network", nil)
	if err != nil {
		logger.Errorf(ctx, "pc.CreateDataChannel: %v", err)
		return
	}
	defer func() {
		if err == nil {
			return
		}
		dc.Close()
	}()

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		logger.Errorf(ctx, "pc.CreateOffer: %v", err)
		return
	}

	if err := pc.SetLocalDescription(offer); err != nil {
		logger.Errorf(ctx, "pc.SetLocalDescription: %v", err)
		return
	}

	encryptedPayload, err := crypt.EncryptRSA(Offer{
		From: n.mesh,
		Sign: invite.Sign,
		SDP:  []byte(offer.SDP),
	}.Marshal(), pubKey)
	if err != nil {
		logger.Errorf(ctx, "crypt.EncryptMessage: %v", err)
		return
	}

	n.broadcastWithExclude(Signal{
		Type:    SingalTypeOffer,
		Payload: encryptedPayload,
	})
	logger.Debugf(ctx, "Offer was sent!")
}
