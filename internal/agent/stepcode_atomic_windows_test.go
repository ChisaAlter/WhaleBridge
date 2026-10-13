package agent

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/edit"
)

// Windows permits snapshots through this handle, but replacing config.toml
// requires DELETE sharing. A real second rename failure must roll the first
// file and the restore-point stash back, without running StepCode.
func TestStepCodeSecondRenameFailureRollsBackWindows(t *testing.T) {
	home, _ := museHome(t)
	dir := filepath.Join(home, ".stepcode")
	config := filepath.Join(dir, "config.toml")
	models := filepath.Join(dir, "models.json")
	before := map[string][]byte{
		config:      []byte("# native selection\ndefaultProvider = \"native\"\ndefaultModel = \"test\"\n"),
		models:      []byte(`{"providers":{"native":{"name":"Native"}}}`),
		stashPath(): []byte("{}\n"),
	}
	for path, data := range before {
		stepcodeWriteTestFile(t, path, string(data))
	}
	name, err := syscall.UTF16PtrFromString(config)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.CloseHandle(handle) })
	firstChanged, secondReached := false, false
	err = edit.Filtered(func(path string, data []byte) []byte {
		if path == config {
			secondReached = true
			got, readErr := os.ReadFile(models)
			if readErr != nil {
				t.Fatal(readErr)
			}
			firstChanged = !bytes.Equal(got, before[models]) && bytes.Contains(got, []byte(`"magpie"`))
		}
		return data
	}, func() error { return stepcode(home).Apply("model", "magpie/deepseek/pro") })
	var rename *os.LinkError
	if !errors.As(err, &rename) || rename.Op != "rename" || rename.New != config || !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		t.Fatalf("second file did not hit Windows' denied replacement of the held config: %v", err)
	}
	if !firstChanged || !secondReached {
		t.Fatal("failure did not happen after the first product file was written")
	}
	for path, want := range before {
		got, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(got, want) {
			t.Fatalf("second-write failure left %s changed: %v", path, readErr)
		}
	}
	state, err := loadStepcodeState(here(home))
	if err != nil || state != nil {
		t.Fatalf("failed connection left a restore-point record: %v", err)
	}
}
