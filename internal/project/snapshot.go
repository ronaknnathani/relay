package project

import (
	"fmt"
	"strings"

	"github.com/ronaknnathani/relay/internal/gitx"
)

// RepositorySnapshotForManifest computes the repository identity used by
// routing and evidence freshness for one project manifest.
func RepositorySnapshotForManifest(manifest Manifest, projectDir string) (RepositorySnapshot, error) {
	if manifest.Worktree == nil || strings.TrimSpace(*manifest.Worktree) == "" {
		return RepositorySnapshot{}, fmt.Errorf("project %q has no worktree", manifest.Slug)
	}
	inputRevision, err := ProjectInputRevision(projectDir)
	if err != nil {
		return RepositorySnapshot{}, fmt.Errorf("snapshot project %q inputs: %w", manifest.Slug, err)
	}
	base := gitx.SnapshotBaseRef(*manifest.Worktree, manifest.BaseBranch, manifest.StartSHA)
	if manifest.RemoteBaseSHA != "" {
		if manifest.BaseBranch == "" || manifest.BaseBranch == "HEAD" ||
			strings.HasPrefix(manifest.BaseBranch, "refs/") ||
			strings.HasPrefix(manifest.BaseBranch, "origin/") ||
			!gitx.ValidBranchName(*manifest.Worktree, manifest.BaseBranch) {
			return RepositorySnapshot{}, fmt.Errorf(
				"project %q has invalid remote base branch %q", manifest.Slug, manifest.BaseBranch,
			)
		}
		base = manifest.RemoteBaseSHA
	}
	snapshot, err := gitx.Snapshot(*manifest.Worktree, base)
	if err != nil {
		return RepositorySnapshot{}, fmt.Errorf("snapshot project %q: %w", manifest.Slug, err)
	}
	return RepositorySnapshot{
		BaseRef: manifest.BaseBranch,
		BaseSHA: snapshot.BaseSHA, BaseTipSHA: snapshot.BaseTipSHA,
		HeadSHA: snapshot.HeadSHA, Fingerprint: snapshot.Fingerprint,
		InputRevision: inputRevision,
		FileCount:     snapshot.FileCount, ChangedLines: snapshot.ChangedLines,
	}, nil
}
