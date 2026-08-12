package picker

import (
	"context"
	"errors"
	"testing"
)

// A backend with no template source must say so rather than open an empty
// chooser the user can only escape from.
func TestLocalBackendTemplatesUnavailable(t *testing.T) {
	b := NewLocalBackend("/home", nil)
	if _, err := b.Templates(context.Background()); err == nil {
		t.Fatal("expected an error when no template source is configured")
	}
}

func TestLocalBackendTemplatesSurfacesSourceError(t *testing.T) {
	b := NewLocalBackend("/home", func() ([]string, error) { return nil, errors.New("config unreadable") })
	if _, err := b.Templates(context.Background()); err == nil {
		t.Fatal("expected the source error to propagate")
	}
}
