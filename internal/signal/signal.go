package signal

import (
	"context"
	"fmt"
	"sync"
	"time"
	"udisend/internal/member"
	"udisend/internal/schedule"
	"udisend/pkg/slice"
)

type Type int8

type (
	Interface interface {
		isSignal()
	}

	WaitOffer struct {
		Who  string
		With string
	}

	ConnectionIsDone struct {
		Who  string
		With string
	}
)

type Handler struct {
	mu      sync.Mutex
	reacts  []func(Interface) bool
	members *member.Set
}

func NewHandler(members *member.Set) *Handler {
	return &Handler{members: members}
}

func (h *Handler) Run(signals <-chan Interface) {
	for s := range signals {
		switch t := s.(type) {
		case WaitOffer:
			connectionCtx, connectionDone := context.WithCancel(context.Background())
			h.mu.Lock()
			h.reacts = append(h.reacts, func(i Interface) bool {
				c, ok := i.(ConnectionIsDone)
				if !ok {
					return false
				}
				if c.Who != t.Who && c.With != t.With {
					return false
				}
				connectionDone()
				return true
			})
			h.mu.Unlock()

			schedule.After(connectionCtx, time.Minute*5, func() {
				h.members.DisconnectiWithCause(t.With, fmt.Errorf("connection with '%s' has not established", t.Who))
			})
		case ConnectionIsDone:
			executed := []int{}
			for i, r := range h.reacts {
				if ok := r(s); ok {
					executed = append(executed, i)
				}
			}

			h.mu.Lock()
			h.reacts = slice.RemoveIndexes(h.reacts, executed)
			h.mu.Unlock()
		}
	}
}

func (w WaitOffer) isSignal()        {}
func (w ConnectionIsDone) isSignal() {}
