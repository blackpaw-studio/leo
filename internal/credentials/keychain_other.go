//go:build !darwin

package credentials

// NewSystemTokenSource reads credential files only: there is no Keychain.
func NewSystemTokenSource(home, user string) TokenSource {
	return TokenSource{User: user, Home: home}
}
