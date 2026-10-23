package davsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/backup"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

func filled(n int, seed uint32) []byte {
	b := make([]byte, n)
	x := seed*2654435761 + 1
	for i := range b {
		x = x*1664525 + 1013904223
		b[i] = byte(x >> 24)
	}
	return b
}

const backupAt = "main|team/magpie/" + file

func partAt(i int) string { return backupAt + ".parts/" + strconv.Itoa(i) }
func (f *githubFixture) parts() int {
	n := 0
	for k := range f.objects {
		if strings.HasPrefix(k, backupAt+".parts/") {
			n++
		}
	}
	return n
}
func githubTestRemote(t *testing.T) (*githubFixture, *github) {
	t.Helper()
	f := newGithubFixture(t)
	g, err := newGitHub(githubConfig())
	if err != nil {
		t.Fatal(err)
	}
	return f, g
}
func githubBlobPosts(f *githubFixture) int {
	n := 0
	for _, r := range f.requests {
		if r == "POST /repos/alice/private/git/blobs" {
			n++
		}
	}
	return n
}

func TestGitHubSplitBackup(t *testing.T) {
	f, g := githubTestRemote(t)
	ctx := t.Context()
	v, err := g.put(ctx, []byte("earlier sealed backup"), "")
	if err != nil {
		t.Fatal(err)
	}
	big := filled(50_800_000, 1)
	before := f.writes
	v, err = g.put(ctx, big, v.ETag)
	if err != nil {
		t.Fatal(err)
	}
	list := bytes.Clone(f.objects[backupAt])
	if f.writes != before+1 || v.ETag != githubSHA(list) || f.parts() != 4 || f.biggestBlob > githubPart {
		t.Fatalf("split was not one bounded atomic commit: commits=%d parts=%d biggest=%d", f.writes-before, f.parts(), f.biggestBlob)
	}
	if b, got, err := g.get(ctx, version{}); err != nil || got != v || !bytes.Equal(b, big) {
		t.Fatalf("split roundtrip: size=%d err=%v", len(b), err)
	}
	beforeReq := len(f.requests)
	if _, _, err := g.get(ctx, v); !errors.Is(err, errNotModified) {
		t.Fatal(err)
	}
	for _, r := range f.requests[beforeReq:] {
		if strings.Contains(r, "/git/blobs/") {
			t.Fatal("unchanged backup downloaded a part again")
		}
	}
	if _, err := g.put(ctx, []byte("stale"), "old-sha"); !errors.Is(err, errChanged) || !bytes.Equal(f.objects[backupAt], list) {
		t.Fatal("stale SHA replaced split backup")
	}
	mid := filled(20<<20, 2)
	before = f.writes
	v, err = g.put(ctx, mid, v.ETag)
	if err != nil || f.writes != before+1 || f.parts() != 2 {
		t.Fatalf("shrinking parts: %v", err)
	}
	if b, _, err := g.get(ctx, version{}); err != nil || !bytes.Equal(b, mid) {
		t.Fatalf("smaller split roundtrip: %v", err)
	}
	small := []byte("small sealed backup")
	v, err = g.put(ctx, small, v.ETag)
	if err != nil || f.parts() != 0 || !bytes.Equal(f.objects[backupAt], small) {
		t.Fatalf("old parts not deleted: %v", err)
	}
	if b, got, err := g.get(ctx, version{}); err != nil || got != v || !bytes.Equal(b, small) {
		t.Fatalf("single file roundtrip after cleanup: %v", err)
	}
}

// A single blob above Contents' smaller write limit still takes one Git
// Data commit, and a large legacy single file is read by immutable blob SHA.
func TestGitHubLargeBackupSingleBlob(t *testing.T) {
	f, g := githubTestRemote(t)
	f.contentsLimit = 1024
	v, err := g.put(t.Context(), []byte("first"), "")
	if err != nil {
		t.Fatal(err)
	}
	f.requests = nil
	before := f.writes
	large := filled(15<<20, 11)
	v, err = g.put(t.Context(), large, v.ETag)
	if err != nil || v.ETag != githubSHA(large) || f.writes != before+1 || githubBlobPosts(f) != 1 || f.parts() != 0 {
		t.Fatalf("large single blob bypass did not commit once: %v", err)
	}
	if b, got, err := g.get(t.Context(), version{}); err != nil || got != v || !bytes.Equal(b, large) {
		t.Fatalf("large single blob roundtrip: %v", err)
	}
	if !slices.Contains(f.requests, "GET /repos/alice/private/git/blobs/"+v.ETag) {
		t.Fatal("large single file was not downloaded by its pinned SHA")
	}
	for _, r := range f.requests {
		if strings.HasPrefix(r, "PUT ") {
			t.Fatal("existing repository wrote through the Contents API")
		}
	}
}

