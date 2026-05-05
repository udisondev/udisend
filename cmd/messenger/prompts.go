package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// prompter wraps stdin in a single shared bufio.Scanner. The previous CLI
// recreated `bufio.NewReader(os.Stdin)` per prompt, which dropped piped
// input whenever the buffer pre-read past a newline. With one Scanner we
// keep state and Pipes / heredocs work end-to-end.
type prompter struct {
	in   io.Reader
	out  io.Writer
	scan *bufio.Scanner
}

func newPrompter(in io.Reader, out io.Writer) *prompter {
	return &prompter{in: in, out: out, scan: bufio.NewScanner(in)}
}

// readLine prints prompt and returns the next stdin line (without trailing
// newline). Returns io.EOF if stdin closed.
func (p *prompter) readLine(prompt string) (string, error) {
	fmt.Fprint(p.out, prompt)
	if !p.scan.Scan() {
		if err := p.scan.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}

	return p.scan.Text(), nil
}

// readYesNo reads a y/n with default. "yes/y/да/д" → true, "no/n/нет/н" → false.
func (p *prompter) readYesNo(prompt string, def bool) (bool, error) {
	hint := "Y/n"
	if !def {
		hint = "y/N"
	}
	for {
		raw, err := p.readLine(fmt.Sprintf("%s [%s]: ", prompt, hint))
		if err != nil {
			return false, err
		}
		raw = strings.ToLower(strings.TrimSpace(raw))
		switch raw {
		case "":
			return def, nil
		case "y", "yes", "д", "да":
			return true, nil
		case "n", "no", "н", "нет":
			return false, nil
		}
		fmt.Fprintln(p.out, "  answer y or n")
	}
}

// readPassphrase reads from controlling TTY without echo. Falls back to
// the shared scanner when stdin is not a TTY (CI, piped input). The
// fallback path is necessary for our smoke tests and any operator who
// pipes secrets from a password manager.
func (p *prompter) readPassphrase(prompt string) (string, error) {
	fmt.Fprint(p.out, prompt)

	f, ok := p.in.(*os.File)
	if ok && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(p.out)
		if err != nil {
			return "", err
		}

		return string(b), nil
	}

	if !p.scan.Scan() {
		if err := p.scan.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}

	return p.scan.Text(), nil
}

// readPassphraseTwice prompts for a passphrase, then a confirmation, and
// returns the value only when they match and pass MinPassphraseLen.
func (p *prompter) readPassphraseTwice(minLen int) (string, error) {
	first, err := p.readPassphrase("Passphrase: ")
	if err != nil {
		return "", err
	}
	if len(first) < minLen {
		return "", fmt.Errorf("passphrase must be at least %d characters", minLen)
	}
	second, err := p.readPassphrase("Confirm:    ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("passphrases do not match")
	}

	return first, nil
}
