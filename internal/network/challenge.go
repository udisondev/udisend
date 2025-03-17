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
)

type signature struct {
	R, S *big.Int
}

func challenge(n *Network, in Income) {
	pubAuth, err := crypt.ExtractPublicKey(in.From)
	if err != nil {
		return
	}
	challengeValue := []byte(rand.Text() + rand.Text())
	payload, err := crypt.EncryptMessage(challengeValue, pubAuth)
	if err != nil {
		return
	}

	n.addReaction(3*time.Second,
		func(testMsg Income) bool {
			if testMsg.From != in.From {
				return false
			}
			if testMsg.Signal.Type != SignalTypeTestChallenge {
				return false
			}
			if !bytes.Equal(challengeValue, testMsg.Signal.Payload) {
				n.disconnect(in.From)
				return true
			}

			n.upgradeConn(testMsg.From, ConnStateVerified)
			if n.connectionsCount() < 1 {
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
					if in.Signal.Type != SignalTypeInvite {
						return false
					}
					decryptedPayload, err := crypt.DecryptMessage(in.Signal.Payload, n.privateKey)
					if err != nil {
						return false
					}
					var invite Invite
					err = invite.Unmarshal(decryptedPayload)
					if err != nil {
						return false
					}
					if invite.To != in.From {
						return false
					}
					if len(invite.Secret) != 26 {
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

							inviteMsg.Signal.Payload = inviteMsg.Signal.Payload[:len(inviteMsg.Signal.Payload)-26]
							n.send(in.From, inviteMsg.Signal)
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
}

func solveChallenge(n *Network, in Income) {
	solved, err := crypt.DecryptMessage(in.Signal.Payload, n.privateKey)
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
