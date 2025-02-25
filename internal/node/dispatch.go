package node

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"
	"udisend/internal/ctxtool"
	"udisend/internal/logger"
	"udisend/internal/member"
	"udisend/internal/message"

	"github.com/pion/webrtc/v4"
	"golang.org/x/crypto/sha3"
)

type signature struct {
	R, S *big.Int
}

func (n *Node) dispatch(ctx context.Context, in message.Income) {
	ctx = ctxtool.Span(ctx, "node.dispatch")
	logger.Debugf(ctx, "Receive message from=%s data=%s", in.From, in.Message.String())

	for _, s := range n.scripts {
		s.Act(in)
	}

	switch in.Type {
	case message.ForYou:
		fmt.Printf("%s: %s\n", in.From, in.Text)
	case message.DoVerify:
		n.doVerify(ctx, in)
	case message.SolveChallenge:
		n.solveChallenge(ctx, in)
	case message.TestChallenge:
		n.checkChallenge(ctx, in)
	case message.NewConnection:
		n.requestSignsFor(in.From)
	case message.GenerateConnectionSign:
		n.generateConnectionSign(ctx, in)
	case message.SendConnectionSign:
		connectionSign := message.ParseConnectionSign(in.Text)
		n.Send(message.Outcome{
			To: connectionSign.To,
			Message: message.Message{
				Type: message.MakeOffer,
				Text: in.Text,
			},
		})
	case message.MakeOffer:
		n.makeOffer(ctx, in)
	case message.SendOffer:
		offer := message.ParseOffer(in.Text)
		n.Send(message.Outcome{
			To: offer.To,
			Message: message.Message{
				Type: message.HandleOffer,
				Text: in.Text,
			},
		})
	case message.HandleOffer:
		n.handleOffer(ctx, in)
	case message.SendAnswer:
		answer := message.ParseAnswer(in.Text)
		n.Send(message.Outcome{
			To: answer.To,
			Message: message.Message{
				Type: message.HandleAnswer,
				Text: in.Text,
			},
		})
	case message.HandleAnswer:
		n.handleAnswer(ctx, in)
	default:
		logger.Debugf(ctx, "unexpected message.MessageType: %#v", in.Type)
	}
}

func (n *Node) checkChallenge(ctx context.Context, in message.Income) {
	ctx = ctxtool.Span(ctx, fmt.Sprintf("node.checkChallenge for=%s", in.From))
	logger.Debugf(ctx, "Start...")

	actualChallenge, ok := n.waitSigning[in.From]
	if !ok {
		logger.Debugf(ctx, "Has no challenge for '%s'", in.From)
		return
	}

	sigBytes, err := hex.DecodeString(in.Text)
	if err != nil {
		logger.Errorf(ctx, "hex.DecodeString <text:%s>: %v", in.Text, err)
		return
	}

	var sig signature
	if _, err := asn1.Unmarshal(sigBytes, &sig); err != nil {
		logger.Errorf(ctx, "asn1.Unmarshal <text:%s>: %v", in.Text, err)
		return
	}

	hash := sha256.Sum256(actualChallenge.Value)
	if !ecdsa.Verify(actualChallenge.PubKey, hash[:], sig.R, sig.S) {
		logger.Errorf(ctx, "'%s' fails challenge", in.From)
		n.disonnect(in.From)
		return
	}

	logger.Debugf(ctx, "challenge successful pass")

	logger.Debugf(ctx, "...End")
	n.requestSignsFor(in.From)
}

func (n *Node) solveChallenge(ctx context.Context, in message.Income) {
	ctx = ctxtool.Span(ctx, fmt.Sprintf("node.doSign for=%s", in.From))
	logger.Debugf(ctx, "Start...")

	challenge, err := hex.DecodeString(in.Text)
	if err != nil {
		logger.Errorf(ctx, "Error decode as hex <challenge:%s>: %v", in.Text, err)
		return
	}

	hash := sha256.Sum256(challenge)

	r, s, err := ecdsa.Sign(rand.Reader, n.privateSignKey, hash[:])
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

	sigHex := hex.EncodeToString(sigBytes)
	n.Send(message.Outcome{
		To: in.From,
		Message: message.Message{
			Type: message.TestChallenge,
			Text: sigHex,
		},
	})

	logger.Debugf(ctx, "...End")
}

