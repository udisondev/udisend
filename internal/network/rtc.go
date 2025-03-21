package network

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"
	. "udisend/internal/network/internal"
	"udisend/pkg/crypt"
	"udisend/pkg/logger"
	"udisend/pkg/span"

	"github.com/pion/webrtc/v4"
)

func generateInvite(n *Network, in Income) {
	ctx := span.Init("generateInvite")
	if len(n.connections) >= 10 {
		logger.Warnf(ctx, "Has no free slot")
		n.broadcastWithExclude(in.Signal, in.From)
		return
	}

	sign := make([]byte, 32)
	rand.Read(sign)
	logger.Debugf(ctx, "Sign=%s generated", hex.EncodeToString(sign))
	secret := make([]byte, 32)
	rand.Read(secret)
	logger.Debugf(ctx, "generated secret: %s", hex.EncodeToString(secret))

	mesh := string(in.Signal.Payload[26:])
	pubKey, err := crypt.ExtractPublicKey(mesh)
	if err != nil {
		logger.Errorf(ctx, "crypt.ExtractPublicKey: %v", err)
		return
	}
	payload, err := crypt.EncryptMessage(
		Invite{
			To:     mesh,
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
			if offerMsg.Signal.Type != SingalTypeNewbieOffer {
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
			var offerSDP webrtc.SessionDescription
			err = json.Unmarshal(offer.SDP, &offerSDP)
			if err != nil {
				logger.Errorf(ctx, "json.Unmarshal: %v", err)
				return true
			}
			logger.Debugf(ctx, "Received offer: %s", string(offer.SDP))
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
						n.addConn(
							offer.From,
							func(outbox <-chan Signal) <-chan Signal {
								inbox := make(chan Signal)
								readingCtx, stopReading := context.WithCancel(context.Background())

								go func() {
									defer func() {
										pc.Close()
										dataChannel.Close()
									}()

									for {
										select {
										case <-readingCtx.Done():
											return
										case s, ok := <-outbox:
											if !ok {
												stopReading()
												return
											}

											dataChannel.Send(s.Marshal())
										}
									}
								}()

								dataChannel.OnMessage(func(msg webrtc.DataChannelMessage) {
									var s Signal
									err := s.Unmarshal(msg.Data)
									if err != nil {
										stopReading()
										return
									}
									inbox <- s
								})

								return inbox
							},
							true)
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

				err = pc.SetRemoteDescription(offerSDP)
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
				anserSDP, err := json.Marshal(pc.LocalDescription())
				if err != nil {
					logger.Errorf(ctx, "json.Marshal: %v", err)
					return
				}

				payload, err := crypt.EncryptMessage(Answer{
					From: n.mesh,
					To:   string(in.Signal.Payload),
					SDP:  anserSDP,
				}.Marshal(), pubKey, n.privateKey)
				if err != nil {
					logger.Errorf(ctx, "crypt.EncryptMessage: %v", err)
					return
				}
				n.broadcastWithExclude(Signal{
					Type:    SignalTypeAnswerForNewbie,
					Payload: payload})
				logger.Debugf(ctx, "Answer was sent!")
			}()

			return true
		})

	n.broadcastWithExclude(Signal{
		Type:    SignalTypeInviteForNewbie,
		Payload: payload,
	})

	logger.Debugf(ctx, "Invite was sent!")
}

func makeOffer(n *Network, in Income) {
	ctx := span.Init("makeOffer")
	if len(n.connections) >= 10 {
		logger.Errorf(ctx, "Has no free slot!")
		return
	}

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
	SDP, err := json.Marshal(pc.LocalDescription())
	if err != nil {
		logger.Errorf(ctx, "json.Marshal: %v", err)
		return
	}

	encryptedPayload, err := crypt.EncryptMessage(Offer{
		From: n.mesh,
		Sign: invite.Sign,
		SDP:  SDP,
	}.Marshal(), pubKey, n.privateKey)
	if err != nil {
		logger.Errorf(ctx, "crypt.EncryptMessage: %v", err)
		return
	}

	n.addReaction(
		time.Second*10,
		func(answer Income) bool {
			if answer.Signal.Type != SignalTypeAnswerForNewbie {
				return false
			}
			decryptedPayload, err := crypt.DecryptMessage(answer.Signal.Payload, n.privateKey)
			if err != nil {
				logger.Debugf(ctx, "crypt.DectyptMeeage: %v", err)
				return false
			}

			var ans Answer
			err = ans.Unmarshal(decryptedPayload)
			if err != nil {
				logger.Debugf(ctx, "ans.Unmarshal: %v", err)
				return true
			}

			logger.Debugf(ctx, "Answer: %s", string(ans.SDP))
			var ansSDP webrtc.SessionDescription
			err = json.Unmarshal(ans.SDP, &ansSDP)
			if err != nil {
				logger.Debugf(ctx, "json.Unmarshal: %v", err)
				return true
			}

			dc.OnOpen(func() {
				logger.Debugf(nil, "Connected with answerrer=%s", crypt.MeshHash(ans.From))
				n.addConn(
					ans.From,
					func(outbox <-chan Signal) <-chan Signal {
						inbox := make(chan Signal)
						readingCtx, stopReading := context.WithCancel(context.Background())

						go func() {
							defer func() {
								pc.Close()
								dc.Close()
							}()

							for {
								select {
								case <-readingCtx.Done():
									return
								case s, ok := <-outbox:
									if !ok {
										stopReading()
										return
									}

									dc.Send(s.Marshal())
								}
							}
						}()

						dc.OnMessage(func(msg webrtc.DataChannelMessage) {
							var s Signal
							err := s.Unmarshal(msg.Data)
							if err != nil {
								logger.Errorf(nil, "Error s.Unmarshal: %s", err)
								stopReading()
								close(inbox)
								return
							}
							logger.Debugf(nil, "Received %s from datachannel", s.Type)
							inbox <- s
						})

						return inbox
					},
					true,
				)
			})

			n.addReaction(
				time.Second*5,
				func(secret Income) bool {
					if secret.Signal.Type != SignalTypeConnectionSecret {
						return false
					}
					n.send(in.From, Signal{Type: SignalTypeConnectionEstablished, Payload: slices.Concat(secret.Signal.Payload, []byte(ans.From))})
					return true
				},
			)

			pc.SetRemoteDescription(ansSDP)
			return true
		},
	)

	n.broadcastWithExclude(Signal{
		Type:    SingalTypeNewbieOffer,
		Payload: encryptedPayload,
	})
	logger.Debugf(ctx, "Offer was sent!")
}
