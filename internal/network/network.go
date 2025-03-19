package network

import (
	"context"
	"crypto/rand"
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
	entryPointHash               string
	inbox                        chan Income
	reactionsMu                  sync.Mutex
	reactions                    map[string]*reaction
	privateKey                   *rsa.PrivateKey
	countOfWorkers, countOfSlots int
	mcache                       map[string]struct{}
	connectionsMu                sync.RWMutex
	connections                  map[string]*connection
}

type connection struct {
	mu       sync.Mutex
	mesh     string
	meshHash string
	outbox   chan Signal
	state    connState
}

type connectable interface {
	Mesh() string
	Hash() string
	Interact(outbox <-chan Signal) <-chan Income
}

type reaction struct {
	mu   sync.Mutex
	fn   func(Income) bool
	done bool
}

type connState uint

const (
	connStateInit connState = iota
	connStateVerified
	connStateTrusted
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

var handlers = map[SignalType]func(*Network, Income){
	SignalTypeChallenge:           challenge,
	SignalTypeSolveChallenge:      solveChallenge,
	SignalTypeNeedInviteForNewbie: generateInvite,
}

func New(mesh string, authKey *rsa.PrivateKey, countOfWorkers, countOfSlots int) *Network {
	n := &Network{
		mesh:           mesh,
		inbox:          make(chan Income),
		reactionsMu:    sync.Mutex{},
		reactions:      make(map[string]*reaction),
		privateKey:     authKey,
		countOfWorkers: countOfWorkers,
		countOfSlots:   countOfSlots,
		mcache:         make(map[string]struct{}),
		connections:    make(map[string]*connection),
	}
	return n
}

func (n *Network) Run(ctx context.Context) {
	n.inbox = make(chan Income)
	closer.Add(func() error {
		close(n.inbox)
		return nil
	})

	wg := sync.WaitGroup{}
	for i := range n.countOfWorkers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for in := range n.inbox {
				logger.Debugf(ctx, "Received %s from=%s", in.Signal.Type, in.From)

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
	ctx := span.Init("Network.ServeWs")
	mesh := r.Header.Get("Mesh")
	if strings.TrimSpace(mesh) == "" {
		return
	}

	pubAuth, err := crypt.ExtractPublicKey(mesh)
	if err != nil {
		logger.Errorf(ctx, "crypt.ExtractPublicKey: %v", err)
		return
	}

	if len(n.connections) >= 10 {
		logger.Warnf(ctx, "Has no free slot!")
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Errorf(ctx, "upgrader.Upgrade: %v", err)
		return
	}

	tcp := &TCP{
		ConnMesh: mesh,
		Conn:     conn,
		PubAuth:  pubAuth,
	}

	n.addConn(
		tcp,
		false,
	)
}

func (n *Network) AttachHead(entrypoint string) error {
	ctx := span.Init("Network.AttachHead")

	resp, err := http.Get(fmt.Sprintf("http://%s/id", entrypoint))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Errorf(ctx, "Error getting entrypoint Mesh <StatusCode:%s>", resp.Status)
		return err
	}

	limitReader := io.LimitReader(resp.Body, 1024)
	headID, err := io.ReadAll(limitReader)
	if err != nil {
		logger.Errorf(ctx, "io.ReadAll: %v", err)
		return err
	}
	headHesh := crypt.MeshHash(string(headID))

	if len(n.connections) >= 10 {
		logger.Errorf(ctx, "Had no free slot!")
		return errors.New("busy slots")
	}

	n.addReaction(
		time.Second*5,
		func(in Income) bool {
			if in.Signal.Type != SignalTypeInviteForNewbie {
				return false
			}
			if in.From != headHesh {
				return false
			}

			go makeOffer(n, in)
			return true
		},
	)
	h := http.Header{}
	h.Add("Mesh", n.mesh)
	u := url.URL{Scheme: "ws", Host: entrypoint, Path: "/ws"}
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), h)
	if err != nil {
		logger.Errorf(ctx, "websocket.DefaultDealer.Dial: %v", err)
		return err
	}

	n.addConn(
		&TCP{
			ConnMesh: string(headID),
			Conn:     conn,
		},
		true,
	)

	return nil
}

func (n *Network) send(mesh string, s Signal) {
	ctx := span.Init("Network.send")
	n.connectionsMu.RLock()
	defer n.connectionsMu.RUnlock()
	conn, ok := n.connections[mesh]
	if !ok {
		return
	}

	logger.Debugf(ctx, "Sending %s to %s", s.Type, conn.meshHash)

	conn.outbox <- s
}

func (n *Network) addReaction(timeout time.Duration, fn func(Income) bool) {
	n.addReactionWithCallback(timeout, fn, func() {})
}

func (n *Network) addReactionWithCallback(timeout time.Duration, fn func(Income) bool, callback func()) {
	n.reactionsMu.Lock()
	defer n.reactionsMu.Unlock()

	key := rand.Text()
	r := reaction{fn: fn}
	n.reactions[key] = &r
	go func() {
		<-time.After(timeout)
		callback()
		n.reactionsMu.Lock()
		defer n.reactionsMu.Unlock()
		delete(n.reactions, key)
	}()
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

func (n *Network) broadcastWithExclude(sig Signal, exclude ...string) {
	n.connectionsMu.RLock()
	defer n.connectionsMu.RUnlock()
	for _, conn := range n.connections {
		if slices.Contains(exclude, conn.mesh) {
			continue
		}
		if conn.state < connStateTrusted {
			continue
		}
		conn.outbox <- sig
	}
}

func (n *Network) disconnect(mesh string) {
	logger.Debugf(span.Init("Network.disconnect: %s", mesh), "Disconnected!")
	n.connectionsMu.Lock()
	defer n.connectionsMu.Unlock()
	conn, ok := n.connections[mesh]
	if !ok {
		return
	}
	close(conn.outbox)
}

func (n *Network) addConn(conn connectable, trusted bool) {
	outbox := make(chan Signal, 256)
	n.connectionsMu.Lock()
	defer n.connectionsMu.Unlock()
	newConn := &connection{
		mesh:     conn.Mesh(),
		meshHash: conn.Hash(),
		outbox:   outbox,
		state:    connStateInit,
	}

	n.connections[newConn.meshHash] = newConn
	if trusted {
		newConn.state = connStateTrusted
	}

	inbox := conn.Interact(outbox)
	if !trusted {
		n.inbox <- Income{From: newConn.meshHash, Signal: Signal{Type: SignalTypeChallenge}}
	}

	go func() {
		ctx := span.Init("Inbox=%s", newConn.meshHash)
		defer func() {
			n.connectionsMu.Lock()
			defer n.connectionsMu.Unlock()
			delete(n.connections, conn.Mesh())
			logger.Debugf(ctx, "Connection deleted")
		}()

		logger.Debugf(ctx, "Ready to handle")
		for in := range inbox {
			if n.checkCache(in.Signal.Payload) {
				continue
			}
			n.putCache(in.Signal.Payload)
			n.inbox <- in
		}
	}()
}

func (n *Network) upgradeConn(mesh string, newState connState) {
	n.connectionsMu.RLock()
	defer n.connectionsMu.RUnlock()
	conn, ok := n.connections[mesh]
	if !ok {
		return
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.state = newState
}

func (n *Network) meshByHash(hash string) (string, bool) {
	n.connectionsMu.RLock()
	defer n.connectionsMu.RUnlock()
	conn, ok := n.connections[hash]
	if !ok {
		return "", false
	}
	return conn.mesh, true
}
