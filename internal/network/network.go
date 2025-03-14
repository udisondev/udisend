package network

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	. "udisend/internal/network/internal"
	"udisend/pkg/closer"
	"udisend/pkg/crypt"
	"udisend/pkg/logger"
	"udisend/pkg/span"

	"github.com/gorilla/websocket"
)

type Network struct {
	mesh                         string
	slots                        []*Slot
	inbox                        chan Income
	reactionsMu                  sync.Mutex
	reactions                    []*reaction
	authKey                      *ecdsa.PrivateKey
	countOfWorkers, countOfSlots int
	mcache                       map[string]struct{}
}

type reaction struct {
	mu   sync.Mutex
	fn   func(Income) bool
	done bool
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

var handlers = map[SignalType]func(*Network, Income){
	SignalTypeChallenge:      challenge,
	SignalTypeSolveChallenge: solveChallenge,
}

func New(mesh string, authKey *ecdsa.PrivateKey, countOfWorkers, countOfSlots int) *Network {
	n := &Network{
		mesh:           mesh,
		inbox:          make(chan Income),
		reactionsMu:    sync.Mutex{},
		reactions:      []*reaction{},
		authKey:        authKey,
		countOfWorkers: countOfWorkers,
		countOfSlots:   countOfSlots,
		mcache:         make(map[string]struct{}),
	}
	for range countOfSlots {
		s := &Slot{}
		n.slots = append(n.slots, s)
		closer.Add(func() error {
			s.Free()
			return nil
		})
	}
	return n
}

func (n *Network) Run(ctx context.Context) {
	ctx = span.Extend(ctx, "Network.Run")
	inbox := make(chan Income)
	closer.Add(func() error {
		close(inbox)
		return nil
	})

	go func() {
		<-ctx.Done()
		logger.Debugf(ctx, "Context closed!")
		for _, s := range n.slots {
			s.Free()
		}
		close(inbox)
	}()

	wg := sync.WaitGroup{}
	for range n.countOfWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for in := range inbox {
				if n.checkCache(in.Signal.Payload) {
					logger.Debugf(ctx, "Repeated packet")
					continue
				}
				n.putCache(in.Signal.Payload)

				for _, r := range n.reactions {
					r.react(in)
				}
				h, ok := handlers[in.Signal.Type]
				if !ok {
					continue
				}
				h(n, in)
			}
		}()
	}
	wg.Wait()
}

func (n *Network) ServeWs(w http.ResponseWriter, r *http.Request) {
	ctx := span.Init("Network.ServerWs")
	logger.Debugf(ctx, "New connection")

	slot, free := n.bookSlot()
	defer free()
	if slot == nil {
		logger.Warnf(ctx, "Has no free slots")
		return
	}

	mesh := r.Header.Get("Mesh")
	if strings.TrimSpace(mesh) == "" {
		logger.Warnf(ctx, "Empty mesh")
		return
	}

	pubAuth, err := crypt.ExtractPublicAuth(mesh)
	if err != nil {
		logger.Errorf(ctx, "crypt.ExtractPublicAuth: %v", err)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Errorf(ctx, "Error upgrade request: %v", err)
		return
	}

	inbox := slot.AddConn(&TCP{
		Mesh:    mesh,
		Conn:    conn,
		PubAuth: pubAuth,
	})

	go func() {
		defer func() {
			conn.Close()
			free()
		}()

		n.inbox <- Income{From: mesh, Signal: Signal{Type: SignalTypeChallenge}}
		for in := range inbox {
			n.inbox <- in
		}
	}()
}

func (n *Network) AttachHead(entrypoint string) error {
	ctx := span.Init("Network.AttachHead")
	logger.Debugf(ctx, "Going to request head ID by calling GET %s/id...", entrypoint)

	slot, free := n.bookSlot()
	if slot == nil {
		return errors.New("has no free slot")
	}

	resp, err := http.Get(fmt.Sprintf("http://%s/id", entrypoint))
	if err != nil {
		logger.Errorf(ctx, "Getting mesh: %v", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Errorf(ctx, "Getting mesh: %v", err)
		return err
	}

	limitReader := io.LimitReader(resp.Body, 1024)
	headID, err := io.ReadAll(limitReader)
	if err != nil {
		logger.Errorf(ctx, "Reading mesh: %v", err)
		return err
	}

	logger.Debugf(ctx, "Received head ID '%s'", string(headID))

	h := http.Header{}
	h.Add("Mesh", n.mesh)
	u := url.URL{Scheme: "ws", Host: entrypoint, Path: "/ws"}
	logger.Debugf(ctx, "Going to attach to '%s'", headID)
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), h)
	if err != nil {
		logger.Errorf(ctx, "Connecting: %v", err)
		return err
	}

	inbox := slot.AddConn(&TCP{
		Mesh: string(headID),
		Conn: conn,
	})

	go func() {
		defer func() {
			conn.Close()
			free()
		}()
		for in := range inbox {
			n.inbox <- in
		}
	}()

	return nil
}

func (n *Network) send(mesh string, s Signal) {
	for _, slot := range n.slots {
		if slot.Conn().Mesh() != mesh {
			continue
		}
		slot.Conn().Send(s)
	}
}

func (n *Network) addReaction(timeout time.Duration, fn func(Income) bool) {
	n.addReactionWithCallback(timeout, fn, func() {})
}

func (n *Network) addReactionWithCallback(timeout time.Duration, fn func(Income) bool, callback func()) {
	ctx := span.Init("interactions.addReactionWithCallback")

	for _, r := range n.reactions {
		if !r.done {
			continue
		}
		if func(r *reaction) bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			if !r.done {
				return false
			}

			logger.Debugf(ctx, "Re-use old reaction")

			r.fn = fn
			r.done = false

			go func(r *reaction) {
				<-time.After(timeout)
				go callback()
				r.mu.Lock()
				defer r.mu.Unlock()
				r.done = true
				r.fn = nil
			}(r)

			return true
		}(r) {
			return
		}

	}

	r := &reaction{fn: fn}

	n.reactionsMu.Lock()
	logger.Debugf(ctx, "Reactions locked")
	defer func() {
		n.reactionsMu.Unlock()
		logger.Debugf(ctx, "Reactions unlocked")
	}()

	logger.Debugf(ctx, "Append reactions")
	n.reactions = append(n.reactions, r)

	go func(r *reaction) {
		<-time.After(timeout)
		go callback()
		r.mu.Lock()
		defer r.mu.Unlock()
		r.done = true
		r.fn = nil
	}(r)
}

func (n *Network) privateAuth() *ecdsa.PrivateKey {
	return n.authKey
}

func (r *reaction) react(in Income) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return
	}
	done := r.fn(in)
	if done {
		r.done = true
		r.fn = nil
	}
}

func (n *Network) bookSlot() (*Slot, func()) {
	ctx := span.Init("Network.bookSlot")
	logger.Debugf(ctx, "Searching free slot...")
	for _, s := range n.slots {
		free := s.TryLock()
		if free {
			logger.Debugf(ctx, "Found!")
			return s, s.Unlock
		}
	}
	logger.Debugf(ctx, "Not found!")
	return nil, nil
}

func (n *Network) putCache(b []byte) {
	hash := sha256.Sum256(b)
	hashString := hex.EncodeToString(hash[:])
	n.mcache[hashString] = struct{}{}
	go func() {
		<-time.After(10 * time.Second)
		delete(n.mcache, hashString)
	}()
}

func (n *Network) checkCache(b []byte) bool {
	hash := sha256.Sum256(b)
	hashString := hex.EncodeToString(hash[:])
	_, ok := n.mcache[hashString]
	return ok
}
