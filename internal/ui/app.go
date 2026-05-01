// Package ui implements the fyne-based desktop GUI of the udisend
// messenger. It is a thin presentation layer over internal/messenger:
// every interaction (send, file, call) calls into the runtime, every
// runtime event lands here via the Listener interface and updates the
// widgets on fyne's main goroutine.
package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/udisondev/udisend/internal/chat"
	"github.com/udisondev/udisend/internal/messenger"
	udsstorage "github.com/udisondev/udisend/internal/storage"
	"github.com/udisondev/udisend/pkg/identity"
)

// AppState is the data the UI keeps in memory. The runtime is the source
// of truth; the UI is a denormalised, view-friendly mirror.
type AppState struct {
	mu       sync.Mutex
	contacts []udsstorage.Contact
	selected identity.Hash
	peers    map[identity.Hash]peerInfo
}

type peerInfo struct {
	state   messenger.PeerState
	call    messenger.CallState
	history []messenger.EventMessage
}

// Run boots the GUI and blocks until the main window closes. The caller's
// context can be used to shut down on signal.
func Run(ctx context.Context, mngr *messenger.Messenger) error {
	a := app.NewWithID("dev.udisend.messenger")
	a.Settings().SetTheme(theme.DefaultTheme())
	w := a.NewWindow("udisend")

	state := &AppState{peers: make(map[identity.Hash]peerInfo)}
	if cs, err := mngr.Contacts(ctx); err == nil {
		state.contacts = cs
	}

	contactsList := widget.NewList(
		func() int {
			state.mu.Lock()
			defer state.mu.Unlock()
			return len(state.contacts)
		},
		func() fyne.CanvasObject {
			return widget.NewLabel("placeholder placeholder placeholder")
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			state.mu.Lock()
			c := state.contacts[i]
			info := state.peers[c.Hash]
			state.mu.Unlock()
			marker := stateMarker(info.state)
			label := o.(*widget.Label)
			alias := c.Alias
			if alias == "" {
				alias = c.Hash.String()[:8]
			}
			label.SetText(fmt.Sprintf("%s %s", marker, alias))
		},
	)

	historyData := widget.NewMultiLineEntry()
	historyData.MultiLine = true
	historyData.Wrapping = fyne.TextWrapWord
	historyData.Disable() // read-only display

	statusLabel := widget.NewLabel("")
	statusLabel.Alignment = fyne.TextAlignTrailing

	callLabel := widget.NewLabel("")

	input := widget.NewMultiLineEntry()
	input.SetPlaceHolder("Type a message and press Send")
	input.Wrapping = fyne.TextWrapWord
	input.SetMinRowsVisible(2)

	updateChat := func() {
		state.mu.Lock()
		peer := state.selected
		info := state.peers[peer]
		state.mu.Unlock()
		if peer.IsZero() {
			historyData.SetText("Select or add a contact to start chatting.")
			callLabel.SetText("")
			statusLabel.SetText("")
			return
		}
		var b strings.Builder
		hist, err := mngr.History(ctx, peer, 200)
		if err == nil {
			for _, h := range hist {
				dir := "→"
				if h.Direction == "in" {
					dir = "←"
				}
				switch chat.MessageKind(h.Kind) {
				case chat.KindText:
					fmt.Fprintf(&b, "[%s] %s %s\n", h.When.Format("15:04:05"), dir, string(h.Body))
				case chat.KindFileOffer, chat.KindFileChunk, chat.KindFileEnd:
					// don't repeat in chat history (these are infrastructure)
				}
			}
		}
		historyData.SetText(b.String())
		statusLabel.SetText("state: " + stateText(info.state))
		switch info.call {
		case messenger.CallInviting:
			callLabel.SetText("📞 calling…")
		case messenger.CallRinging:
			callLabel.SetText("📞 incoming call from peer (use buttons)")
		case messenger.CallActive:
			callLabel.SetText("📞 in call (text + signaling only — see ASSUMPTIONS for media)")
		case messenger.CallRejected:
			callLabel.SetText("📞 call rejected")
		case messenger.CallEnded:
			callLabel.SetText("📞 call ended")
		default:
			callLabel.SetText("")
		}
	}

	refresh := func() {
		fyne.Do(func() {
			contactsList.Refresh()
			updateChat()
		})
	}

	contactsList.OnSelected = func(i widget.ListItemID) {
		state.mu.Lock()
		if i < len(state.contacts) {
			state.selected = state.contacts[i].Hash
		}
		state.mu.Unlock()
		updateChat()
	}

	addContactBtn := widget.NewButton("➕ Add contact", func() {
		hashEntry := widget.NewEntry()
		hashEntry.SetPlaceHolder("32-char hex destination hash")
		aliasEntry := widget.NewEntry()
		aliasEntry.SetPlaceHolder("Alias")
		form := dialog.NewForm("Add contact", "Add", "Cancel",
			[]*widget.FormItem{
				{Text: "Destination hash", Widget: hashEntry},
				{Text: "Alias", Widget: aliasEntry},
			},
			func(ok bool) {
				if !ok {
					return
				}
				h, err := identity.ParseHash(strings.TrimSpace(hashEntry.Text))
				if err != nil {
					dialog.ShowError(err, w)
					return
				}
				addCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				if err := mngr.AddContact(addCtx, h, strings.TrimSpace(aliasEntry.Text)); err != nil {
					dialog.ShowError(err, w)
					return
				}
				if cs, err := mngr.Contacts(ctx); err == nil {
					state.mu.Lock()
					state.contacts = cs
					state.mu.Unlock()
				}
				refresh()
			}, w)
		form.Resize(fyne.NewSize(420, 200))
		form.Show()
	})

	verifyBtn := widget.NewButton("✓ Mark verified", func() {
		state.mu.Lock()
		peer := state.selected
		state.mu.Unlock()
		if peer.IsZero() {
			return
		}
		if err := mngr.VerifyContact(ctx, peer, true); err != nil {
			dialog.ShowError(err, w)
			return
		}
		if cs, err := mngr.Contacts(ctx); err == nil {
			state.mu.Lock()
			state.contacts = cs
			state.mu.Unlock()
		}
		refresh()
	})

	sendBtn := widget.NewButton("Send", func() {
		state.mu.Lock()
		peer := state.selected
		state.mu.Unlock()
		if peer.IsZero() || strings.TrimSpace(input.Text) == "" {
			return
		}
		text := input.Text
		input.SetText("")
		go func() {
			sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if err := mngr.SendText(sendCtx, peer, text); err != nil {
				fyne.Do(func() { dialog.ShowError(err, w) })
			}
			refresh()
		}()
	})

	fileBtn := widget.NewButton("📎 File", func() {
		state.mu.Lock()
		peer := state.selected
		state.mu.Unlock()
		if peer.IsZero() {
			return
		}
		picker := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if err != nil || reader == nil {
				return
			}
			path := reader.URI().Path()
			_ = reader.Close()
			go func() {
				sendCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				defer cancel()
				if err := mngr.SendFile(sendCtx, peer, path); err != nil {
					fyne.Do(func() { dialog.ShowError(err, w) })
				}
			}()
		}, w)
		picker.Show()
	})

	startCallBtn := widget.NewButton("📞 Call", func() {
		state.mu.Lock()
		peer := state.selected
		state.mu.Unlock()
		if peer.IsZero() {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if err := mngr.StartCall(ctx, peer); err != nil {
				fyne.Do(func() { dialog.ShowError(err, w) })
			}
		}()
	})

	acceptCallBtn := widget.NewButton("✓ Accept", func() {
		state.mu.Lock()
		peer := state.selected
		state.mu.Unlock()
		if peer.IsZero() {
			return
		}
		go mngr.AcceptCall(ctx, peer)
	})

	endCallBtn := widget.NewButton("✕ End", func() {
		state.mu.Lock()
		peer := state.selected
		state.mu.Unlock()
		if peer.IsZero() {
			return
		}
		go mngr.EndCall(ctx, peer)
	})

	identitySummary := widget.NewLabel(fmt.Sprintf(
		"You: %s   |   Listening: %s",
		mngr.Identity().Public().DestinationHash().String(),
		mngr.LocalAddress(),
	))
	identitySummary.TextStyle = fyne.TextStyle{Monospace: true}

	chatPanel := container.NewBorder(
		container.NewVBox(callLabel, widget.NewSeparator()),
		container.NewVBox(
			input,
			container.NewHBox(sendBtn, fileBtn, startCallBtn, acceptCallBtn, endCallBtn),
			widget.NewSeparator(),
			statusLabel,
		),
		nil, nil,
		historyData,
	)

	contactsPanel := container.NewBorder(
		addContactBtn,
		verifyBtn,
		nil, nil,
		contactsList,
	)

	split := container.NewHSplit(contactsPanel, chatPanel)
	split.SetOffset(0.3)

	root := container.NewBorder(
		identitySummary,
		nil, nil, nil,
		split,
	)
	w.SetContent(root)
	w.Resize(fyne.NewSize(1100, 700))

	mngr.SetListener(messenger.Listener{
		OnMessage: func(e messenger.EventMessage) {
			state.mu.Lock()
			info := state.peers[e.Peer]
			info.history = append(info.history, e)
			state.peers[e.Peer] = info
			state.mu.Unlock()
			refresh()
		},
		OnFile: func(e messenger.EventFile) {
			fyne.Do(func() {
				dialog.ShowInformation(
					"File received",
					fmt.Sprintf("Saved %s\nfrom %s\nat %s",
						e.Name, e.Peer.String()[:12]+"…", e.Path),
					w,
				)
			})
		},
		OnState: func(e messenger.EventState) {
			state.mu.Lock()
			info := state.peers[e.Peer]
			info.state = e.State
			state.peers[e.Peer] = info
			state.mu.Unlock()
			refresh()
		},
		OnCall: func(e messenger.EventCall) {
			state.mu.Lock()
			info := state.peers[e.Peer]
			info.call = e.State
			state.peers[e.Peer] = info
			state.mu.Unlock()
			refresh()
		},
	})

	// Refresh contacts every 5s in case background events reach storage.
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cs, err := mngr.Contacts(ctx)
				if err != nil {
					continue
				}
				sort.Slice(cs, func(i, j int) bool {
					ai, bi := cs[i].Alias, cs[j].Alias
					if ai == bi {
						return cs[i].Hash.String() < cs[j].Hash.String()
					}
					return ai < bi
				})
				state.mu.Lock()
				state.contacts = cs
				state.mu.Unlock()
				refresh()
			}
		}
	}()

	w.SetCloseIntercept(func() {
		w.Close()
	})
	w.ShowAndRun()
	return nil
}

func stateMarker(s messenger.PeerState) string {
	switch s {
	case messenger.PeerOnline:
		return "🟢"
	case messenger.PeerConnecting:
		return "🟡"
	case messenger.PeerFailed:
		return "🔴"
	default:
		return "⚪"
	}
}

func stateText(s messenger.PeerState) string {
	switch s {
	case messenger.PeerOnline:
		return "online"
	case messenger.PeerConnecting:
		return "connecting"
	case messenger.PeerFailed:
		return "failed"
	default:
		return "offline"
	}
}
