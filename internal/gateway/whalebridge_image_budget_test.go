package gateway

import (
	"bytes"
	"container/list"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

func imageBudgetCache(t *testing.T, budget, most int) func() int64 {
	t.Helper()
	old, oldBudget, oldMost := fitted, fittedBudget, fittedMost
	fitted = &fitCache{m: map[[32]byte]*list.Element{}}
	fittedBudget, fittedMost = budget, most
	t.Cleanup(func() { fitted, fittedBudget, fittedMost = old, oldBudget, oldMost })
	from := refits.Load()
	return func() int64 { return refits.Load() - from }
}

func imageBudgetURL(t *testing.T, m image.Image, format string) string {
	t.Helper()
	var b bytes.Buffer
	var err error
	switch format {
	case "jpeg":
		err = jpeg.Encode(&b, m, &jpeg.Options{Quality: 95})
	case "gif":
		err = gif.Encode(&b, m, nil)
	default:
		err = png.Encode(&b, m)
	}
	if err != nil {
		t.Fatal(err)
	}
	return "data:image/" + format + ";base64," + base64.StdEncoding.EncodeToString(b.Bytes())
}

func imageBudgetDecode(t *testing.T, url string) (image.Image, string) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(url[strings.IndexByte(url, ',')+1:])
	if err != nil {
		t.Fatal(err)
	}
	m, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return m, format
}

func imageBudgetShots(t *testing.T, n int) []string {
	t.Helper()
	shots := make([]string, n)
	for i := range shots {
		m := image.NewNRGBA(image.Rect(0, 0, 2100, 2))
		for x := 0; x < 2100; x++ {
			m.SetNRGBA(x, i%2, color.NRGBA{R: byte(i), G: byte(i >> 8), B: byte(x), A: 255})
		}
		shots[i] = imageBudgetURL(t, m, "png")
	}
	return shots
}

func imageBudgetBody(shots []string) []byte {
	var input []any
	for i, shot := range shots {
		input = append(input, map[string]any{"type": "function_call_output", "call_id": fmt.Sprint(i), "output": []any{
			map[string]any{"type": "input_image", "image_url": shot},
		}})
	}
	b, _ := json.Marshal(map[string]any{"model": "gpt-5.5", "input": input})
	return b
}

func imageBudgetAccount() provider.Provider {
	return provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "fixture@example.invalid"}}
}

func TestWhaleBridgeImageBudgetSizeAndScope(t *testing.T) {
	for _, c := range []struct {
		l            imageLimits
		w, h, ow, oh int
	}{
		{imageLimits{2048, 2500}, 64, 32, 64, 32},
		{imageLimits{2048, 2500}, 2048, 2048, 1600, 1600},
		{imageLimits{2048, 2500}, 3456, 2234, 1979, 1280},
		{imageLimits{6000, 10000}, 6401, 100, 6000, 94},
	} {
		if w, h := c.l.size(c.w, c.h); w != c.ow || h != c.oh {
			t.Errorf("%v %dx%d -> %dx%d, want %dx%d", c.l, c.w, c.h, w, h, c.ow, c.oh)
		}
	}
	imageBudgetCache(t, 1<<20, 8192)
	url := imageBudgetShots(t, 1)[0]
	body := imageBudgetBody([]string{url})
	for _, c := range []struct {
		p  provider.Provider
		to provider.Protocol
	}{
		{provider.Provider{ID: "openai"}, provider.Responses},
		{provider.Provider{Account: &provider.Account{Agent: "claude"}}, provider.Responses},
		{imageBudgetAccount(), provider.Chat},
		{imageBudgetAccount(), provider.Anthropic},
	} {
		if got := codexImagesFit(c.p, c.to, body); !bytes.Equal(got, body) {
			t.Errorf("non-Codex Responses body changed: %s %v", c.p.ID, c.to)
		}
	}
	for _, part := range []map[string]any{
		{"type": "input_image", "image_url": url, "detail": "low"},
		{"type": "input_image", "image_url": "https://fixture.invalid/image.png"},
		{"type": "input_image", "file_id": "file-fixture"},
		{"type": "input_image", "image_url": "data:image/webp;base64,UklGRgAAAABXRUJQ"},
		{"type": "input_image", "image_url": "data:image/png;base64,not-base64"},
		{"type": "input_image", "image_url": "data:image/png,unencoded"},
	} {
		b, _ := json.Marshal(map[string]any{"input": []any{map[string]any{"content": []any{part}}}})
		if got := codexImagesFit(imageBudgetAccount(), provider.Responses, b); !bytes.Equal(got, b) {
			t.Errorf("out-of-scope inline image changed: %+v", part)
		}
	}
}

