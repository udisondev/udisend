package check

import "errors"

var (
	ErrBlankNickname = errors.New("blank nickname")
)

func Nickname(v string) error {
	if v == "" {
		return ErrBlankNickname
	}

	return nil
}
