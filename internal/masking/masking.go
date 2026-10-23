// Package masking adapts the existing redaction implementation to the
// gateway's registered masker. It adds no privacy settings of its own.
package masking

import (
	"fmt"
	"net/http"

	"github.com/ChisaAlter/WhaleBridge/internal/gwmw"
	"github.com/ChisaAlter/WhaleBridge/internal/redact"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

type Masker struct{}

func Wanted(st settings.Settings) bool {
	return st.Redact || st.RedactPersonal || len(st.RedactWords) > 0
}
func options() redact.Options {
	st, err := settings.LoadChecked()
	if err != nil {
		panic(fmt.Errorf("privacy settings could not be read"))
	}
	return redact.Options{Secrets: st.Redact, Personal: st.RedactPersonal, Kinds: st.RedactKinds, Words: st.RedactWords, Rules: st.RedactRules}
}
func (Masker) JSON(body []byte) ([]byte, int) {
	o := options()
	if !o.Secrets && !o.Personal && len(o.Words) == 0 {
		return body, 0
	}
	return redact.MaskJSON(body, o)
}
func (Masker) Text(s string) (string, int)           { return redact.Mask(s, options()) }
func (Masker) Restore(s string, escaped bool) string { return redact.Restore(s, escaped) }
func (Masker) Writer(w http.ResponseWriter) (http.ResponseWriter, func()) {
	rw := redact.NewWriter(w)
	return rw, rw.Finish
}

// Restore and RestoreWriter only recover values from the existing store.
// They never mask an outbound request and remain available after a producer
// has been unregistered, for replies to earlier requests and later media polls.
func Restore(text string, escaped bool) string { return redact.Restore(text, escaped) }
func RestoreWriter(w http.ResponseWriter) (http.ResponseWriter, func()) {
	rw := redact.NewWriter(w)
	return rw, rw.Finish
}
func SetKeyPath(path string) { redact.SetKeyPath(path) }

var _ gwmw.Masker = Masker{}
