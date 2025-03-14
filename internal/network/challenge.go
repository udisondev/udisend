package network

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"math/big"
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
	ctx := span.Init("challenge <ID:%s>", in.From)
	logger.Debugf(ctx, "Start...")

	challenge := []byte(rand.Text() + rand.Text())

	n.addReaction(3*time.Second,
		func(nextIn Income) bool {
			if nextIn.From != in.From {
				return false
			}
			if nextIn.Signal.Type != SignalTypeTestChallenge {
				return false
			}

			ctx := span.Init("Test challenge <ID:%s>", in.From)
			logger.Debugf(ctx, "Start...")

			var sig signature
			if _, err := asn1.Unmarshal(nextIn.Signal.Payload, &sig); err != nil {
				logger.Errorf(ctx, "asn1.Unmarshal: %v", err)
				return true
			}

			pubAuth, err := crypt.ExtractPublicAuth(in.From)
			if err != nil {
				logger.Errorf(ctx, "crypt.ExtractPublicAuth: %v", err)
				return true
			}

			hash := sha256.Sum256(challenge)
			if !ecdsa.Verify(
				pubAuth,
				hash[:],
				sig.R,
				sig.S,
			) {
				logger.Warnf(ctx, "Failed!")
				return true
			}

			logger.Debugf(ctx, "Success!")

			logger.Debugf(ctx, "...End")
			return true
		})

	n.send(
		in.From,
		Signal{
			Type:    SignalTypeSolveChallenge,
			Payload: challenge,
		},
	)

	logger.Debugf(ctx, "...End")
}

func solveChallenge(n *Network, in Income) {
	ctx := span.Init("solving challange of '%s'", in.From)
	logger.Debugf(ctx, "Start...")

	hash := sha256.Sum256(in.Signal.Payload)

	r, s, err := ecdsa.Sign(
		rand.Reader,
		n.privateAuth(),
		hash[:],
	)
	if err != nil {
		logger.Errorf(ctx, "ecdsa.Sign: %v", err)
		return
	}

	sig := signature{R: r, S: s}
	sigBytes, err := asn1.Marshal(sig)
	if err != nil {
		logger.Errorf(ctx, "asn1.Marshal: %v", err)
		return
	}

	n.send(
		in.From,
		Signal{
			Type:    SignalTypeTestChallenge,
			Payload: sigBytes,
		},
	)

	logger.Debugf(ctx, "...End")
}
