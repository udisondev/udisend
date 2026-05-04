package network

import (
	"sync"

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
