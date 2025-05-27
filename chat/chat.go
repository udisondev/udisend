package chat

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type (
	Outcome interface {
		_isOutcome()
	}
	AddContactReq    struct{ ID string }
	RemoveContactReq struct {
		ID        string
		BlackList bool
	}
	OutcomeMsg struct {
		Recipient string
		Text      string
		CreatedAt time.Time
	}
)

type (
	Income interface {
		_isIncome()
	}
	AddContactResp struct {
		ID          string
		Success     bool
		Description string
	}
	RemoveContactResp struct {
		ID          string
		Success     bool
		Description string
	}
	IncomeMsg struct {
		Author    string
		Text      string
		CreatedAt time.Time
	}
	SendMsgErr struct {
		Description string
	}
	Disconnected struct {
		ID string
	}
)

type Chat struct {
	contactList   list.Model
	dialogs       map[string][]string
	dialog        viewport.Model
	input         textarea.Model
	contactInput  textinput.Model
	out           chan<- Outcome
	mode          ChatMode
	width, height int
}

type Contact struct {
	ID, Alias string
}

type ChatMode uint8

const (
	NoneMode ChatMode = iota
	ListMode
	WriteMode
	AddContactMode
)

const gap = "\n\n"

var (
	incomeStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	outcomeStype = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
)

func Run(income <-chan Income) (<-chan Outcome, error) {
	out := make(chan Outcome)
	p := tea.NewProgram(initChat(out))
	go func() {
		if _, err := p.Run(); err != nil {
			close(out)
		}
	}()

	go func() {
		for in := range income {
			slog.Info("Recived income", slog.Any("in", in))
			p.Send(in)
		}
	}()

	return out, nil
}

func initChat(out chan<- Outcome) *Chat {
	ta := textarea.New()
	ta.Placeholder = "Enter your message..."
	ta.Prompt = "┃ "
	ta.CharLimit = 500
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.ShowLineNumbers = false
	ta.SetWidth(30)
	ta.SetHeight(3)

	addC := textinput.New()
	addC.Placeholder = "Enter contact ID..."
	addC.Prompt = "> "
	addC.CharLimit = 100
	addC.Width = 30
	addC.Blur()

	return &Chat{
		mode:         ListMode,
		dialog:       viewport.New(30, 5),
		contactList:  list.New([]list.Item{Contact{ID: "Lovely", Alias: "Lovely"}}, list.NewDefaultDelegate(), 30, 5),
		input:        ta,
		contactInput: addC,
		dialogs:      make(map[string][]string),
		out:          out,
	}
}

func (c *Chat) Init() tea.Cmd {
	return nil
}

func (c *Chat) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.Resize(msg)
		return c, nil
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			close(c.out)
			return c, tea.Batch(tea.ClearScreen, tea.Quit)
		case tea.KeyEsc:
			c.mode = ListMode
			c.input.Blur()
		}
		switch c.mode {
		case NoneMode:
			return c.UpdateNoneMode(msg)
		case ListMode:
			return c.UpdateListMode(msg)
		case WriteMode:
			return c.UpdateWriteMode(msg)
		case AddContactMode:
			return c.UpdateAddContactMode(msg)
		}
	case IncomeMsg:
		dialog, ok := c.dialogs[msg.Author]
		if !ok {
			c.AppendContact(msg.Author)
			dialog = make([]string, 1)
		}
		dialog = append(dialog, formatIncome(msg))
		c.dialogs[msg.Author] = dialog
		if msg.Author == c.Selected().ID {
			c.RefreshDialog()
		}
	case AddContactResp:
		if !msg.Success {
			return c, nil
		}
		c.AppendContact(msg.ID)
		c.dialogs[msg.ID] = make([]string, 0)
	case Outcome:
		c.out <- msg
	}

	return c, nil
}

