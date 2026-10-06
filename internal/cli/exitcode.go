package cli

import "errors"

// exitCodeError is an error that asks leo to exit with a status other than
// 1, for a caller that tells failures apart by it (the bridge mod).
type exitCodeError struct {
	code int
	err  error
}

func (e exitCodeError) Error() string { return e.err.Error() }
func (e exitCodeError) Unwrap() error { return e.err }

// ExitCode is the status leo exits with for err: 0 for none, the status an
// error in its chain asks for, else 1.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var coded exitCodeError
	if errors.As(err, &coded) {
		return coded.code
	}
	return 1
}
