// Package launcher owns Athena's installation, desktop and service lifecycle.
package launcher

import "athena-launcher/internal/launcher/deployment"

// Run parses and executes one Athena Launcher command.
func Run(args []string) error {
	return deployment.Run(args)
}
