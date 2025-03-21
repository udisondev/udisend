package network

import (
	"bytes"
	"context"
	"crypto/rand"
	"math/big"
	"slices"
	"sync"
	"sync/atomic"
	"time"
	. "udisend/internal/network/internal"
	"udisend/pkg/crypt"
	"udisend/pkg/logger"
	"udisend/pkg/span"
)

type signature struct {
	R, S *big.Int
}

func challenge(n *Network, in Income) {
	ctx := span.Init("challenge hash=%s", in.From)
	mesh, ok := n.meshByHash(in.From)
	if !ok {
		logger.Warnf(ctx, "There is no mesh of hash=%s", in.From)
		return
	}
	logger.Debugf(ctx, "Start...")
	pubAuth, err := crypt.ExtractPublicKey(mesh)
	if err != nil {
		logger.Errorf(ctx, "crypt.ExtractPublicKey: %v", err)
		return
	}
	challengeValue := make([]byte, 32)
	rand.Read(challengeValue)
	payload, err := crypt.EncryptRSA(challengeValue, pubAuth)
	if err != nil {
		logger.Errorf(ctx, "crypt.EncryptRSA: %v", err)
		return
	}

	logger.Debugf(ctx, "Going to add waiting solved challenge")
	n.addReaction(3*time.Second,
		func(testMsg Income) bool {
			ctx := span.Init("Test challenge")
			if testMsg.Signal.Type != SignalTypeTestChallenge {
				return false
			}
			if testMsg.From != in.From {
				return false
			}
			if !bytes.Equal(challengeValue, testMsg.Signal.Payload) {
				logger.Warnf(ctx, "Failed!")
				n.disconnect(in.From)
				return true
			}

			n.upgradeConn(testMsg.From, connStateVerified)
			if len(n.connections) < 2 {
				n.upgradeConn(testMsg.From, connStateTrusted)
				return true
			}

			minReqConns := min(len(n.connections)-1, 5)
			invitesCount := atomic.Int32{}
			invitesCtx, invitesRecieved := context.WithCancel(context.Background())

			connectionEstablishedCtx, connectionsEstablished := context.WithCancel(context.Background())
			connectionsCount := atomic.Int32{}
			mu := sync.Mutex{}
			invitesMap := make(map[string][]byte, minReqConns)

			n.addReaction(
				time.Second*30,
				func(inviteMsg Income) bool {
					ctx := span.Init("handle invite")
					logger.Debugf(ctx, "Working invite reaction for=%s", in.From)
					if inviteMsg.Signal.Type != SignalTypeInviteForNewbie {
						return false
					}
					decryptedPayload, err := crypt.DecryptMessage(inviteMsg.Signal.Payload, n.privateKey)
					if err != nil {
						logger.Errorf(ctx, "crypt.DecryptMessage: %v", err)
						return false
					}
					var invite Invite
					err = invite.Unmarshal(decryptedPayload)
					if err != nil {
						logger.Errorf(ctx, "invite.Unmarshal: %v", err)
						return false
					}
					if invite.To != n.mesh {
						logger.Warnf(ctx, "Invalid direction")
						return false
					}
					if len(invite.Secret) != 32 {
						logger.Warnf(ctx, "Invalid secret")
						return false
					}

					mu.Lock()
					invitesMap[crypt.MeshHash(invite.From)] = invite.Secret
					logger.Debugf(ctx, "Added invite from %s", crypt.MeshHash(invite.From))
					mu.Unlock()

					go func() {
						select {
						case <-time.After(30 * time.Second):
						case <-invitesCtx.Done():
							logger.Debugf(ctx, "Sending answer from=%s", crypt.MeshHash(invite.From))
							n.addReaction(
								time.Duration(10*minReqConns)*time.Second,
								func(answer Income) bool {
									if answer.Signal.Type != SignalTypeAnswerForNewbie {
										return false
									}
									decryptedPayload, err := crypt.DecryptMessage(answer.Signal.Payload, n.privateKey)
									if err != nil {
										logger.Errorf(ctx, "crypt.DecryptMessage: %v", err)
										return false
									}
									var ans Answer
									err = ans.Unmarshal(decryptedPayload)
									if err != nil {
										logger.Errorf(ctx, "ans.Unmarshal: %s", err)
										return true
									}
									encryptedPayload, err := crypt.EncryptMessage(ans.Marshal(), pubAuth, n.privateKey)
									if err != nil {
										logger.Errorf(ctx, "crypt.EncryptMessage: %v", err)
										return true
									}
									n.send(in.From, Signal{Type: SignalTypeAnswerForNewbie, Payload: encryptedPayload})
									return true
								},
							)
							n.addReaction(
								time.Duration(10*minReqConns)*time.Second,
								func(offer Income) bool {
									if offer.Signal.Type != SingalTypeNewbieOffer {
										return false
									}
									if offer.From != in.From {
										return false
									}
									n.broadcastWithExclude(offer.Signal)
									return true
								},
							)
							n.addReaction(
								time.Duration(10*minReqConns)*time.Second,
								func(connEstablishedMsg Income) bool {
									if connEstablishedMsg.Signal.Type != SignalTypeConnectionEstablished {
										return false
									}
									if connEstablishedMsg.From != in.From {
										return false
									}
									givenSecret := connEstablishedMsg.Signal.Payload[:32]
									with := string(connEstablishedMsg.Signal.Payload[32:])
									actualSecret, ok := invitesMap[crypt.MeshHash(with)]
									if !ok {
										logger.Debugf(nil, "Has not invite with=%s", with)
										n.disconnect(in.From)
										return false
									}
									if !bytes.Equal(givenSecret, actualSecret) {
										logger.Debugf(ctx, "Secrets not equal given=%s expected=%s", string(givenSecret), string(actualSecret))
										n.disconnect(in.From)
										return false
									}

									if connectionsCount.Add(1) == int32(minReqConns) {
										connectionsEstablished()
										return true
									}

									return false
								},
							)

							invite.Secret = nil
							encryptedInvite, err := crypt.EncryptMessage(invite.Marshal(), pubAuth, n.privateKey)
							if err != nil {
								logger.Errorf(ctx, "crypt.EncryptMessage: %v", err)
								return
							}
							n.send(in.From, Signal{Type: SignalTypeInviteForNewbie, Payload: encryptedInvite})
						}
					}()

					if invitesCount.Add(1) == int32(minReqConns) {
						mu.Lock()
						invitesRecieved()
						go func() {
							defer mu.Unlock()
							select {
							case <-time.After(time.Duration(10*minReqConns) * time.Second):
								n.disconnect(in.From)
							case <-connectionEstablishedCtx.Done():
								n.upgradeConn(in.From, connStateTrusted)
								logger.Debugf(nil, "%s Trusted!", crypt.MeshHash(in.From))
								if minReqConns > 4 {
									n.disconnect(in.From)
								}
							}
						}()
						return true
					}

					return false
				},
			)
			n.broadcastWithExclude(Signal{
				Type:    SignalTypeNeedInviteForNewbie,
				Payload: slices.Concat([]byte(rand.Text()), []byte(n.mesh)),
			}, testMsg.From)

			return true
		})

	n.send(
		in.From,
		Signal{
			Type:    SignalTypeSolveChallenge,
			Payload: payload,
		},
	)
	logger.Debugf(ctx, "Challenge was sent")
}

func solveChallenge(n *Network, in Income) {
	solved, err := crypt.DecryptRSA(in.Signal.Payload, n.privateKey)
	if err != nil {
		return
	}

	n.send(
		in.From,
		Signal{
			Type:    SignalTypeTestChallenge,
			Payload: solved,
		},
	)

}
