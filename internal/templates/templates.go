package templates

import (
	"net/http"
	"text/template"
	"time"
	"udisend/internal/logger"
)

var (
	tmpl = template.Must(template.New("").Funcs(template.FuncMap{
		"fmtTime": func(t time.Time, layout string) string {
			return t.Format(layout)
		},
	}).ParseFiles(
		"internal/templates/index.tmpl",
		"internal/templates/users.tmpl",
		"internal/templates/chat.tmpl",
		"internal/templates/unread.tmpl",
		"internal/templates/message.tmpl",
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
	if err := tmpl.ExecuteTemplate(w, "index", users); err != nil {
		logger.Errorf(nil, "Error render index: %v", err)
	}
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
