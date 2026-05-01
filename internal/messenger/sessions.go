package messenger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/udisondev/udisend/internal/chat"
	"github.com/udisondev/udisend/internal/storage"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/signaling"
	uwebrtc "github.com/udisondev/udisend/pkg/webrtc"
)

// onIncomingChannel is registered with signaling.Service. It builds a
// responder webrtc.Session and stashes the resulting peerSession in the
// map so subsequent calls to ensureSession find it.
func (m *Messenger) onIncomingChannel(peer identity.Hash, ch *signaling.Channel) {
	m.cfg.Logger.Info("messenger: incoming signaling", "peer", peer)
	pubID, err := m.lookupPeerPublic(peer)
	if err != nil {
		m.cfg.Logger.Warn("messenger: cannot resolve incoming peer", "peer", peer, "err", err)
		_ = ch.Close()
		return
	}
	rtc, err := uwebrtc.NewSession(uwebrtc.Config{
		Identity:   m.id,
		PeerPublic: pubID,
		Channel:    ch,
		Initiator:  false,
		Logger:     m.cfg.Logger,
	})
	if err != nil {
		m.cfg.Logger.Warn("messenger: webrtc init", "err", err)
		_ = ch.Close()
		return
	}
	sess := m.installSession(peer, ch, rtc)
	rtc.SetMessageHandler(func(payload []byte) { m.onChatPayload(peer, payload) })
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), uwebrtc.HandshakeTimeout)
		defer cancel()
		if err := rtc.Run(ctx); err != nil {
			m.cfg.Logger.Warn("messenger: webrtc run (responder)", "peer", peer, "err", err)
			m.markSessionFailed(sess)
			return
		}
		select {
		case <-rtc.DataReady():
			m.markSessionOnline(sess)
		case <-time.After(uwebrtc.HandshakeTimeout):
			m.markSessionFailed(sess)
		}
	}()
}

// ensureSession returns an open peer session, opening a new one if needed.
func (m *Messenger) ensureSession(ctx context.Context, peer identity.Hash) (*peerSession, error) {
	m.sessionsMu.RLock()
	if existing, ok := m.sessions[peer]; ok {
		m.sessionsMu.RUnlock()
		if existing.state == PeerOnline {
			return existing, nil
		}
		// In-flight or failed — wait for state via DataReady.
		<-existing.rtc.DataReady()
		if existing.state == PeerOnline {
			return existing, nil
		}
		return nil, errPeerUnreachable
	}
	m.sessionsMu.RUnlock()

	pubID, err := m.lookupPeerPublic(peer)
	if err != nil {
		return nil, err
	}
	sigCh, err := m.signaling.Connect(ctx, peer)
	if err != nil {
		return nil, err
	}
	rtc, err := uwebrtc.NewSession(uwebrtc.Config{
		Identity:   m.id,
		PeerPublic: pubID,
		Channel:    sigCh,
		Initiator:  true,
		Logger:     m.cfg.Logger,
	})
	if err != nil {
		_ = sigCh.Close()
		return nil, err
	}
	sess := m.installSession(peer, sigCh, rtc)
	rtc.SetMessageHandler(func(payload []byte) { m.onChatPayload(peer, payload) })

	runCtx, cancel := context.WithTimeout(context.Background(), uwebrtc.HandshakeTimeout)
	defer cancel()
	if err := rtc.Run(runCtx); err != nil {
		m.markSessionFailed(sess)
		return nil, err
	}
	select {
	case <-rtc.DataReady():
		m.markSessionOnline(sess)
		return sess, nil
	case <-runCtx.Done():
		m.markSessionFailed(sess)
		return nil, runCtx.Err()
	}
}

// installSession registers a fresh peerSession for `peer`, returning the
// stored value. It evicts any prior session so we don't leak handles.
func (m *Messenger) installSession(peer identity.Hash, sig *signaling.Channel, rtc *uwebrtc.Session) *peerSession {
	m.sessionsMu.Lock()
	defer m.sessionsMu.Unlock()
	if old, ok := m.sessions[peer]; ok {
		_ = old.rtc.Close()
		_ = old.sig.Close()
	}
	sess := &peerSession{
		peer:      peer,
		sig:       sig,
		rtc:       rtc,
		state:     PeerConnecting,
		startedAt: time.Now(),
		incoming:  make(map[chat.MessageID]*incomingFile),
	}
	m.sessions[peer] = sess
	m.emitState(peer, PeerConnecting)
	return sess
}

