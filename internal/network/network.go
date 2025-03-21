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
	meshHash                     string
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
	mu         sync.Mutex
	mesh       string
	meshHash   string
	outbox     chan Signal
	disconnect func()
	state      connState
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
		meshHash:       crypt.MeshHash(mesh),
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

	if len(n.connections) >= 10 {
		logger.Warnf(ctx, "Has no free slot!")
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Errorf(ctx, "upgrader.Upgrade: %v", err)
		return
	}

	n.addConn(
		mesh,
		func(outbox <-chan Signal) <-chan Signal {
			inbox := make(chan Signal)

			readCtx, stopReading := context.WithCancel(context.Background())
			go func() {
				defer func() {
					recover()
					stopReading()
				}()

				for s := range outbox {
					w, err := conn.NextWriter(websocket.BinaryMessage)
					if err != nil {
						logger.Errorf(ctx, "t.Conn.NextWriter: %v", err)
						break
					}

					_, err = w.Write(s.Marshal())
					if err != nil {
						logger.Errorf(ctx, "w.Write: %v", err)
					}

					if err := w.Close(); err != nil {
						logger.Errorf(ctx, "w.Close: %v", err)
						break
					}
				}
			}()

			go func() {
				defer func() {
					recover()
					close(inbox)
				}()
			loop:
				for {
					select {
					case <-readCtx.Done():
						break loop
					default:
						_, b, err := conn.ReadMessage()
						if err != nil {
							if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
								break loop
							}
							continue
						}

						var s Signal
						err = s.Unmarshal(b)
						if err != nil {
							logger.Errorf(ctx, "s.Unmarshal: %v", err)
							break loop
						}

						inbox <- s
					}
				}
			}()

			return inbox
		},
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

	handlers[SignalTypeInviteForNewbie] = func(n *Network, in Income) {
		if in.From != headHesh {
			return
		}

		makeOffer(n, in)
	}

	h := http.Header{}
	h.Add("Mesh", n.mesh)
	u := url.URL{Scheme: "ws", Host: entrypoint, Path: "/ws"}
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), h)
	if err != nil {
		logger.Errorf(ctx, "websocket.DefaultDealer.Dial: %v", err)
		return err
	}

	n.addConn(
		string(headID),
		func(outbox <-chan Signal) <-chan Signal {
			inbox := make(chan Signal)

			readCtx, stopReading := context.WithCancel(context.Background())
			go func() {
				defer func() {
					recover()
					stopReading()
				}()

				for s := range outbox {
					w, err := conn.NextWriter(websocket.BinaryMessage)
					if err != nil {
						logger.Errorf(ctx, "t.Conn.NextWriter: %v", err)
						break
					}

					_, err = w.Write(s.Marshal())
					if err != nil {
						logger.Errorf(ctx, "w.Write: %v", err)
					}

					if err := w.Close(); err != nil {
						logger.Errorf(ctx, "w.Close: %v", err)
						break
					}
				}
			}()

			go func() {
				defer func() {
					recover()
					close(inbox)
				}()
			loop:
				for {
					select {
					case <-readCtx.Done():
						break loop
					default:
						_, b, err := conn.ReadMessage()
						if err != nil {
							if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
								break loop
							}
							continue
						}

						var s Signal
						err = s.Unmarshal(b)
						if err != nil {
							logger.Errorf(ctx, "s.Unmarshal: %v", err)
							break loop
						}

						inbox <- s
					}
				}
			}()

			return inbox
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
		logger.Debugf(nil, "Going to broadcast to=%s", conn.meshHash)
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
	conn.disconnect()
}

func (n *Network) addConn(mesh string, interact func(<-chan Signal) <-chan Signal, trusted bool) {
	outbox := make(chan Signal, 256)
	n.connectionsMu.Lock()
	defer n.connectionsMu.Unlock()
	hash := crypt.MeshHash(mesh)

	newConn := &connection{
		mesh:     mesh,
		meshHash: hash,
		outbox:   outbox,
		disconnect: sync.OnceFunc(func() {
			close(outbox)
		}),
		state: connStateInit,
	}

	n.connections[hash] = newConn
	if trusted {
		newConn.state = connStateTrusted
	}

	inbox := interact(outbox)
	if !trusted {
		n.inbox <- Income{From: newConn.meshHash, Signal: Signal{Type: SignalTypeChallenge}}
	}

	go func() {
		ctx := span.Init("Inbox=%s", newConn.meshHash)
		defer func() {
			n.connectionsMu.Lock()
			defer n.connectionsMu.Unlock()
			delete(n.connections, hash)
			logger.Debugf(ctx, "Connection deleted")
		}()

		for in := range inbox {
			if n.checkCache(in.Payload) {
				logger.Debugf(ctx, "Is duplicate")
				continue
			}
			n.putCache(in.Payload)
			n.inbox <- Income{From: hash, Signal: in}
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
