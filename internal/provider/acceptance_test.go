package provider

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Live tests are deliberately separate from the default stateful fake suite.
func TestLiveAcceptance(t *testing.T) {
	if os.Getenv("HF_ACC") != "1" {
		t.Skip("set HF_ACC=1 plus dedicated HF_TEST_NAMESPACE and HF_TOKEN to opt in")
	}
	for _, key := range []string{"HF_TOKEN", "HF_TEST_NAMESPACE", "HF_TEST_GITHUB_REPOSITORY", "HF_TEST_BRANCH"} {
		if os.Getenv(key) == "" {
			t.Fatalf("%s required for explicit live acceptance", key)
		}
	}
	cmd := exec.Command("python3", filepath.Join("..", "..", "tests", "acceptance", "live.py"), "--mode", "roundtrip", "--confirm-disposable", "--namespace", os.Getenv("HF_TEST_NAMESPACE"), "--github-repository", os.Getenv("HF_TEST_GITHUB_REPOSITORY"), "--branch", os.Getenv("HF_TEST_BRANCH"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("live acceptance: %v\n%s", err, output)
	}
}