func TestWhaleBridgeImageBudgetSingleWalkPreservesBytes(t *testing.T) {
	imageBudgetCache(t, 1<<20, 8192)
	url := imageBudgetShots(t, 1)[0]
	body := []byte("{ \"input\" : [{\"output\":[{\"image_url\":\"" + url + "\",\"type\":\"input_image\"}],\"content\":[{\"type\":\"input_image\",\"image_url\":\"" + url + "\"}]}],\"unrelated\":\"keep  spaces\" }")
	out := codexImagesFit(imageBudgetAccount(), provider.Responses, body)
	first := gjson.GetBytes(out, "input.0.output.0.image_url").String()
	second := gjson.GetBytes(out, "input.0.content.0.image_url").String()
	if first == url || first != second {
		t.Fatal("out-of-order output/content image offsets were not replaced consistently")
	}
	if !bytes.Equal(bytes.ReplaceAll(out, []byte(first), []byte("IMG")), bytes.ReplaceAll(body, []byte(url), []byte("IMG"))) {
		t.Fatal("bytes outside the inline image strings changed")
	}
	// Escaped data URLs still identify their original JSON range.
	escaped := bytes.ReplaceAll(imageBudgetBody([]string{url}), []byte("data:"), []byte(`data\u003a`))
	if got := codexImagesFit(imageBudgetAccount(), provider.Responses, escaped); bytes.Equal(got, escaped) || !json.Valid(got) {
		t.Fatal("escaped image URL did not retain a valid replacement range")
	}
}

func TestWhaleBridgeImageBudgetMultiTurnAndDedup(t *testing.T) {
	made := imageBudgetCache(t, 8<<20, 8192)
	shots := imageBudgetShots(t, 66)
	first := codexImagesFit(imageBudgetAccount(), provider.Responses, imageBudgetBody(append(slices.Clone(shots[:65]), shots[0])))
	if n := made(); n != 65 {
		t.Fatalf("first turn made %d images, want 65 unique", n)
	}
	next := codexImagesFit(imageBudgetAccount(), provider.Responses, imageBudgetBody(shots))
	if n := made(); n != 66 {
		t.Fatalf("second turn made %d total, want only one new image", n)
	}
	for i := range 65 {
		path := fmt.Sprintf("input.%d.output.0.image_url", i)
		was, now := gjson.GetBytes(first, path).String(), gjson.GetBytes(next, path).String()
		if was == shots[i] || was != now {
			t.Fatalf("screenshot %d was not kept as the same fitted bytes", i)
		}
	}
	m, _ := imageBudgetDecode(t, gjson.GetBytes(next, "input.65.output.0.image_url").String())
	if m.Bounds().Dx() != 2048 {
		t.Fatalf("new screenshot width = %d", m.Bounds().Dx())
	}
}

func TestWhaleBridgeImageBudgetConcurrentRequests(t *testing.T) {
	imageBudgetCache(t, 1<<20, 8192)
	body := imageBudgetBody(imageBudgetShots(t, 8))
	const requests = 6
	out := make([][]byte, requests)
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = codexImagesFit(imageBudgetAccount(), provider.Responses, body)
		}()
	}
	wg.Wait()
	for i := range out {
		if !json.Valid(out[i]) || bytes.Equal(out[i], body) || !bytes.Equal(out[i], out[0]) {
			t.Fatalf("concurrent request %d changed image bytes or did not complete", i)
		}
	}
	if len(fitted.m) != 8 || fitted.bytes > fittedBudget {
		t.Fatalf("concurrent cache entries/bytes: %d/%d", len(fitted.m), fitted.bytes)
	}
}

func TestWhaleBridgeImageBudgetPastCacheBudget(t *testing.T) {
	shots := imageBudgetShots(t, 8)
	l := imageLimits{2048, 2500}
	sizes := make([]int, len(shots))
	for i, url := range shots {
		sizes[i] = len(codexImageRefit(url[strings.IndexByte(url, ',')+1:], l).data)
	}
	budget := 3 * slices.Max(sizes)
	made := imageBudgetCache(t, budget, 8192)
	body := imageBudgetBody(shots)
	codexImagesFit(imageBudgetAccount(), provider.Responses, body)
	if n := made(); n != int64(len(shots)) {
		t.Fatalf("first turn made %d images", n)
	}
	fitted.Lock()
	kept := len(fitted.m)
	fitted.Unlock()
	codexImagesFit(imageBudgetAccount(), provider.Responses, body)
	if n := made() - int64(len(shots)); n != int64(len(shots)-kept) {
		t.Fatalf("next turn made %d images, want %d cache misses", n, len(shots)-kept)
	}
	if fitted.bytes > budget {
		t.Fatalf("cache holds %d bytes, budget %d", fitted.bytes, budget)
	}
}

func TestWhaleBridgeImageBudgetLRUBytesEntriesAndIdle(t *testing.T) {
	imageBudgetCache(t, 6, 3)
	key := func(s string) [32]byte { return fitKey(s, imageLimits{2048, 2500}) }
	a, b, c := key("a"), key("b"), key("c")
	fitted.put(a, fitEntry{mime: "image/png", data: []byte{1, 2, 3}})
	fitted.put(b, fitEntry{mime: "image/png", data: []byte{4, 5, 6}})
	fitted.get(a) // a is recent: b must go first.
	fitted.put(c, fitEntry{data: []byte{7, 8, 9}})
	if _, ok := fitted.get(b); ok {
		t.Fatal("LRU kept the oldest compressed bytes")
	}
	if got, ok := fitted.get(a); !ok || !bytes.Equal(got.data, []byte{1, 2, 3}) || fitted.bytes != 6 {
		t.Fatalf("LRU content or compressed byte accounting changed: %+v, %d", got, fitted.bytes)
	}
	fitted.put(key("too-big"), fitEntry{data: make([]byte, 7)})
	if _, ok := fitted.get(key("too-big")); ok || fitted.bytes != 6 {
		t.Fatal("entry larger than the byte budget was kept")
	}
	for _, s := range []string{"unchanged-1", "unchanged-2", "unchanged-3", "unchanged-4"} {
		fitted.put(key(s), fitEntry{})
	}
	if len(fitted.m) != 3 || fitted.bytes != 0 {
		t.Fatalf("unchanged entries escaped the count budget: %d entries, %d bytes", len(fitted.m), fitted.bytes)
	}
	for el := fitted.order.Front(); el != nil; el = el.Next() {
		el.Value.(*fitItem).used = time.Now().Add(-fittedIdle - time.Minute)
	}
	fitted.get(key("unchanged-4"))
	fitted.letGo()
	if len(fitted.m) != 1 {
		t.Fatalf("idle eviction removed a recently used entry or kept old ones: %d", len(fitted.m))
	}
	if fitKey("same", imageLimits{2048, 2500}) == fitKey("same", imageLimits{6000, 10000}) {
		t.Fatal("cache key ignored detail limits")
	}
}

func TestWhaleBridgeImageBudgetBatches(t *testing.T) {
	for _, c := range []struct {
		holds  []int
		budget int
		want   [][2]int
	}{
		{nil, 100, nil},
		{[]int{10, 10, 10, 10}, 100, [][2]int{{0, 4}}},
		{[]int{60, 60}, 100, [][2]int{{0, 1}, {1, 2}}},
		{[]int{40, 40, 40, 40, 10}, 100, [][2]int{{0, 2}, {2, 5}}},
		{[]int{10, 250, 10}, 100, [][2]int{{0, 1}, {1, 2}, {2, 3}}},
		{[]int{320 << 20, 320 << 20, 160 << 20}, fitBudget, [][2]int{{0, 1}, {1, 3}}},
	} {
		if got := fitInHand(c.holds, c.budget); !reflect.DeepEqual(got, c.want) {
			t.Errorf("holds=%v budget=%d: %v, want %v", c.holds, c.budget, got, c.want)
		}
	}
}

