//go:build darwin

package deployment

import (
	"fmt"
	"os/exec"
	"strings"
)

func verifyInstalledCodeSignature(path, status string) error {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "NOTARIZED", "DEVELOPER_ID", "SIGNED":
		output, err := exec.Command("/usr/bin/codesign", "--verify", "--deep", "--strict", path).CombinedOutput()
		if err != nil {
			return fmt.Errorf("verify macOS code signature for %s: %w: %s", path, err, strings.TrimSpace(string(output)))
		}
	}
	return nil
}
