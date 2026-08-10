package deployment

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func Run(args []string) error {
	command := defaultCommand()
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	home, err := defaultHome()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	homeFlag := flags.String("home", home, "Athena data and installation directory")
	manifestFlag := flags.String("manifest", "", "release manifest file or HTTPS URL")
	frontendFlag := flags.String("frontend-dir", os.Getenv("ATHENA_FRONTEND_DIR"), "local Athena UI dist directory (development only)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	absHome, err := filepath.Abs(*homeFlag)
	if err != nil {
		return err
	}
	manifestSource := strings.TrimSpace(*manifestFlag)
	if manifestSource == "" {
		manifestSource = defaultManifest(absHome)
	}
	frontendDir, err := normalizeFrontendOverride(*frontendFlag)
	if err != nil {
		return err
	}
	opts := options{command: command, home: absHome, manifestSource: manifestSource, frontendDir: frontendDir}

	switch command {
	case "launch":
		return launchDesktop(opts)
	case "start":
		return startDetached(opts)
	case "run":
		return runForeground(opts)
	case "install", "update":
		_, _, _, err := prepare(context.Background(), opts)
		return err
	case "validate":
		manifest, err := loadManifest(context.Background(), opts.manifestSource)
		if err != nil {
			return err
		}
		fmt.Printf("Manifest %s is valid for %s (%d services).\n", manifest.Version, platformKey(), len(manifest.Services))
		return nil
	case "stop":
		return stopManaged(opts.home)
	case "status":
		printStatus(opts.home)
		return nil
	case "version":
		fmt.Printf("athena-launcher %s (%s)\n", LauncherVersion, platformKey())
		return nil
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func printUsage() {
	fmt.Println(`Athena one-file installer and service manager

Usage:
  athena-launcher launch  [--home PATH] [--manifest URL_OR_FILE]
  athena-launcher start   [--home PATH] [--manifest URL_OR_FILE]
  athena-launcher run     [--home PATH] [--manifest URL_OR_FILE]
  athena-launcher install [--home PATH] [--manifest URL_OR_FILE]
  athena-launcher update  [--home PATH] [--manifest URL_OR_FILE]
  athena-launcher validate [--manifest URL_OR_FILE]
  athena-launcher stop    [--home PATH]
  athena-launcher status  [--home PATH]
  athena-launcher version

Environment:
  ATHENA_HOME          installation and data directory (default ~/.athena)
  ATHENA_MANIFEST_URL release manifest HTTPS URL or local file`)
}
