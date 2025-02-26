package node

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"udisend/internal/message"
)

func (n *Node) handleUsers(w http.ResponseWriter, r *http.Request) {
	usersHTML := `<h3>Пользователи</h3>
	<ul>
		<li>Пользователь1</li>
		<li>Пользователь2</li>
		<li>Пользователь3</li>
	</ul>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, usersHTML)
}

// handleMessages возвращает накопленные сообщения и очищает их, чтобы избежать дублирования
func (n *Node) handleMessages(w http.ResponseWriter, r *http.Request) {
	n.messagesMu.RLock()
	defer n.messagesMu.RUnlock()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	for member, msgs := range n.messages {
		for i, msg := range msgs {
			if msg.Read {
				continue
			}
			msgs[i].Read = true
			timestamp := time.Now().Format(time.TimeOnly)
			fmt.Fprintf(w, `<div>[%s] %s:    %s</div>`, timestamp, member, msg.Text)
		}
	}
}

// handleSend принимает сообщение от пользователя и добавляет его в список сообщений
func (n *Node) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	err := r.ParseForm()
	if err != nil {
		http.Error(w, "Ошибка парсинга формы", http.StatusBadRequest)
		return
	}
	msg := r.FormValue("message")
	if msg == "" {
		http.Error(w, "Пустое сообщение", http.StatusBadRequest)
		return
	}
	if len(msg) < 4 {
		http.Error(w, "Ваше сообщение не может быть короче 4х символов", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(msg, "/") {
		http.Error(w, "Ваше сообщение должно начинаться с '/'", http.StatusBadRequest)
		return
	}
	del := strings.Index(msg, " ")
	if del == -1 {
		http.Error(w, "После '/<recepient' должен быть пробел и ваше сообщение!", http.StatusBadRequest)
		return
	}
	if len(msg[del:]) < 2 {
		http.Error(w, "Сообщение должно быть не пустым!", http.StatusBadRequest)
		return
	}

	recepient := msg[1:del]
	timestamp := time.Now().Format(time.TimeOnly)

	n.Send(message.Outcome{
		To: recepient,
		Message: message.Message{
			Type: message.Private,
			Text: msg[del+1:],
		},
	})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div>[%s] You: %s</div>`, timestamp, msg)
}