func (n *Node) doVerify(ctx context.Context, in message.Income) {
	ctx = ctxtool.Span(ctx, fmt.Sprintf("node.doVerify member=%s", in.From))
	logger.Debugf(ctx, "Start...")

	clstMemb, ok := n.myCluster.members[in.From]
	if !ok {
		logger.Debugf(ctx, "Has no member=%s in my cluster", in.From)
		return
	}

	challengeValue := make([]byte, 32)
	if _, err := rand.Read(challengeValue); err != nil {
		logger.Errorf(ctx, "Error generate challenge: %v", err)
		return
	}

	challengeHex := hex.EncodeToString(challengeValue)

	n.waitSigningMu.Lock()
	n.waitSigning[in.From] = challenge{For: in.From, Value: challengeValue, PubKey: clstMemb.pubKey}
	n.waitSigningMu.Unlock()

	go func() {
		<-time.After(5 * time.Second)
		logger.Debugf(ctx, "Removing challenge for '%s'", in.From)
		n.waitSigningMu.Lock()
		delete(n.waitSigning, in.From)
		n.waitSigningMu.Unlock()
	}()

	n.Send(message.Outcome{
		To: in.From,
		Message: message.Message{
			Type: message.SolveChallenge,
			Text: challengeHex,
		},
	})
	logger.Debugf(ctx, "...End")
}

func (n *Node) generateConnectionSign(ctx context.Context, in message.Income) {
	ctx = ctxtool.Span(ctx, fmt.Sprintf("node.generateConnectionSign for '%s'", in.Text))
	logger.Debugf(ctx, "Start...")
	sign, err := genSign()
	if err != nil {
		logger.Errorf(ctx, "Error generating: %v", err)
		return
	}

	n.signMapMu.Lock()
	n.signMap[in.Text] = message.ConnectionSign{
		From: n.id,
		To:   in.Text,
		Sign: sign,
		Stun: n.stunServer,
	}
	n.signMapMu.Unlock()

	go func() {
		<-time.After(2 * time.Minute)
		logger.Debugf(ctx, "sign removed")

		n.signMapMu.Lock()
		delete(n.signMap, in.Text)
		n.signMapMu.Unlock()
	}()

	n.Send(message.Outcome{
		To: in.From,
		Message: message.Message{
			Type: message.SendConnectionSign,
			Text: strings.Join([]string{n.id, in.Text, sign, n.stunServer}, "|"),
		},
	})

	logger.Debugf(ctx, "...End")
}

func (n *Node) handleAnswer(ctx context.Context, in message.Income) {
	answer := message.ParseAnswer(in.Text)
	ctx = ctxtool.Span(ctx, fmt.Sprintf("node.handleAnswer of '%s'", answer.From))
	logger.Debugf(ctx, "Start...")

	memb, ok := n.waitAnswer[answer.From]
	if !ok {
		logger.Debugf(ctx, "Has no wait answer")
		return
	}

	descr := webrtc.SessionDescription{}
	decode(answer.SDP, &descr)

	memberCtx, disconnect := context.WithCancel(ctx)
	n.AddMember(memberCtx, memb, disconnect)
	if err := memb.SetRemoteDescription(descr); err != nil {
		disconnect()
		logger.Errorf(ctx, "Error setting remote description: %v", err)
		return
	}

	logger.Debugf(ctx, "...End")
}

