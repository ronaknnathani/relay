package programview

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/ronaknnathani/relay/internal/program"
)

// ArtifactKind identifies an allowlisted artifact namespace.
type ArtifactKind string

const (
	ArtifactKindTask     ArtifactKind = "task"
	ArtifactKindContract ArtifactKind = "contract"
)

var (
	// ErrInvalidArtifactSelector reports malformed or disallowed selectors.
	ErrInvalidArtifactSelector = errors.New("invalid artifact selector")
	// ErrArtifactSelectorNotFound reports a valid selector with no matching task or contract.
	ErrArtifactSelectorNotFound = errors.New("artifact selector not found")
)

// ArtifactSelector identifies one task file or one manifest-recorded contract.
type ArtifactSelector struct {
	Kind ArtifactKind
	Item string
	Name string
	Ref  string
}

// LoadArtifact resolves and reads exactly one allowlisted artifact.
func LoadArtifact(slug string, selector ArtifactSelector, limit int64) (ArtifactResponse, error) {
	if limit <= 0 {
		limit = defaultArtifactLimit
	}
	path, err := program.Find(slug)
	if err != nil {
		return ArtifactResponse{}, err
	}
	p, err := program.Load(path)
	if err != nil {
		return ArtifactResponse{}, err
	}

	var artifact ArtifactDTO
	switch selector.Kind {
	case ArtifactKindTask:
		artifact, err = loadTaskArtifact(p, selector, limit)
	case ArtifactKindContract:
		artifact, err = loadContractArtifact(filepath.Dir(path), p, selector, limit)
	default:
		return ArtifactResponse{}, fmt.Errorf("%w: kind %q", ErrInvalidArtifactSelector, selector.Kind)
	}
	if err != nil {
		if errors.Is(err, ErrInvalidArtifactSelector) || errors.Is(err, ErrArtifactSelectorNotFound) {
			return ArtifactResponse{}, err
		}
		return ArtifactResponse{
			State: ArtifactStateError, Artifact: artifact, Error: err.Error(),
		}, err
	}
	return artifactResponse(artifact), nil
}

func loadTaskArtifact(p program.Program, selector ArtifactSelector, limit int64) (ArtifactDTO, error) {
	if selector.Item == "" || selector.Ref != "" || !allowedTaskArtifact(selector.Name) {
		return ArtifactDTO{}, fmt.Errorf(
			"%w: task item %q name %q", ErrInvalidArtifactSelector, selector.Item, selector.Name,
		)
	}
	for _, item := range p.Items {
		if item.ID != selector.Item {
			continue
		}
		if item.ProjectSlug == "" {
			return ArtifactDTO{Name: selector.Name, Path: selector.Name}, nil
		}
		childDir, _, _, err := loadChild(item.ProjectSlug)
		if err != nil {
			return ArtifactDTO{Name: selector.Name, Path: selector.Name},
				fmt.Errorf("resolve task %s child %q: %w", item.ID, item.ProjectSlug, err)
		}
		return readArtifact(childDir, selector.Name, limit, true)
	}
	return ArtifactDTO{}, fmt.Errorf("%w: task %q", ErrArtifactSelectorNotFound, selector.Item)
}

func loadContractArtifact(programDir string, p program.Program, selector ArtifactSelector, limit int64) (ArtifactDTO, error) {
	if selector.Ref == "" || selector.Item != "" || selector.Name != "" {
		return ArtifactDTO{}, fmt.Errorf("%w: contract ref %q", ErrInvalidArtifactSelector, selector.Ref)
	}
	for _, contract := range p.Contracts {
		if contract.Ref == selector.Ref {
			return readArtifact(programDir, contract.Path, limit, true)
		}
	}
	return ArtifactDTO{}, fmt.Errorf("%w: contract %q", ErrArtifactSelectorNotFound, selector.Ref)
}

func allowedTaskArtifact(name string) bool {
	for _, allowed := range childArtifactNames {
		if name == allowed {
			return true
		}
	}
	return false
}

func artifactResponse(artifact ArtifactDTO) ArtifactResponse {
	state := ArtifactStateMissing
	if artifact.Present {
		switch {
		case artifact.Truncated:
			state = ArtifactStateTruncated
		case artifact.Text != nil && *artifact.Text == "":
			state = ArtifactStateEmpty
		default:
			state = ArtifactStateLoaded
		}
	}
	return ArtifactResponse{State: state, Artifact: artifact}
}
