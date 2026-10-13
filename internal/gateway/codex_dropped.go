package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
)

// edgeReset is the 503 ChatGPT's front (Cloudflare, then Envoy) answers,
// as text, when the backend behind it closed the connection before any
// reply.
var edgeReset = regexp.MustCompile(`(?i)upstream connect error or disconnect/reset before headers`)

// codexDropBody is the request size from which that 503 may be about the
// request. ylorn (Discord, v0.1.1179) got it for every Codex turn a PNG
// screenshot took past about 1 MiB, also without magpie. It isn't a limit:
// bodies of 0.5 to 20 MiB were answered from here (a2b96139c), and
// ScarfQian's (#1628) came back 200 on the ninth try after eight of these
// 503s through another tool. "remote connection failure" is Envoy failing
// to reach the backend, which comes and goes.
const codexDropBody = 1 << 20

// codexResetSaid is in what a ChatGPT account's reply says once
// codexDropped has read it as that 503 to a request this large: what the
// accounts loop tells it by, through its own error shape, to ask again
// without resting anybody.
const codexResetSaid = "This usually passes, so magpie asked again without resting the account."

// codexDropped is the reply, still the 503 it is, that a ChatGPT Codex
// backend gave a request of size bytes when it closed the connection
// before answering one this large. It says what happened and, should it
// keep happening, what makes the request smaller. It stays a 503 so
// magpie and then Codex ask again: told 413, Codex never does, and the
// next turn carries the same images, so the conversation was stuck on a
// drop the next try may not get (#1628). A smaller request's reset is left
// as it came.
func codexDropped(res *http.Response, size int) *http.Response {
	if res == nil || res.StatusCode != http.StatusServiceUnavailable || size < codexDropBody {
		return res
	}
	head, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
	if !edgeReset.Match(head) {
		res.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(head), res.Body), res.Body}
		return res
	}
	res.Body.Close()
	msg := fmt.Sprintf("ChatGPT's Codex backend closed the connection before answering this %.1f MiB request (503: %s). "+
		codexResetSaid+" "+
		"If it keeps happening in this conversation, its images may be why: run /compact, or remove or shrink the images (a JPEG in place of a large PNG screenshot).",
		float64(size)/(1<<20), bytes.TrimSpace(head))
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "server_error"}})
	h := res.Header.Clone()
	for _, k := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Transfer-Encoding"} {
		h.Del(k)
	}
	h.Set("Content-Type", "application/json")
	out := *res
	out.Header, out.Body, out.ContentLength = h, io.NopCloser(bytes.NewReader(b)), int64(len(b))
	return &out
}

// codexReset says a failure is a large request's reset codexDropped read:
// no account's fault, and asked again.
func codexReset(status int, body []byte) bool {
	return status == http.StatusServiceUnavailable && bytes.Contains(body, []byte(codexResetSaid))
}
