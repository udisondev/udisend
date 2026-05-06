package network

import (
	"sync"
	"sync/atomic"

	"github.com/udisondev/udisend/pkg/identity"
)

// Income is one delivered unit from the network — a payload frame or a
// session terminator. Tagged with peer, session, and PeerPublic. The
// PeerPublic is resolved once per session by the network and stamped on
// every Income, so consumers can authenticate without further lookups.
//
// Income is allocated from a sync.Pool. Consumers MUST call Release()
// once they are done with the Payload — after Release(), the Payload
// slice may be re-used for a future Income, so don't retain references.
type Income struct {
	Peer       identity.Hash
	SessionID  SessionID
	PeerPublic identity.PublicIdentity
	Payload    []byte
	Final      bool

	pool *sync.Pool
}

// Release returns the *Income to its pool. Idempotent; safe to call
// from defer in handlers.
func (i *Income) Release() {
	if i == nil || i.pool == nil {
		return
	}

	p := i.pool
	*i = Income{}

	p.Put(i)
}

// incomePool reuses *Income wrappers across the lifetime of a Node.
// The Payload []byte is not pooled here — it comes from signaling's
// noise.Decrypt which currently allocates per-frame. Pooling that slice
// would require pushing buffer-as-arg API into pkg/signaling; deferred.
var incomePool = sync.Pool{
	New: func() any { return &Income{} },
}

func newIncome() *Income {
	inc := incomePool.Get().(*Income)
	inc.pool = &incomePool

	return inc
}

// incomeChannel coordinates concurrent Send + single Close of the
// Income stream a *Node hands to its consumer. Multiple goroutines
// (the per-session pump + every signaling-handler the dispatcher
// spawns) write into it, and Run owns the close. Without
// coordination, close-then-send panics ("send on closed channel").
//
// Coordination uses two primitives:
//
//   - sync/atomic.Bool `closed` — fast-path skip so steady-state Send
//     does no locking once the channel is shut.
//   - sync.RWMutex `mu` — Send takes RLock around the actual chan op,
//     Close takes Lock around the close. RWMutex semantics guarantee
//     Lock waits for every outstanding RLock holder, so Close cannot
//     race with an in-flight Send. New Sends after Close see the atomic
//     flag and bail out before they ever take RLock.
//
// Senders MUST NOT block while holding the RLock or Close starves
// behind them. Send accepts an `abort` channel and uses select to
// guarantee bounded RLock holding.
type incomeChannel struct {
	ch     chan *Income
	closed atomic.Bool
	mu     sync.RWMutex
}

func newIncomeChannel(buffer int) *incomeChannel {
	return &incomeChannel{ch: make(chan *Income, buffer)}
}

// Recv exposes the underlying receive channel for consumers. The
// channel is closed cleanly by Close, so consumers using `for inc :=
// range c.Recv()` terminate naturally on shutdown. Consumers that
// already select on a context don't need the close — n.income exists
// to feed them, not to signal lifecycle. Either pattern is fine; the
// close is preserved precisely so the simpler `for range` form keeps
// working.
func (c *incomeChannel) Recv() <-chan *Income { return c.ch }

// Send delivers inc, returning true on success. Returns false if the
// channel is closed or abort fires first; the caller is then
// responsible for releasing inc back to the pool. abort is typically
// the Node's run-context Done channel, so a node that has begun
// shutting down does not leak slots in the inbox.
func (c *incomeChannel) Send(inc *Income, abort <-chan struct{}) bool {
	if c.closed.Load() {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	// Re-check under RLock: Close may have flipped the flag between
	// the fast-path Load and the lock acquisition.
	if c.closed.Load() {
		return false
	}
	select {
	case c.ch <- inc:
		return true
	case <-abort:
		return false
	}
}

// Close marks the channel closed and shuts it down. Idempotent.
// Briefly waits for any in-flight Send (RLock holders) to finish so
// the close itself never overlaps a send. Subsequent Send calls see
// the atomic flag and return false without taking the lock.
func (c *incomeChannel) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.CompareAndSwap(false, true) {
		close(c.ch)
	}
}
