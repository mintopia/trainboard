package update

import (
	"io"
	"log/slog"
)

// testLogger discards logs; updater tests assert behaviour, not log lines.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