func TestWhaleBridgeImageBudgetBytesPerPixel(t *testing.T) {
	for _, c := range []struct {
		model  color.Model
		format string
		bpp    int
	}{
		{color.GrayModel, "png", 1},
		{color.Gray16Model, "png", 2},
		{color.RGBAModel, "png", 4},
		{color.NRGBAModel, "png", 4},
		{color.RGBA64Model, "png", 8},
		{color.NRGBA64Model, "png", 8},
		{color.YCbCrModel, "jpeg", 4},
		{color.GrayModel, "jpeg", 1},
		{color.NRGBAModel, "gif", 4},
	} {
		got, ok := imageHolds(image.Config{ColorModel: c.model, Width: 23, Height: 17}, c.format)
		if !ok || got != 23*17*c.bpp {
			t.Errorf("%s bpp=%d: got %d, %v", c.format, c.bpp, got, ok)
		}
	}
	for _, c := range []image.Config{
		{Width: 0, Height: 1}, {Width: 1, Height: -1},
	} {
		if _, ok := imageHolds(c, "png"); ok {
			t.Errorf("invalid image dimensions admitted: %+v", c)
		}
	}
	for _, size := range [][2]int{{8000, 8000}, {1 << 30, 1 << 30}, {int(^uint(0) >> 1), int(^uint(0) >> 1)}} {
		if got, ok := imageHolds(image.Config{ColorModel: color.NRGBA64Model, Width: size[0], Height: size[1]}, "png"); !ok || got != holdsMost {
			t.Errorf("oversized pixels were not capped before multiplication: %v -> %d, %v", size, got, ok)
		}
	}
}

func imageBudgetPNGHeader(w, h uint32, depth, kind byte) []byte {
	var out bytes.Buffer
	out.WriteString("\x89PNG\r\n\x1a\n")
	var header bytes.Buffer
	binary.Write(&header, binary.BigEndian, w)
	binary.Write(&header, binary.BigEndian, h)
	header.Write([]byte{depth, kind, 0, 0, 0})
	for _, chunk := range []struct {
		name string
		data []byte
	}{{"IHDR", header.Bytes()}, {"IDAT", []byte{0x78, 0x9c, 0x03, 0, 0, 0, 0, 1}}, {"IEND", nil}} {
		binary.Write(&out, binary.BigEndian, uint32(len(chunk.data)))
		body := append([]byte(chunk.name), chunk.data...)
		out.Write(body)
		binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE(body))
	}
	return out.Bytes()
}

func TestWhaleBridgeImageBudgetRejectsHeaderBeforeDecode(t *testing.T) {
	made := imageBudgetCache(t, 1<<20, 8192)
	var shots []string
	for i := range 4 {
		raw := imageBudgetPNGHeader(uint32(8000+i), 8000, 16, 6)
		url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
		if len(url) > 1024 || fitHolds(url) != holdsMost {
			t.Fatalf("small oversized header not accounted: %d bytes, holds=%d", len(url), fitHolds(url))
		}
		if e := codexImageRefit(base64.StdEncoding.EncodeToString(raw), imageLimits{2048, 2500}); e.data != nil {
			t.Fatal("oversized decode was attempted")
		}
		shots = append(shots, url)
	}
	body := imageBudgetBody(shots)
	if got := codexImagesFit(imageBudgetAccount(), provider.Responses, body); !bytes.Equal(got, body) || made() != 0 {
		t.Fatalf("oversized headers were refitted: attempts=%d", made())
	}
}

func TestWhaleBridgeImageBudgetPreflightAvoidsFullSourceAllocation(t *testing.T) {
	// One MiB is enough to distinguish a bounded header read from a full
	// source allocation, without making large pixel buffers or images.
	invalid := bytes.Repeat([]byte{'x'}, 1<<20)
	smallURL := imageBudgetURL(t, image.NewRGBA(image.Rect(0, 0, 2, 2)), "png")
	small, err := base64.StdEncoding.DecodeString(smallURL[strings.IndexByte(smallURL, ',')+1:])
	if err != nil {
		t.Fatal(err)
	}
	alreadyFits := append(small, make([]byte, 1<<20)...)
	for _, c := range []struct {
		name string
		raw  []byte
	}{{"unreadable header", invalid}, {"valid image already fits", alreadyFits}} {
		t.Run(c.name, func(t *testing.T) {
			b64 := base64.StdEncoding.EncodeToString(c.raw)
			url := "data:image/png;base64," + b64
			if c.name == "unreadable header" && fitHolds(url) != 0 {
				t.Fatal("unreadable header did not have a zero batch estimate")
			}
			if e := codexImageRefit(b64, imageLimits{2048, 2500}); e.data != nil {
				t.Fatal("passthrough image was changed")
			}
			if body := imageBudgetBody([]string{url}); !bytes.Equal(codexImagesFit(imageBudgetAccount(), provider.Responses, body), body) {
				t.Fatal("request bytes changed for a passthrough image")
			}
			allocs := testing.Benchmark(func(b *testing.B) {
				for b.Loop() {
					codexImageRefit(b64, imageLimits{2048, 2500})
				}
			})
			if bytes := allocs.AllocedBytesPerOp(); bytes > 128<<10 {
				t.Fatalf("header preflight allocated %d bytes for %d source bytes", bytes, len(c.raw))
			}
		})
	}
}

