package otlp

import (
	"strings"
	"testing"
	"time"
)

func TestOTelParentValidation(t *testing.T) {
	valid := "00-" + strings.Repeat("a", 32) + "-" + strings.Repeat("b", 16) + "-01"
	for _, value := range []string{valid, "", strings.Replace(valid, "00-", "ff-", 1), strings.ToUpper(valid), strings.Replace(valid, strings.Repeat("a", 32), strings.Repeat("0", 32), 1), valid + "-extra", valid[:len(valid)-1] + "z"} {
		trace, parent := otelParent(value)
		if value == valid {
			if trace == "" || parent == "" {
				t.Fatal("valid parent rejected")
			}
		} else if trace != "" || parent != "" {
			t.Fatalf("accepted malformed parent: %q", value)
		}
	}
}

// fastOTel has the exporter read the agents' transcripts and send what it
// holds within a fraction of a second, not every 2s and 5s.
func fastOTel(t *testing.T) {
	every, sessionsEvery := OTelEvery, OTelSessionsEvery
	OTelEvery, OTelSessionsEvery = 200*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { OTelEvery, OTelSessionsEvery = every, sessionsEvery })
}
