package network

import (
	"bytes"
	"crypto/rand"
	"math/big"
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
		func(nextIn Income) bool {
			if nextIn.From != in.From {
				return false
			}
			if nextIn.Signal.Type != SignalTypeTestChallenge {
				return false
			}
			if !bytes.Equal(challengeValue, nextIn.Signal.Payload) {
				return true
			}

			n.upgradeConn(nextIn.From, ConnStateVerified)
			if n.connectionsCount() < 1 {
				n.upgradeConn(nextIn.From, ConnStateConnected)
				return true
			}

			n.broadcastWithExclude(Signal{
				Type: SignalTypeNeedInvite,
			}, nextIn.From)

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
	solved, err := crypt.DecryptMessage(in.Signal.Payload, n.authKey)
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
