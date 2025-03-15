package network

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
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
	authKey                      *rsa.PrivateKey
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
	SignalTypeNeedInvite:     generateInvite,
}

func New(mesh string, authKey *rsa.PrivateKey, countOfWorkers, countOfSlots int) *Network {
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
	}
	return n
}

func (n *Network) Run(ctx context.Context) {
	n.inbox = make(chan Income)
	closer.Add(func() error {
		close(n.inbox)
		return nil
	})

	go func() {
		<-ctx.Done()
		for _, s := range n.slots {
			s.Free()
		}
		close(n.inbox)
	}()

	wg := sync.WaitGroup{}
	for i := range n.countOfWorkers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for in := range n.inbox {
				logger.Debugf(nil, "Received %s signal", in.Signal.Type)
				if n.checkCache(in.Signal.Payload) {
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
		}(i)
	}
	wg.Wait()
}

func (n *Network) ServeWs(w http.ResponseWriter, r *http.Request) {
	slot, free := n.bookSlot()
	if slot == nil {
		return
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

	mesh := r.Header.Get("Mesh")
	if strings.TrimSpace(mesh) == "" {
		err = errors.New("empty mesh")
		return
	}

	pubAuth, err := crypt.ExtractPublicKey(mesh)
	if err != nil {
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	inbox := slot.AddConn(
		&TCP{
			Mesh:    mesh,
			Conn:    conn,
			PubAuth: pubAuth,
		},
		false,
	)

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
	slot, free := n.bookSlot()
	if slot == nil {
		return errors.New("has no free slot")
	}

	closer.Add(func() error {
		free()
		return nil
	})

	resp, err := http.Get(fmt.Sprintf("http://%s/id", entrypoint))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return err
	}

	limitReader := io.LimitReader(resp.Body, 1024)
	headID, err := io.ReadAll(limitReader)
	if err != nil {
		return err
	}

	h := http.Header{}
	h.Add("Mesh", n.mesh)
	u := url.URL{Scheme: "ws", Host: entrypoint, Path: "/ws"}
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), h)
	if err != nil {
		return err
	}

	inbox := slot.AddConn(
		&TCP{
			Mesh: string(headID),
			Conn: conn,
		},
		true,
	)

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
	logger.Debugf(span.Init("Network.send"), "Going to send %s", s.Type)
	for _, slot := range n.slots {
		if slot.Mesh() != mesh {
			continue
		}
		slot.Send(s)
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
		logger.Debugf(ctx, "Reaction removed")
	}(r)
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
		if s.ConnState() > ConnStateEmpty {
			return nil, nil
		}
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

func (n *Network) upgradeConn(mesh string, newState ConnState) {
	for _, s := range n.slots {
		if s.Mesh() != mesh {
			continue
		}
		s.UpgrageConn(newState)
	}
}

func (n *Network) connectionsCount() int {
	count := 0
	for _, s := range n.slots {
		if s.ConnState() > ConnStateVerified {
			count++
		}
	}
	return count
}

func (n *Network) broadcastWithExclude(sig Signal, exclude ...string) {
	for _, s := range n.slots {
		if slices.Contains(exclude, s.Mesh()) {
			continue
		}
		if s.ConnState() < ConnStateConnected {
			continue
		}
		s.Send(sig)
	}
}