func TestGitHubOneFileBackupStillReads(t *testing.T) {
	f, g := githubTestRemote(t)
	for _, b := range [][]byte{[]byte("legacy small"), filled(30<<20, 3)} {
		f.objects[backupAt] = b
		f.advance("main")
		got, v, err := g.get(t.Context(), version{})
		if err != nil || !bytes.Equal(got, b) || v.ETag != githubSHA(b) {
			t.Fatalf("legacy single file size=%d: %v", len(b), err)
		}
	}
	old := githubSHA(f.objects[backupAt])
	big := filled(githubPart+1, 4)
	if _, err := g.put(t.Context(), big, old); err != nil {
		t.Fatal(err)
	}
	if b, _, err := g.get(t.Context(), version{}); err != nil || !bytes.Equal(b, big) {
		t.Fatal("parts cannot replace an old large single file")
	}
}

func TestGitHubPartsOlderMagpie(t *testing.T) {
	f, g := githubTestRemote(t)
	if _, err := g.put(t.Context(), filled(githubPart+1, 5), ""); err != nil {
		t.Fatal(err)
	}
	_, err := backup.Open(f.objects[backupAt], githubConfig().Passphrase)
	if err == nil || errors.Is(err, backup.ErrCorrupt) || errors.Is(err, backup.ErrNotBackup) || !strings.Contains(err.Error(), "newer magpie") {
		t.Fatalf("old reader must stop without recovery: %v", err)
	}
	if _, ok := described(githubConfig(), f.objects[backupAt], err).(*notBackup); ok {
		t.Fatal("manifest offered for replacing")
	}
}

func TestGitHubPartsDamaged(t *testing.T) {
	setup := func(t *testing.T) (*githubFixture, *github, githubParts) {
		f, g := githubTestRemote(t)
		if _, err := g.put(t.Context(), filled(githubPart+10, 6), ""); err != nil {
			t.Fatal(err)
		}
		var m githubParts
		if err := json.Unmarshal(f.objects[backupAt], &m); err != nil || len(m.Parts) != 2 {
			t.Fatal("invalid fixture manifest")
		}
		return f, g, m
	}
	for _, mode := range []string{"changed", "missing", "short", "oversized", "size sum", "bad sha", "too many", "newer", "type error"} {
		t.Run(mode, func(t *testing.T) {
			f, g, m := setup(t)
			switch mode {
			case "changed":
				f.blobs[m.Parts[1].SHA] = filled(int(m.Parts[1].Size), 7)
			case "missing":
				delete(f.blobs, m.Parts[1].SHA)
				delete(f.objects, partAt(2))
			case "short":
				f.blobs[m.Parts[1].SHA] = []byte("short")
			case "oversized":
				f.blobs[m.Parts[1].SHA] = filled(int(m.Parts[1].Size)+1, 8)
			case "size sum":
				m.Size++
			case "bad sha":
				m.Parts[0].SHA = strings.Repeat("z", 40)
			case "too many":
				m.Parts = append(m.Parts, m.Parts...)
				m.Parts = append(m.Parts, m.Parts[0])
			case "newer":
				m.Split = "github-parts-2"
			}
			if mode == "type error" {
				f.objects[backupAt] = []byte(`{"format":"magpie-backup","version":2,"split":"github-parts","size":"oops"}`)
			} else if mode == "size sum" || mode == "bad sha" || mode == "too many" || mode == "newer" {
				f.objects[backupAt], _ = json.Marshal(m)
			}
			if b, _, err := g.get(t.Context(), version{}); err == nil || b != nil {
				t.Fatalf("damaged %s assembled a backup: size=%d err=%v", mode, len(b), err)
			}
		})
	}
}

func TestGitHubBranchMovedMeanwhile(t *testing.T) {
	for _, mode := range []string{"unrelated", "backup changed", "branch deleted", "continual commits"} {
		t.Run(mode, func(t *testing.T) {
			f, g := githubTestRemote(t)
			v, err := g.put(t.Context(), []byte("first"), "")
			if err != nil {
				t.Fatal(err)
			}
			f.requests = nil
			conflicts := 0
			f.beforeRef = func() {
				conflicts++
				if mode != "continual commits" {
					f.beforeRef = nil
				}
				switch mode {
				case "backup changed":
					f.objects[backupAt] = []byte("competitor backup")
					f.advance("main")
				case "branch deleted":
					delete(f.heads, "main")
				default:
					f.objects["main|other.txt"] = []byte("other data")
					f.advance("main")
				}
			}
			_, err = g.put(t.Context(), []byte("second"), v.ETag)
			switch mode {
			case "unrelated":
				if err != nil || string(f.objects[backupAt]) != "second" || string(f.objects["main|other.txt"]) != "other data" || githubBlobPosts(f) != 1 {
					t.Fatalf("retry lost data or reuploaded blob: %v", err)
				}
			case "backup changed":
				if !errors.Is(err, errChanged) || string(f.objects[backupAt]) != "competitor backup" {
					t.Fatalf("competitor overwritten: %v", err)
				}
			case "branch deleted":
				if err == nil || errors.Is(err, errChanged) || !strings.Contains(err.Error(), "Reference does not exist") {
					t.Fatalf("deleted branch was recreated: %v", err)
				}
			case "continual commits":
				if !errors.Is(err, errChanged) || conflicts != 4 || githubBlobPosts(f) != 1 || string(f.objects[backupAt]) != "first" {
					t.Fatalf("retry budget: conflicts=%d err=%v", conflicts, err)
				}
			}
		})
	}
}

