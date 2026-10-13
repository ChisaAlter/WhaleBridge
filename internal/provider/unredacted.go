package provider

import "net/url"

func (p Provider) SkipsRedaction() bool {
	if !p.Unredacted || p.Account != nil || p.IsRemoteMagpie() {
		return false
	}
	return LocalAddresses(p.Chat, p.Responses, p.Anthropic, p.Gemini, p.Decide)
}

func LocalAddresses(bases ...string) bool {
	n := 0
	for _, b := range bases {
		if b == "" {
			continue
		}
		u, err := url.Parse(b)
		if err != nil || u.Host == "" || !local(u.Hostname()) {
			return false
		}
		n++
	}
	return n > 0
}
