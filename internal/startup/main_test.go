package startup

import (
	"os"
	"testing"

	"github.com/blackpaw-studio/leo/internal/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }
