package peerinbox

import (
	"os"
	"testing"

	"github.com/blackpaw-studio/leo/internal/testenv"
)

// TestMain points HOME at a temp dir so tests never touch the developer's
// real ~/.claude.json, ~/.claude/, ~/.codex or ~/.leo (issue #203).
func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }
