package main

import (
	"encoding/json"

	"github.com/hashicorp/yamux"
)

// Envelope – универсальная обёртка для всех сообщений.
type Envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Message – структура для чат-сообщений.
type Message struct {
	ID        string   `json:"id"`
	Sender    string   `json:"sender"`
	Recipient string   `json:"recipient"` // Если пусто – широковещательное сообщение.
	Text      string   `json:"text"`
	HopPath   []string `json:"hop_path"` // Для предотвращения циклической ретрансляции.
}

// Hello – handshake-сообщение, передаёт уникальный идентификатор и публичный (слушающий) адрес.
type Hello struct {
	Nick            string   `json:"peer_id"`
	Port            string   `json:"port"`
	PublicAddresses []string `json:"public_addresses"`
}

// Candidate – информация об узле для рекурсивного подключения.
type Candidate struct {
	Nick      string   `json:"peer_id"`
	Addresses []string `json:"address"`
}

type Member struct {
	Nick            string
	Address         string
	PublicAddresses []string
}

// CandidateMessage – сообщение, содержащее список кандидатов.
type CandidateMessage struct {
	Candidates []Candidate `json:"candidates"`
}

// Peer – информация о подключённом узле.
type Peer struct {
	session *yamux.Session
	*Member
}
