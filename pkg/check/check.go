package check

import "errors"

var (
	ErrBlankMemberID = errors.New("blank memberID")
)

func MemberID(v string) error {
	if v == "" {
		return ErrBlankMemberID
	}

	return nil
}
