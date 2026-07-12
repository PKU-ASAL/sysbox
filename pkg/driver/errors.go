package driver

import (
	"errors"
	"fmt"
)

type ErrorCategory string

const (
	ErrorNotFound         ErrorCategory = "not-found"
	ErrorUnavailable      ErrorCategory = "unavailable"
	ErrorPermissionDenied ErrorCategory = "permission-denied"
	ErrorInvalidState     ErrorCategory = "invalid-state"
	ErrorUnsupported      ErrorCategory = "unsupported"
)

type Error struct {
	Category        ErrorCategory
	Driver, Message string
	Err             error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("driver %s: %s: %v", e.Driver, e.Message, e.Err)
	}
	return fmt.Sprintf("driver %s: %s", e.Driver, e.Message)
}
func (e *Error) Unwrap() error { return e.Err }
func Wrap(category ErrorCategory, driver, message string, err error) error {
	return &Error{Category: category, Driver: driver, Message: message, Err: err}
}
func IsCategory(err error, category ErrorCategory) bool {
	var target *Error
	return errors.As(err, &target) && target.Category == category
}
