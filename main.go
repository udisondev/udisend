package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/udisondev/udisend/chat"
	"github.com/udisondev/udisend/config"
	"github.com/udisondev/udisend/network"
)

func main() {
	logFile, err := os.OpenFile("app.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		panic(err)
	}

	handler := slog.NewJSONHandler(logFile, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	logger := slog.New(handler)
	slog.SetDefault(logger)

	blackList := make(map[string]bool)
	n := network.NewNode(func(ID string) bool {
		return blackList[ID]
	})

	inEvents := make(chan chat.Income)
	outEvents, err := chat.Run(inEvents)
	if err != nil {
		return
	}

	defer fmt.Print("\033[H\033[2J")

	interactions := make(map[string]struct {
		out   chan network.Msg
		close func()
	})

	for out := range outEvents {
		switch out := out.(type) {
		case chat.AddContactReq:
			inter, err := n.AddContact(out.ID)
			if err != nil {
				inEvents <- chat.AddContactResp{
					ID:          out.ID,
					Success:     false,
					Description: err.Error(),
				}
				continue
			}

			ctx, cancel := context.WithCancel(context.Background())
			outbox := make(chan network.Msg, config.ChatOutboxPoolSize)
			inbox := inter(outbox)
			interactions[out.ID] = struct {
				out   chan network.Msg
				close func()
			}{out: outbox, close: cancel}

			go func() {
				defer close(outbox)
				defer delete(interactions, out.ID)

				for {
					select {
					case <-ctx.Done():
						return
					case in, ok := <-inbox:
						if !ok {
							inEvents <- chat.Disconnected{
								ID: out.ID,
							}
							return
						}
						inEvents <- chat.IncomeMsg{
							Author:    out.ID,
							Text:      in.Text,
							CreatedAt: in.CreatedAt,
						}
					}
				}
			}()

		case chat.RemoveContactReq:
			inter, ok := interactions[out.ID]
			if !ok {
				continue
			}

			delete(interactions, out.ID)
			if out.BlackList {
				blackList[out.ID] = true
			}
			inter.close()
			inEvents <- chat.RemoveContactResp{
				ID:      out.ID,
				Success: true,
			}
		case chat.OutcomeMsg:
			inter, ok := interactions[out.Recipient]
			if !ok {
				continue
			}
			select {
			case inter.out <- network.Msg{
				Text:      out.Text,
				CreatedAt: out.CreatedAt,
			}:
			default:
				inter.close()
				inEvents <- chat.SendMsgErr{
					Description: fmt.Sprintf("%s is not reading", out.Recipient),
				}
			}
		}
	}

}
