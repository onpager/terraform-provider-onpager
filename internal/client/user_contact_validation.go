package client

import (
	"errors"
	"strings"
	"unicode/utf8"
)

func ValidateManagedContactName(name string) error {
	if name == "" || strings.TrimSpace(name) != name || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 200 || strings.ContainsRune(name, 0) {
		return errors.New("display_name must contain 1–200 UTF-8 characters without NUL or surrounding whitespace")
	}
	return nil
}