func (c *Chat) UpdateWriteMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var (
		inpCmd tea.Cmd
		vpCmd  tea.Cmd
	)
	c.input, inpCmd = c.input.Update(msg)
	c.dialog, vpCmd = c.dialog.Update(msg)

	if msg.Type == tea.KeyEnter {
		recip := c.Selected().ID
		outcome := OutcomeMsg{
			Recipient: recip,
			Text:      strings.TrimSuffix(c.input.Value(), "\n"),
			CreatedAt: time.Now(),
		}
		c.dialogs[c.Selected().ID] = append(c.dialogs[c.Selected().ID], formatOutcome(outcome))
		c.RefreshDialog()
		c.dialog.GotoBottom()
		c.input.Reset()
	}

	return c, tea.Batch(vpCmd, inpCmd)
}

func (c *Chat) UpdateListMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		c.contactList.CursorDown()
		c.RefreshDialog()
	case "k", "up":
		c.contactList.CursorUp()
		c.RefreshDialog()
	case "a":
		c.mode = AddContactMode
		c.contactInput.Focus()
	case "D":
		id := c.Selected().ID
		c.contactList.RemoveItem(c.contactList.Cursor())
		c.out <- RemoveContactReq{ID: id}
	case "enter":
		c.mode = WriteMode
		c.input.Focus()
	}
	return c, nil
}

func (c *Chat) UpdateNoneMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "f":
		c.mode = ListMode
	case "i":
		c.mode = WriteMode
	}
	return c, nil
}

func (c *Chat) RefreshDialog() {
	dialog := c.dialogs[c.Selected().ID]
	c.dialog.SetContent(strings.Join(dialog, "\n"))
	c.dialog.GotoBottom()
}

func (c *Chat) AppendContact(ID string) {
	c.contactList.InsertItem(len(c.contactList.Items()), Contact{ID: ID, Alias: ID})
}

func (c *Chat) Resize(ws tea.WindowSizeMsg) {
	c.width = ws.Width
	c.height = ws.Height
	c.contactList.SetWidth(c.width / 4)
	c.contactList.SetHeight(c.height)
	c.input.SetWidth(c.width / 4 * 3)
	c.dialog.Width = c.width / 4 * 3
	c.dialog.Height = c.height - c.input.Height() - lipgloss.Height(gap)
	c.dialog.GotoBottom()
}

func (c *Chat) View() string {
	if c.mode == AddContactMode {
		box := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(1, 2).
			Render(c.contactInput.View())

		// Центрируем её в окне
		return lipgloss.Place(
			c.width,
			c.height,
			lipgloss.Center,
			lipgloss.Center,
			box,
		)
	}

	cv := lipgloss.NewStyle().
		Border(lipgloss.ThickBorder(), false, true).
		Render(c.contactList.View())

	dv := lipgloss.JoinVertical(
		lipgloss.Top,
		c.dialog.View(),
		gap,
		c.input.View(),
	)

	return lipgloss.JoinHorizontal(lipgloss.Top, cv, dv)
}

func (c *Chat) Selected() Contact {
	return c.contactList.SelectedItem().(Contact)
}

func (c Contact) FilterValue() string {
	return c.Alias
}

func (c Contact) Title() string {
	return c.Alias
}

func (c Contact) Description() string {
	return c.ID
}

func formatIncome(msg IncomeMsg) string {
	return incomeStyle.Render(fmt.Sprintf("%s | %s: ", msg.CreatedAt.Format(time.DateTime), msg.Author)) + msg.Text
}

func formatOutcome(msg OutcomeMsg) string {
	return outcomeStype.Render(fmt.Sprintf("%s | You: ", msg.CreatedAt.Format(time.DateTime))) + msg.Text
}

func (c *Chat) UpdateAddContactMode(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEnter:
			c.contactInput.Blur()
			c.out <- AddContactReq{ID: strings.TrimSuffix(c.contactInput.Value(), "\n")}
			c.mode = ListMode
			return c, nil
		}
	}

	var cmd tea.Cmd
	c.contactInput, cmd = c.contactInput.Update(msg)
	return c, cmd
}

func (a AddContactReq) _isOutcome()    {}
func (m IncomeMsg) _isIncome()         {}
func (r RemoveContactReq) _isOutcome() {}
func (a AddContactResp) _isIncome()    {}
func (m OutcomeMsg) _isOutcome()       {}
func (r RemoveContactResp) _isIncome() {}
func (r SendMsgErr) _isIncome()        {}
func (r Disconnected) _isIncome()      {}