func TestWhaleBridgeImageBudgetEXIFStreamHeader(t *testing.T) {
	var shot bytes.Buffer
	if err := jpeg.Encode(&shot, image.NewRGBA(image.Rect(0, 0, 23, 17)), nil); err != nil {
		t.Fatal(err)
	}
	exif := make([]byte, 0xffff-2)
	copy(exif, "Exif\x00\x00")
	raw := append([]byte{0xff, 0xd8, 0xff, 0xe1, 0xff, 0xff}, exif...)
	raw = append(raw, shot.Bytes()[2:]...)
	url := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(raw)
	// DecodedLen is an upper bound that may include final base64 padding.
	want := base64.StdEncoding.DecodedLen(len(url[strings.IndexByte(url, ',')+1:])) + 23*17*4
	if got := fitHolds(url); got != want {
		t.Fatalf("header behind 64KiB EXIF holds %d, want %d", got, want)
	}
}

func TestWhaleBridgeImageBudgetTransparentGreyStorage(t *testing.T) {
	for _, depth := range []byte{8, 16} {
		var m image.Image
		if depth == 8 {
			m = image.NewGray(image.Rect(0, 0, 23, 17))
		} else {
			m = image.NewGray16(image.Rect(0, 0, 23, 17))
		}
		url := imageBudgetURL(t, m, "png")
		raw, err := base64.StdEncoding.DecodeString(url[strings.IndexByte(url, ',')+1:])
		if err != nil {
			t.Fatal(err)
		}
		// tRNS identifies grey zero as transparent, without changing IHDR.
		var trns bytes.Buffer
		binary.Write(&trns, binary.BigEndian, uint32(2))
		chunk := []byte{'t', 'R', 'N', 'S', 0, 0}
		trns.Write(chunk)
		binary.Write(&trns, binary.BigEndian, crc32.ChecksumIEEE(chunk))
		raw = append(append(slices.Clone(raw[:33]), trns.Bytes()...), raw[33:]...)
		decoded, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		var pixelBytes int
		switch img := decoded.(type) {
		case *image.NRGBA:
			pixelBytes = len(img.Pix)
		case *image.NRGBA64:
			pixelBytes = len(img.Pix)
		default:
			t.Fatalf("transparent grey decoded into %T", decoded)
		}
		cfg, format, err := codexImageConfig(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := imageHolds(cfg, format); !ok || got != pixelBytes {
			t.Fatalf("transparent grey %d-bit holds %d, allocated %d", depth, got, pixelBytes)
		}
		b64 := base64.StdEncoding.EncodeToString(raw)
		if got, want := fitHolds("data:image/png;base64,"+b64), base64.StdEncoding.DecodedLen(len(b64))+pixelBytes; got != want {
			t.Fatalf("transparent grey stream holds %d, want %d", got, want)
		}
		standard, err := png.DecodeConfig(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if plain, _ := imageHolds(standard, "png"); plain*4 != pixelBytes {
			t.Fatalf("fixture did not demonstrate IHDR undercount: %d vs %d", plain, pixelBytes)
		}
		// A declared ancillary chunk of 4GiB, truncated after its header,
		// is skipped as a stream and fails before any pixel allocation.
		truncated := append(slices.Clone(raw[:33]), []byte{0xff, 0xff, 0xff, 0xff, 't', 'E', 'X', 't'}...)
		if _, _, err := codexImageConfig(bytes.NewReader(truncated)); err == nil {
			t.Fatal("truncated oversized ancillary chunk was accepted")
		}
	}
}

func TestWhaleBridgeImageBudgetPalettePNG(t *testing.T) {
	imageBudgetCache(t, 1<<20, 8192)
	for _, transparent := range []bool{false, true} {
		t.Run(fmt.Sprint("alpha=", transparent), func(t *testing.T) {
			palette := color.Palette{color.NRGBA{R: 240, A: 255}, color.NRGBA{B: 240, A: 255}}
			if transparent {
				palette[0] = color.NRGBA{R: 240, A: 128}
				palette[1] = color.NRGBA{}
			}
			m := image.NewPaletted(image.Rect(0, 0, 2100, 4), palette)
			for y := range 4 {
				for x := 1050; x < 2100; x++ {
					m.SetColorIndex(x, y, 1)
				}
			}
			url := imageBudgetURL(t, m, "png")
			b64 := url[strings.IndexByte(url, ',')+1:]
			raw, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				t.Fatal(err)
			}
			cfg, format, err := codexImageConfig(bytes.NewReader(raw))
			if err != nil || format != "png" {
				t.Fatalf("palette header failed: %s %v", format, err)
			}
			if _, ok := cfg.ColorModel.(color.Palette); !ok {
				t.Fatalf("fixture header is not palette: %T", cfg.ColorModel)
			}
			if transparent && !bytes.Contains(raw, []byte("tRNS")) {
				t.Fatal("fixture lacks palette transparency")
			}
			pixels, _ := imageHolds(cfg, format)
			if got, want := fitHolds(url), base64.StdEncoding.DecodedLen(len(b64))+pixels; got != want || got == 0 {
				t.Fatalf("palette header estimate = %d, want %d", got, want)
			}
			l := imageLimits{2048, 2500}
			e := codexImageRefit(b64, l)
			if e.data == nil {
				t.Fatal("palette PNG passed through without fitting")
			}
			jobs := []fitJob{{url: url, l: l}}
			fitAll(jobs)
			if !jobs[0].changed || jobs[0].with != e.url() {
				t.Fatal("palette PNG batch result differs from direct refit")
			}
			got, f := imageBudgetDecode(t, jobs[0].with)
			if f != "png" || got.Bounds().Dx() != 2048 || got.Bounds().Dy() != 4 {
				t.Fatalf("palette output = %s %v", f, got.Bounds())
			}
			_, _, _, leftAlpha := got.At(128, 1).RGBA()
			_, _, _, rightAlpha := got.At(1900, 1).RGBA()
			if transparent && (leftAlpha < 32000 || leftAlpha > 34000 || rightAlpha != 0) {
				t.Fatalf("palette alpha changed: left=%d right=%d", leftAlpha, rightAlpha)
			}
			if !transparent && (leftAlpha != 65535 || rightAlpha != 65535) {
				t.Fatalf("opaque palette gained alpha: left=%d right=%d", leftAlpha, rightAlpha)
			}
		})
	}
}

func TestWhaleBridgeImageBudgetFormatsAndAlpha(t *testing.T) {
	imageBudgetCache(t, 1<<20, 8192)
	m := image.NewNRGBA64(image.Rect(0, 0, 10, 4))
	for y := range 4 {
		for x := range 10 {
			m.SetNRGBA64(x, y, color.NRGBA64{R: 65535, B: 32768, A: 32768})
		}
	}
	for _, format := range []string{"png", "jpeg", "gif"} {
		url := imageBudgetURL(t, m, format)
		jobs := []fitJob{{url: url, l: imageLimits{side: 6, patches: 20}}}
		fitAll(jobs)
		if !jobs[0].changed {
			t.Fatalf("%s image was not made smaller", format)
		}
		got, decodedFormat := imageBudgetDecode(t, jobs[0].with)
		wantFormat := format
		if format == "gif" {
			wantFormat = "png"
		}
		if decodedFormat != wantFormat || got.Bounds().Dx() != 6 || got.Bounds().Dy() != 2 {
			t.Errorf("%s -> %s %v", format, decodedFormat, got.Bounds())
		}
		if format == "png" {
			r, g, b, a := got.At(3, 1).RGBA()
			if a < 32000 || a > 34000 || r > a || g > a || b > a || r < 31000 {
				t.Fatalf("alpha or premultiplied channels changed: %d,%d,%d,%d", r, g, b, a)
			}
		}
	}
	// GIFs that already fit are still normalized to PNG; PNG/JPEG fit unchanged.
	for _, format := range []string{"png", "jpeg", "gif"} {
		url := imageBudgetURL(t, m, format)
		jobs := []fitJob{{url: url, l: imageLimits{2048, 2500}}}
		fitAll(jobs)
		if jobs[0].changed != (format == "gif") {
			t.Errorf("fitting %s changed=%v", format, jobs[0].changed)
		}
	}
}
