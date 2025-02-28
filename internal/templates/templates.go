package templates

import (
	"net/http"
	"text/template"
	"time"
)

var (
	tmpl = template.Must(template.ParseFiles(
		"index.tmpl",
		"users.tmpl",
		"chat.tmpl",
		"unread.tmpl",
		"message.tmpl",
	))
)

type User struct {
	ID   string
	Name string
}

type Message struct {
	Time   time.Time
	Text   string
	IsMine bool
}

type Chat struct {
	With     string
	Messages []Message
}

func RenderIndex(w http.ResponseWriter, users []User) {
	tmpl.ExecuteTemplate(w, "index", users)
}

func RenderUsers(w http.ResponseWriter, users []User) {
	tmpl.ExecuteTemplate(w, "users", users)
}

func RenderChat(w http.ResponseWriter, chat Chat) {
	tmpl.ExecuteTemplate(w, "chat", chat)
}

func RenderMessage(w http.ResponseWriter, msg Message) {
	tmpl.ExecuteTemplate(w, "message", msg)
}

func RenderUnread(w http.ResponseWriter, msgs []Message) {
	tmpl.ExecuteTemplate(w, "unread", msgs)
}
