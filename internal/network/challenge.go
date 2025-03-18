package network

import (
	"bytes"
	"context"
	"crypto/rand"
	"math/big"
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
	ctx := span.Init("challenge")
	pubAuth, err := crypt.ExtractPublicKey(in.From)
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

			n.upgradeConn(testMsg.From, ConnStateVerified)
			if n.connectionsCount() < 1 {
				logger.Debugf(ctx, "Connections count less than one")
				n.upgradeConn(testMsg.From, ConnStateConnected)
				return true
			}

			n.broadcastWithExclude(Signal{
				Type:    SignalTypeNeedInvite,
				Payload: []byte(n.mesh),
			}, testMsg.From)

			minReqConns := min(n.connectionsCount(), 5)
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
					if in.Signal.Type != SignalTypeInvite {
						return false
					}
					logger.Debugf(ctx, "Received Invite")
					decryptedPayload, err := crypt.DecryptMessage(in.Signal.Payload, n.privateKey)
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
					if invite.To != in.From {
						logger.Warnf(ctx, "Invalid direction")
						return false
					}
					if len(invite.Secret) != 26 {
						logger.Warnf(ctx, "Invalid secret")
						return false
					}

					mu.Lock()
					invitesMap[inviteMsg.From] = invite.Secret
					mu.Unlock()

					go func() {
						select {
						case <-time.After(30 * time.Second):
						case <-invitesCtx.Done():
							n.addReaction(
								time.Duration(10*minReqConns)*time.Second,
								func(connEstablishedMsg Income) bool {
									if connEstablishedMsg.From != in.From {
										return false
									}
									if len(connEstablishedMsg.Signal.Payload) < 500 {
										n.disconnect(in.From)
										return false
									}
									givenSecret := connEstablishedMsg.Signal.Payload[:26]
									with := string(connEstablishedMsg.Signal.Payload[26:])
									actualSecret, ok := invitesMap[with]
									if !ok {
										n.disconnect(in.From)
										return false
									}
									if !bytes.Equal(givenSecret, actualSecret) {
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
							n.send(in.From, Signal{Type: SignalTypeInvite, Payload: encryptedInvite})
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

	n.upgradeConn(in.From, ConnStateConnected)
}
