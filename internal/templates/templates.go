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
	err := tmpl.ExecuteTemplate(w, "users", users)
	if err != nil {
		logger.Errorf(nil, "Error render users: %v", err)
	}
}

func RenderChat(w http.ResponseWriter, chat Chat) {
	err := tmpl.ExecuteTemplate(w, "chat", chat)
	if err != nil {
		logger.Errorf(nil, "Error render chat: %v", err)
	}
}

func RenderMessage(w http.ResponseWriter, msg Message) {
	err := tmpl.ExecuteTemplate(w, "message", msg)
	if err != nil {
		logger.Errorf(nil, "Error render message: %v", err)
	}
}

func RenderUnread(w http.ResponseWriter, msgs []Message) {
	err := tmpl.ExecuteTemplate(w, "unread", msgs)
	if err != nil {
		logger.Errorf(nil, "Error render unread: %v", err)
	}
}
