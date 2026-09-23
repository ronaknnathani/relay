package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ronaknnathani/relay/internal/gitx"
)

func resolveProgramItemRepo(primaryRepo, cwd, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return primaryRepo, nil
	}

	candidate := requested
	switch {
	case filepath.IsAbs(requested):
	case strings.ContainsRune(requested, filepath.Separator):
		candidate = filepath.Join(cwd, requested)
	default:
		candidate = filepath.Join(filepath.Dir(primaryRepo), requested)
	}
	candidate = filepath.Clean(candidate)
	repo, err := gitx.CanonicalRepositoryRoot(candidate)
	if err != nil {
		return "", fmt.Errorf(
			"resolve work item repository %q at %s: repository must be checked out at %s before retrying: %w",
			requested, candidate, candidate, err,
		)
	}
	return repo, nil
}
