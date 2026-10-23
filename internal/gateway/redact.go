package gateway

import (
	"context"
	"errors"
	"net/http"

	"github.com/ChisaAlter/WhaleBridge/internal/gwmw"
	"github.com/ChisaAlter/WhaleBridge/internal/masking"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

func requestMasker() (gwmw.Masker, error) {
	st, err := settings.LoadChecked()
	if err != nil {
		return nil, errors.New("privacy settings could not be read; nothing was sent")
	}
	return gwmw.Masking(masking.Wanted(st))
}

type maskingOutput[T any] struct {
	value T
	count int
}
type maskingWriter struct {
	writer http.ResponseWriter
	done   func()
}

func wrapMasker(w http.ResponseWriter, m gwmw.Masker) (http.ResponseWriter, func(), error) {
	if m == nil {
		rw, done := masking.RestoreWriter(w)
		return rw, done, nil
	}
	out, err := gwmw.Safely(func() maskingWriter { rw, done := m.Writer(w); return maskingWriter{rw, done} })
	if err != nil {
		return w, func() {}, err
	}
	if out.writer == nil || out.done == nil {
		return w, func() {}, errors.New("masking writer unavailable; nothing was sent")
	}
	return out.writer, out.done, nil
}

func redactedDestination(w http.ResponseWriter, body []byte, ctx context.Context) (http.ResponseWriter, []byte, func(), error) {
	captured, ok := ctx.Value(maskingContextKey{}).(capturedMasker)
	if !ok {
		return redacted(w, body)
	}
	return redactWith(w, body, captured.masker)
}

// redacted captures the masker before sending, and the reply writer belongs
// to that same instance even when runtime reconciliation swaps registrations.
func redacted(w http.ResponseWriter, body []byte) (http.ResponseWriter, []byte, func(), error) {
	m, err := requestMasker()
	if err != nil {
		return w, nil, func() {}, err
	}
	return redactWith(w, body, m)
}
func redactWith(w http.ResponseWriter, body []byte, m gwmw.Masker) (http.ResponseWriter, []byte, func(), error) {
	if m == nil {
		return w, body, func() {}, nil
	}
	out, err := gwmw.Safely(func() maskingOutput[[]byte] { b, n := m.JSON(body); return maskingOutput[[]byte]{b, n} })
	if err != nil {
		return w, nil, func() {}, err
	}
	if out.value == nil && len(body) > 0 {
		return w, nil, func() {}, errors.New("masking returned no request; nothing was sent")
	}
	if out.count == 0 {
		return w, out.value, func() {}, nil
	}
	rw, done, err := wrapMasker(w, m)
	return rw, out.value, done, err
}
func redactedPrompt(w http.ResponseWriter, prompt string) (http.ResponseWriter, string, func(), error) {
	m, err := requestMasker()
	if err != nil {
		return w, "", func() {}, err
	}
	if m == nil {
		return w, prompt, func() {}, nil
	}
	out, err := gwmw.Safely(func() maskingOutput[string] { s, n := m.Text(prompt); return maskingOutput[string]{s, n} })
	if err != nil {
		return w, "", func() {}, err
	}
	if out.count == 0 {
		return w, out.value, func() {}, nil
	}
	rw, done, err := wrapMasker(w, m)
	return rw, out.value, done, err
}

type maskingContextKey struct{}
type capturedMasker struct{ masker gwmw.Masker }

func captureMasking(ctx context.Context) (context.Context, error) {
	if _, ok := ctx.Value(maskingContextKey{}).(capturedMasker); ok {
		return ctx, nil
	}
	m, err := requestMasker()
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, maskingContextKey{}, capturedMasker{m}), nil
}
func maskedQuery(query string, p *provider.Provider) (string, error) {
	return maskedQueryIn(context.Background(), query, p)
}
func maskedQueryIn(ctx context.Context, query string, p *provider.Provider) (string, error) {
	if p != nil && p.SkipsRedaction() {
		return query, nil
	}
	var m gwmw.Masker
	if captured, ok := ctx.Value(maskingContextKey{}).(capturedMasker); ok {
		m = captured.masker
	} else {
		var err error
		m, err = requestMasker()
		if err != nil {
			return "", err
		}
	}
	if m == nil {
		return query, nil
	}
	return gwmw.Safely(func() string { s, _ := m.Text(query); return s })
}
func restoreMasked(ctx context.Context, text string) (string, error) {
	captured, ok := ctx.Value(maskingContextKey{}).(capturedMasker)
	if !ok {
		return "", errors.New("masking response context unavailable")
	}
	if captured.masker == nil {
		return masking.Restore(text, false), nil
	}
	return gwmw.Safely(func() string { return captured.masker.Restore(text, false) })
}

// unredactedRoute keeps the existing local-only exception: a fallback to a
// remote destination still requires masking for the entire route.
func unredactedRoute(p provider.Provider, isGroup bool, ms []provider.Member) bool {
	if isGroup {
		if len(ms) == 0 {
			return false
		}
		for _, m := range ms {
			if !m.Provider.SkipsRedaction() {
				return false
			}
		}
		return true
	}
	if !p.SkipsRedaction() {
		return false
	}
	for _, id := range p.Fallback {
		if fp, _, ok := provider.Resolve(id); ok && !fp.SkipsRedaction() {
			return false
		}
	}
	return true
}
