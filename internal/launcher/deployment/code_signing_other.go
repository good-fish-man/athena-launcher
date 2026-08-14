//go:build !darwin && !windows

package deployment

func verifyInstalledCodeSignature(_, _ string) error { return nil }
