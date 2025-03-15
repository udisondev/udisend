package network

import (
	"bytes"
	"crypto/rand"
	"time"
	. "udisend/internal/network/internal"
	"udisend/pkg/closer"
	"udisend/pkg/crypt"
)

func generateInvite(n *Network, in Income) {
	slot, free := n.bookSlot()
	if slot == nil {
		n.broadcastWithExclude(in.Signal, in.From)
	}
	var err error
	defer func() {
		if err != nil {
			free()
			return
		}
		closer.Add(func() error {
			free()
			return nil
		})
	}()

	sign := rand.Text()
	pubKey, err := crypt.ExtractPublicKey(string(in.Signal.Payload))
	if err != nil {
		return
	}
	encryptedSign, err := crypt.EncryptMessage([]byte(sign), pubKey)
	if err != nil {
		return
	}

	n.addReaction(
		time.Second*20,
		func(nextIn Income) bool {
			if nextIn.Signal.Type != SingalTypeOffer {
				return false
			}
			decryptedPayload, err := crypt.DecryptMessage(nextIn.Signal.Payload, n.authKey)
			if err != nil {
				return true
			}
			var offer Offer
			offer.Unmarshal(decryptedPayload)
			if offer.From != string(in.Signal.Payload) {
				return true
			}
			if sign != string(offer.Sign) {
				return true
			}
			return true
		})
	n.broadcastWithExclude(Signal{
		Type: SignalTypeInvite,
		Payload: Invite{
			To:   in.From,
			From: n.mesh,
			Sign: encryptedSign,
		}.Marshal(),
	})
}
