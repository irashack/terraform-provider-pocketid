package main

import (
	"io"
	"log"
	"testing"
)

// The provider's process must not write anything through Go's standard
// logger: net/http writes the content of an unsolicited response there (see
// silenceStandardLogger).
func TestSilenceStandardLogger(t *testing.T) {
	previous := log.Writer()
	t.Cleanup(func() { log.SetOutput(previous) })

	silenceStandardLogger()
	if log.Writer() != io.Discard {
		t.Fatalf("the standard logger writes to %T, want io.Discard", log.Writer())
	}
}