func (n *Node) makeOffer(ctx context.Context, in message.Income) {
	connSign := message.ParseConnectionSign(in.Text)
	ctx = ctxtool.Span(ctx, fmt.Sprintf("node.makeOffer for '%s'", connSign.From))
	logger.Debugf(ctx, "Start...")

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{connSign.Stun},
			},
		},
	}

	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		logger.Errorf(ctx, "webrtc.NewPeerConnection <stubServer:%s>: %v", connSign.Stun, err)
		return
	}

	dc, err := pc.CreateDataChannel("private", nil)
	if err != nil {
		logger.Errorf(ctx, "pc.CreateDataChannel <stubServer:%s>: %v", connSign.Stun, err)
		pc.Close()
		return
	}

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		dc.Close()
		pc.Close()
		return
	}

	if err := pc.SetLocalDescription(offer); err != nil {
		logger.Errorf(ctx, "pc.SetLocalDescription: %v", err)
		dc.Close()
		pc.Close()
		return
	}

	answered := member.NewAnswerICE(connSign.From, dc, pc)
	n.waitAnswerMu.Lock()
	n.waitAnswer[connSign.From] = answered
	n.waitAnswerMu.Unlock()

	go func() {
		<-time.After(2 * time.Minute)
		logger.Debugf(ctx, "No wait answer yet")
		n.waitAnswerMu.Lock()
		delete(n.waitAnswer, connSign.From)
		n.waitAnswerMu.Unlock()
		dc.Close()
		pc.Close()
	}()

	n.Send(message.Outcome{
		To: in.From,
		Message: message.Message{
			Type: message.SendOffer,
			Text: strings.Join([]string{
				n.id,
				connSign.From,
				connSign.Sign,
				connSign.Stun,
				encode(pc.LocalDescription()),
			}, "|"),
		},
	})

	logger.Debugf(ctx, "...End")
}

func (n *Node) handleOffer(ctx context.Context, in message.Income) {
	offer := message.ParseOffer(in.Text)
	ctx = ctxtool.Span(ctx, fmt.Sprintf("node.makeOffer for '%s'", offer.From))
	logger.Debugf(ctx, "Start...")

	actual, ok := n.signMap[offer.From]
	if !ok {
		logger.Debugf(ctx, "Has no sign")
		return
	}

	if actual.Sign != offer.Sign {
		logger.Debugf(ctx, "Signs are not equal actual=%s given=%s", actual.Sign, offer.Sign)
		return
	}

	if offer.Stun != actual.Stun {
		logger.Debugf(ctx, "Stun servers are not equal actual=%s given=%s", actual.Stun, offer.Stun)
		return
	}

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{n.stunServer},
			},
		},
	}

	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		panic(err)
	}

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		logger.Debugf(ctx, "Connection change state to '%s'", state.String())
	})

	sd := webrtc.SessionDescription{}
	decode(offer.SDP, &sd)

	err = pc.SetRemoteDescription(sd)
	if err != nil {
		panic(err)
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		panic(err)
	}

	gatherComplete := webrtc.GatheringCompletePromise(pc)

	err = pc.SetLocalDescription(answer)
	if err != nil {
		panic(err)
	}

	<-gatherComplete

	offered := member.NewOfferICE(offer.From, pc)
	memberCtx, disconnect := context.WithCancel(ctx)
	n.AddMember(memberCtx, offered, func() {
		pc.Close()
		disconnect()
	})

	n.Send(message.Outcome{
		To: in.From,
		Message: message.Message{
			Type: message.SendAnswer,
			Text: strings.Join([]string{n.id, offer.From, encode(pc.LocalDescription())}, "|"),
		},
	})

	logger.Debugf(ctx, "...End")
}

func (n *Node) requestSignsFor(ID string) {
	for memID, m := range n.members {
		if memID == ID {
			continue
		}

		logger.Debugf(nil, "Going to request signs for=%s from=%s", ID, memID)

		m.send <- message.Message{
			Type: message.GenerateConnectionSign,
			Text: ID,
		}
	}
}

func encode(obj *webrtc.SessionDescription) string {
	b, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}

	return base64.StdEncoding.EncodeToString(b)
}

func decode(in string, obj *webrtc.SessionDescription) {
	b, err := base64.StdEncoding.DecodeString(in)
	if err != nil {
		panic(err)
	}

	if err = json.Unmarshal(b, obj); err != nil {
		panic(err)
	}
}

func genSign() (string, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}

	hash := sha3.Sum256(randomBytes)

	return hex.EncodeToString(hash[:]), nil
}

func dropElements[T any](src []T, idx []int) []T {
	sort.Ints(idx)
	out := make([]T, len(src)-len(idx))
	cur := 0
	for _, i := range idx {
		if i == cur {
			cur = i + 1
			continue
		}

		out = append(out, src[cur:i]...)
		cur = i + 1
	}

	return out
}
