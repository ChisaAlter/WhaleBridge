package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

// Mantle serves its OpenAI APIs under two prefixes, each model at one:
// closed GPTs and Grok at /openai/v1, the others at /v1. A model-id guess
// refused for its route is retried once at the other prefix and remembered.
func mantleRoutes(p provider.Provider, to provider.Protocol) bool {
	return (to == provider.Chat || to == provider.Responses) && p.IsBedrockMantle()
}

func mantleRouteKey(model string) string { return "mantle-route:" + model }

// Each forward owns its actual first base, including when a test transport
// omits Response.Request. A retry explicitly picks the opposite base rather
// than consulting another request's newly learned shared route.
type mantleAttemptKey struct{}
type mantleAttempt struct{ base string }
type mantleRetryBaseKey struct{}

func (s *Server) mantleBase(ctx context.Context, p provider.Provider, to provider.Protocol, base, model string) string {
	if forced, _ := ctx.Value(mantleRetryBaseKey{}).(string); forced != "" {
		return forced
	}
	base = provider.MantleBase(base, model, !s.fits(p.ID, mantleRouteKey(model), to))
	if attempt, _ := ctx.Value(mantleAttemptKey{}).(*mantleAttempt); attempt != nil {
		attempt.base = base
	}
	return base
}

// Prefer the response's real URL (including redirects); the per-forward
// base is a safe fallback for custom transports without Response.Request.
func mantleSentBase(ctx context.Context, path string, res *http.Response) string {
	if res.Request != nil && res.Request.URL != nil {
		u := *res.Request.URL
		if strings.HasSuffix(u.Path, strings.SplitN(path, "?", 2)[0]) {
			u.Path = strings.TrimSuffix(u.Path, strings.SplitN(path, "?", 2)[0])
			u.RawPath, u.RawQuery, u.Fragment = "", "", ""
			return u.String()
		}
	}
	if attempt, _ := ctx.Value(mantleAttemptKey{}).(*mantleAttempt); attempt != nil {
		return attempt.base
	}
	return ""
}

var mantleWrongRoute = regexp.MustCompile(`(?i)(?:isn't|is not|not) supported on this route`)

// Commit only a demonstrated prefix. A failed probe never revokes a route
// learned by another request while this one's first response was in flight.
func (s *Server) mantleLearn(p provider.Provider, to provider.Protocol, model, base string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := p.ID + "\x00" + mantleRouteKey(model) + "\x00" + string(to)
	if base == provider.MantleBase(p.Base(to), model, true) {
		s.unfit[key] = true
	} else if base == provider.MantleBase(p.Base(to), model, false) {
		delete(s.unfit, key)
	}
}

func (s *Server) mantleReroute(ctx context.Context, p provider.Provider, to provider.Protocol, path string, body []byte, in http.Header, res *http.Response) (*http.Response, error) {
	model := gjson.GetBytes(body, "model").String()
	if res.StatusCode < 400 || res.StatusCode >= 500 || model == "" {
		return res, nil
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(b))
	if !mantleWrongRoute.Match(b) {
		return res, nil
	}
	sent := mantleSentBase(ctx, path, res)
	guess, other := provider.MantleBase(sent, model, false), provider.MantleBase(sent, model, true)
	var alternate string
	switch sent {
	case guess:
		alternate = other
	case other:
		alternate = guess
	}
	if sent == "" || alternate == "" || alternate == sent {
		return res, nil
	}
	retryCtx := context.WithValue(ctx, mantleRetryBaseKey{}, alternate)
	again, err := s.forwardOnce(retryCtx, p, to, path, body, in)
	if err == nil && again.StatusCode < 400 {
		s.mantleLearn(p, to, model, alternate)
		return again, nil
	}
	var ab []byte
	if err == nil {
		ab, _ = io.ReadAll(io.LimitReader(again.Body, 1<<20))
		again.Body.Close()
		again.Body = io.NopCloser(bytes.NewReader(ab))
	}
	if err == nil && !mantleWrongRoute.Match(ab) {
		// A non-routing 4xx recognizes the alternate prefix; 5xx do not
		// establish it, but their answer is still passed on unchanged.
		if again.StatusCode < 500 {
			s.mantleLearn(p, to, model, alternate)
		}
		return again, nil
	}
	// Neither prefix served it, or the retry could not reach Mantle. Keep
	// the first refusal without changing another request's learned route.
	return res, nil
}
