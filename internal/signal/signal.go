package signal

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
	"udisend/internal/member"
	"udisend/internal/scheduler"
)

type Type int8

type Interface interface {
	Type() Type
}

type WaitConnection struct {
	From string
}

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

			scheduler.After(connectionCtx, time.Minute*5, func() {
				h.members.DisconnectiWithCause(t.With, errors.New("had not connected"))
			})
		case ConnectionIsDone:
			executed := []int{}
			for i, r := range h.reacts {
				if ok := r(s); ok {
					executed = append(executed, i)
				}
			}

			h.mu.Lock()
			h.reacts = removeIndexes(h.reacts, executed)
			h.mu.Unlock()
		}
	}
}

func removeIndexes[T any](s []T, indexes []int) []T {
	idxs := append([]int(nil), indexes...)
	sort.Sort(sort.Reverse(sort.IntSlice(idxs)))

	for _, idx := range idxs {
		if idx < 0 || idx >= len(s) {
			continue
		}
		s = append(s[:idx], s[idx+1:]...)
	}
	return s
}

type WaitOffer struct {
	Who  string
	With string
}

type ConnectionIsDone struct {
	Who  string
	With string
}

func (s ConnectionIsDone) Type() Type {
	return TypeConnectionIsDone
}

func (s WaitOffer) Type() Type {
	return WaitOfferType
}

func (s WaitConnection) Type() Type {
	return TypeWaitConnection
}

type Struct struct {
	Type Type
}

const (
	TypeWaitConnection   Type = 0
	WaitOfferType             = 1
	TypeConnectionIsDone      = 2
)
