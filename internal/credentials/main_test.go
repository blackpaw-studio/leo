package credentials

import (
	"os"
	"testing"

	"github.com/blackpaw-studio/leo/internal/testenv"
)

// TestMain points HOME at a temp dir so tests never touch the developer's
// real ~/.claude, ~/.codex or Keychain-adjacent files.
func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }
