//go:build windows

package deployment

import (
	"fmt"
	"os/exec"
	"strings"
)

func verifyInstalledCodeSignature(path, status string) error {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "AUTHENTICODE", "SIGNED":
		command := "$signature = Get-AuthenticodeSignature -LiteralPath $args[0]; if ($signature.Status -ne 'Valid') { Write-Error $signature.Status; exit 1 }"
		output, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", command, path).CombinedOutput()
		if err != nil {
			return fmt.Errorf("verify Windows Authenticode signature for %s: %w: %s", path, err, strings.TrimSpace(string(output)))
		}
	}
	return nil
}
