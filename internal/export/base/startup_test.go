package base_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orglibs/driver-box/v2/driverbox"
	"github.com/orglibs/driver-box/v2/pkg/config"
)

func TestFailedCollectionStartupReturnsError(t *testing.T) {
	if os.Getenv("DRIVERBOX_STARTUP_TEST_CHILD") != "true" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestFailedCollectionStartupReturnsError$", "-test.timeout=20s")
		cmd.Dir = t.TempDir()
		cmd.Env = append(os.Environ(), "DRIVERBOX_STARTUP_TEST_CHILD=true")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("failed-startup cleanup: %v\n%s", err, output)
		}
		return
	}
	root := t.TempDir()
	target := filepath.Join(root, "driver", "extra", "config.json")
	if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("{bad json"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ENV_RESOURCE_PATH, root)
	t.Setenv(config.ENV_LOG_PATH, filepath.Join(root, "driverbox.log"))
	// Invalid ports deterministically fail without requiring network access.
	t.Setenv(config.ENV_UDP_DISCOVER_LISTEN, "-1")
	t.Setenv(config.ENV_HTTP_LISTEN, "-1")
	if err := driverbox.Start(); err == nil || !strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("original configuration error was not returned: %v", err)
	}
	if err := driverbox.Stop(); err != nil {
		t.Fatal(err)
	}
}