func (m *Messenger) markSessionOnline(sess *peerSession) {
	sess.mu.Lock()
	sess.state = PeerOnline
	sess.mu.Unlock()
	m.emitState(sess.peer, PeerOnline)
	go m.flushOutboxFor(sess.peer)
}

func (m *Messenger) markSessionFailed(sess *peerSession) {
	sess.mu.Lock()
	sess.state = PeerFailed
	sess.mu.Unlock()
	m.emitState(sess.peer, PeerFailed)
	m.sessionsMu.Lock()
	if cur, ok := m.sessions[sess.peer]; ok && cur == sess {
		delete(m.sessions, sess.peer)
	}
	m.sessionsMu.Unlock()
}

func (m *Messenger) emitState(peer identity.Hash, state PeerState) {
	if m.listener.OnState != nil {
		go m.listener.OnState(EventState{Peer: peer, State: state})
	}
}

// lookupPeerPublic gets the peer's public identity from local store first,
// then from the DHT presence record. Returns an error if neither is found.
func (m *Messenger) lookupPeerPublic(peer identity.Hash) (identity.PublicIdentity, error) {
	if c, err := m.storage.GetContact(context.Background(), peer); err == nil {
		return c.Public, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	rec, err := m.resolver.Lookup(ctx, peer)
	if err != nil {
		return identity.PublicIdentity{}, err
	}
	return rec.Public, nil
}

// onChatPayload is the receive callback wired into webrtc.Session. It
// dispatches by message kind.
func (m *Messenger) onChatPayload(peer identity.Hash, payload []byte) {
	msg, err := chat.Decode(payload)
	if err != nil {
		m.cfg.Logger.Warn("messenger: decode chat", "peer", peer, "err", err)
		return
	}
	switch msg.Kind {
	case chat.KindText:
		body := string(msg.Body)
		_, _ = m.storage.AppendMessage(context.Background(), storage.HistoryEntry{
			Peer:      peer,
			Direction: "in",
			Kind:      storage.MessageKind(msg.Kind),
			Body:      msg.Body,
			Status:    1,
			When:      msg.Timestamp,
		})
		m.sendAck(peer, msg.ID)
		if m.listener.OnMessage != nil {
			m.listener.OnMessage(EventMessage{Peer: peer, ID: msg.ID, Body: body, Timestamp: msg.Timestamp})
		}
	case chat.KindAck:
		ack, err := chat.DecodeAck(msg.Body)
		if err == nil {
			m.cfg.Logger.Debug("messenger: ack", "peer", peer, "id", ack.ID)
		}
	case chat.KindFileOffer:
		offer, err := chat.DecodeFileOffer(msg.Body)
		if err != nil {
			m.cfg.Logger.Warn("messenger: file offer decode", "err", err)
			return
		}
		m.beginIncomingFile(peer, offer)
		m.sendAck(peer, msg.ID)
	case chat.KindFileChunk:
		chunk, err := chat.DecodeFileChunk(msg.Body)
		if err != nil {
			m.cfg.Logger.Warn("messenger: file chunk decode", "err", err)
			return
		}
		m.handleIncomingChunk(peer, chunk)
	case chat.KindFileEnd:
		end, err := chat.DecodeFileEnd(msg.Body)
		if err != nil {
			m.cfg.Logger.Warn("messenger: file end decode", "err", err)
			return
		}
		m.finishIncomingFile(peer, end.FileID)
	case chat.KindCallInvite:
		m.emitCall(peer, CallRinging)
	case chat.KindCallAccept:
		m.emitCall(peer, CallActive)
	case chat.KindCallReject:
		m.emitCall(peer, CallRejected)
	case chat.KindCallEnd:
		m.emitCall(peer, CallEnded)
	default:
		m.cfg.Logger.Debug("messenger: ignore unknown msg", "kind", msg.Kind)
	}
}

func (m *Messenger) sendAck(peer identity.Hash, id chat.MessageID) {
	ack := chat.Message{
		ID:        chat.NewMessageID(),
		Kind:      chat.KindAck,
		Timestamp: time.Now().UTC(),
		Body:      chat.EncodeAck(chat.Ack{ID: id}),
	}
	if err := m.sendMessage(context.Background(), peer, ack); err != nil {
		m.cfg.Logger.Debug("messenger: ack send failed", "err", err)
	}
}

func (m *Messenger) sendMessage(ctx context.Context, peer identity.Hash, msg chat.Message) error {
	blob, err := msg.Encode()
	if err != nil {
		return err
	}
	sess, err := m.ensureSession(ctx, peer)
	if err != nil {
		return err
	}
	return sess.rtc.SendData(ctx, blob)
}

func (m *Messenger) emitCall(peer identity.Hash, state CallState) {
	if m.listener.OnCall != nil {
		go m.listener.OnCall(EventCall{Peer: peer, State: state})
	}
}

func (m *Messenger) beginIncomingFile(peer identity.Hash, offer chat.FileOffer) {
	dir := filepath.Join(m.cfg.StorageDir, "downloads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		m.cfg.Logger.Warn("messenger: mkdir downloads", "err", err)
		return
	}
	tmp, err := os.CreateTemp(dir, ".inflight-*.bin")
	if err != nil {
		m.cfg.Logger.Warn("messenger: create temp", "err", err)
		return
	}
	in := &incomingFile{
		name:    offer.Name,
		size:    offer.Size,
		sha256:  offer.SHA256Hex,
		tmpFile: tmp,
		hasher:  newSHA256(),
	}
	m.sessionsMu.RLock()
	sess := m.sessions[peer]
	m.sessionsMu.RUnlock()
	if sess == nil {
		_ = tmp.Close()
		return
	}
	sess.mu.Lock()
	sess.incoming[offer.FileID] = in
	sess.mu.Unlock()
}

func (m *Messenger) handleIncomingChunk(peer identity.Hash, chunk chat.FileChunk) {
	m.sessionsMu.RLock()
	sess := m.sessions[peer]
	m.sessionsMu.RUnlock()
	if sess == nil {
		return
	}
	sess.mu.Lock()
	in, ok := sess.incoming[chunk.FileID]
	sess.mu.Unlock()
	if !ok {
		return
	}
	if _, err := in.tmpFile.WriteAt(chunk.Data, int64(chunk.Offset)); err != nil {
		m.cfg.Logger.Warn("messenger: write chunk", "err", err)
		return
	}
	in.hasher.write(chunk.Data)
	in.written += uint64(len(chunk.Data))
}

func (m *Messenger) finishIncomingFile(peer identity.Hash, id chat.MessageID) {
	m.sessionsMu.RLock()
	sess := m.sessions[peer]
	m.sessionsMu.RUnlock()
	if sess == nil {
		return
	}
	sess.mu.Lock()
	in, ok := sess.incoming[id]
	if ok {
		delete(sess.incoming, id)
	}
	sess.mu.Unlock()
	if !ok {
		return
	}
	if err := in.tmpFile.Sync(); err != nil {
		m.cfg.Logger.Warn("messenger: sync incoming file", "err", err)
	}
	tmpPath := in.tmpFile.Name()
	_ = in.tmpFile.Close()
	finalDir := filepath.Join(m.cfg.StorageDir, "downloads")
	finalPath := filepath.Join(finalDir, sanitiseName(in.name))
	finalPath = uniquePath(finalPath)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		m.cfg.Logger.Warn("messenger: rename incoming file", "err", err)
		_ = os.Remove(tmpPath)
		return
	}
	hashOK := in.hasher.sumHex() == in.sha256 || in.sha256 == ""
	if !hashOK {
		m.cfg.Logger.Warn("messenger: file hash mismatch", "name", in.name)
	}
	if m.listener.OnFile != nil {
		m.listener.OnFile(EventFile{
			Peer:   peer,
			Name:   in.name,
			Path:   finalPath,
			Size:   in.size,
			SHA256: in.hasher.sumHex(),
		})
	}
}

func sanitiseName(name string) string {
	if name == "" {
		return "file.bin"
	}
	bad := []rune{'/', '\\', ':', '*', '?', '"', '<', '>', '|'}
	out := make([]rune, 0, len(name))
	for _, r := range name {
		skip := false
		for _, b := range bad {
			if r == b {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, r)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}

func uniquePath(p string) string {
	if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
		return p
	}
	dir := filepath.Dir(p)
	base := filepath.Base(p)
	ext := filepath.Ext(base)
	stem := base[:len(base)-len(ext)]
	for i := 1; i < 1000; i++ {
		cand := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if _, err := os.Stat(cand); errors.Is(err, os.ErrNotExist) {
			return cand
		}
	}
	return p
}
