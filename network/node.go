package network

import (
	"crypto/rand"
	"time"
)

type Node struct {
}

func NewNode(illegalConn func(string) bool) *Node {
	return &Node{}
}

type Msg struct {
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
}

func (c *Node) AddContact(ID string) (func(<-chan Msg) <-chan Msg, error) {
	return func(ch <-chan Msg) <-chan Msg {
		income := make(chan Msg)
		go func() {
			defer close(income)
			for range ch {
			}
		}()

		go func() {
			t := time.NewTicker(5 * time.Second)
			for {
				<-t.C
				income <- Msg{
					Text:      rand.Text(),
					CreatedAt: time.Now(),
				}
			}
		}()
		return income

	}, nil
}
