//go:build !darwin && !windows

package secretstore

func platformVault(string) (vault, error) { return nil, ErrUnavailable }