func TestGitHubPartsBranchMovedMeanwhile(t *testing.T) {
	big := filled(githubPart+1, 9)
	for _, changed := range []bool{false, true} {
		t.Run(strconv.FormatBool(changed), func(t *testing.T) {
			f, g := githubTestRemote(t)
			v, err := g.put(t.Context(), []byte("first"), "")
			if err != nil {
				t.Fatal(err)
			}
			f.beforeRef = func() {
				f.beforeRef = nil
				if changed {
					f.objects[backupAt] = []byte("competitor")
				} else {
					f.objects["main|other.txt"] = []byte("preserved")
				}
				f.advance("main")
			}
			f.requests = nil
			_, err = g.put(t.Context(), big, v.ETag)
			if changed {
				if !errors.Is(err, errChanged) || f.parts() != 0 || string(f.objects[backupAt]) != "competitor" {
					t.Fatalf("partial split became visible: %v", err)
				}
			} else {
				if err != nil || githubBlobPosts(f) != 3 || f.parts() != 2 || string(f.objects["main|other.txt"]) != "preserved" {
					t.Fatalf("split retry did not reuse blobs: %v", err)
				}
				if b, _, err := g.get(t.Context(), version{}); err != nil || !bytes.Equal(b, big) {
					t.Fatal("split retry assembled wrong backup")
				}
			}
		})
	}
}

func TestGitHubEmptyRepositoryLargeAndParts(t *testing.T) {
	for _, n := range []int{4096, githubPart + 1} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			f, g := githubTestRemote(t)
			f.empty, f.contentsLimit = true, 1024
			big := filled(n, 10)
			v, err := g.put(t.Context(), big, "")
			if err != nil || f.writes != 2 || len(f.objects["main|team/magpie/README.md"]) == 0 {
				t.Fatalf("initialize then commit: %v", err)
			}
			if n > githubPart {
				puts := slices.DeleteFunc(slices.Clone(f.requests), func(r string) bool { return !strings.HasPrefix(r, "PUT ") })
				if !slices.Equal(puts, []string{"PUT /repos/alice/private/contents/team/magpie/README.md"}) {
					t.Fatal("split attempted large Contents PUT")
				}
			}
			if b, got, err := g.get(t.Context(), version{}); err != nil || got != v || !bytes.Equal(b, big) {
				t.Fatal("initialized backup read differs")
			}
		})
	}
}

func TestGitHubBackupSizeBound(t *testing.T) {
	f, g := githubTestRemote(t)
	if _, err := g.put(t.Context(), make([]byte, githubMaxFile+1), ""); err == nil || f.writes != 0 || len(f.requests) != 0 {
		t.Fatal("oversized backup made a network request")
	}
	for _, m := range []githubParts{
		{Format: "magpie-backup", Version: 2, Split: githubSplit, Size: githubMaxFile + 1, Parts: []githubPartRef{{strings.Repeat("a", 40), githubPart}, {strings.Repeat("b", 40), githubPart}}},
		{Format: "magpie-backup", Version: 2, Split: githubSplit, Size: 2, Parts: []githubPartRef{{strings.Repeat("a", 40), 1}, {strings.Repeat("b", 40), 1}}},
	} {
		b, _ := json.Marshal(m)
		if _, err := parseParts(b); err == nil {
			t.Fatal("out-of-bound manifest accepted")
		}
	}
}

func TestWhaleBridgeGitHubSyncLargeSettings(t *testing.T) {
	f := newGithubFixture(t)
	a, b := newComputer(t), newComputer(t)
	a.use(t)
	text := string(bytes.Repeat([]byte("large-synthetic-model-label"), (githubPart/26)+20))
	s := settings.Load()
	s.ModelNames = map[string]string{"synthetic/model": text}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	cfg := githubConfig()
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.parts() < 2 {
		t.Fatal("large component settings did not use parts")
	}
	before, posts := f.writes, githubBlobPosts(f)
	if err := Now(context.Background()); err != nil || f.writes != before || githubBlobPosts(f) != posts {
		t.Fatal("unchanged sync uploaded again")
	}
	b.use(t)
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load().ModelNames["synthetic/model"]; got != text {
		t.Fatal("second computer did not restore large component settings")
	}
	g, err := newGitHub(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _, err := g.get(t.Context(), version{})
	bundle, openErr := backup.Open(sealed, cfg.Passphrase)
	if err != nil || openErr != nil || bundle.Library != nil {
		t.Fatalf("large sync changed component backup policy: %v %v", err, openErr)
	}
}
