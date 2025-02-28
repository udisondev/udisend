package node

import (
	"cmp"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"
	"udisend/internal/message"
	"udisend/internal/templates"
)

func (n *Node) usersHandler(w http.ResponseWriter, r *http.Request) {
	templates.RenderUsers(w, n.loadUsers())
}

func (n *Node) loadUsers() []templates.User {
	n.membersMu.RLock()
	defer n.membersMu.RUnlock()

	users := make([]templates.User, len(n.members))
	for id := range maps.Keys(n.members) {
		users = append(users, templates.User{
			ID:   id,
			Name: id,
		})
	}

	return slices.SortedFunc(slices.Values(users), func(a, b templates.User) int {
		return cmp.Compare(a.ID, b.ID)
	})
}

func (n *Node) indexHandler(w http.ResponseWriter, r *http.Request) {
	templates.RenderIndex(w, n.loadUsers())
}

func (n *Node) messagesHandler(w http.ResponseWriter, r *http.Request) {
	user := r.URL.Query().Get("user_id")
	if user == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	n.messagesMu.RLock()
	defer n.messagesMu.RUnlock()

	messages, ok := n.messages[user]
	if !ok {
		templates.RenderChat(w, templates.Chat{
			With:     user,
			Messages: nil,
		})
		return
	}

	msgs := make([]templates.Message, 0, len(messages))
	for i, m := range messages {
		messages[i].Read = true
		msgs = append(msgs, templates.Message{
			IsMine: n.id == m.From,
			Time:   m.Time,
			Text:   m.Text,
		})
	}

	templates.RenderChat(w, templates.Chat{
		With:     user,
		Messages: msgs,
	})
}

func (n *Node) unreadHandler(w http.ResponseWriter, r *http.Request) {
	user := r.URL.Query().Get("user_id")
	if user == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	n.messagesMu.RLock()
	defer n.messagesMu.RUnlock()

	messages, ok := n.messages[user]
	if !ok {
		templates.RenderChat(w, templates.Chat{
			With:     user,
			Messages: nil,
		})
		return
	}

	msgs := make([]templates.Message, 0, len(messages))
	for i, m := range messages {
		if m.Read {
			continue
		}
		messages[i].Read = true
		msgs = append(msgs, templates.Message{
			IsMine: n.id == m.From,
			Time:   m.Time,
			Text:   m.Text,
		})
	}

	if len(msgs) < 1 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	templates.RenderUnread(w, msgs)
}

// handleSend принимает сообщение от пользователя и добавляет его в список сообщений
func (n *Node) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}

	recepient := r.FormValue("to_id")
	if recepient == "" {
		return
	}
	text := r.FormValue("content")
	if strings.TrimSpace(text) == "" {
		return
	}

	n.messagesMu.Lock()
	defer n.messagesMu.Unlock()

	chat, ok := n.messages[recepient]
	if !ok {
		chat = make([]message.PrivateMessage, 0)
	}
	n.messages[recepient] = append(
		chat,
		message.PrivateMessage{
			From: n.id,
			Text: text,
			Read: false,
			Time: time.Time{},
		})

	n.Send(message.Outcome{
		To: recepient,
		Message: message.Message{
			Type: message.Private,
			Text: text,
		},
	})

	templates.RenderMessage(w, templates.Message{
		Time:   time.Now(),
		Text:   text,
		IsMine: true,
	})

}
