package machine

import (
	"os"
	"testing"

	"github.com/blackpaw-studio/leo/internal/testenv"
)

// TestMain points HOME at a temp dir so tests never touch the developer's
// real home (issue #203).
func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }
