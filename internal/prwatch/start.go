package prwatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
)

// RequireLiveOwner confirms exactly one live Herdr agent carries the owner's
// Relay identity.
//
// A watcher whose owner does not exist observes a pull request forever and
// hands its work to nobody, and a watcher whose owner is ambiguous cannot
// decide which of two sessions to wake. Neither is discovered until an owner
// wake is finally attempted, hours later, in a pane nobody is reading — so
// starting one is refused up front, before any tab is created.
func RequireLiveOwner(agents []herdr.Agent, mode Mode, projectSlug, ownerSlug string) (herdr.Agent, error) {
	owner, err := herdr.FindLiveProjectOwner(agents, ownerSlug)
	if err == nil {
		return owner, nil
	}
	var duplicate *herdr.DuplicateProjectOwnerError
	if errors.As(err, &duplicate) {
		return herdr.Agent{}, fmt.Errorf(
			"cannot start a pr watcher for project %q: %w", projectSlug, err,
		)
	}
	if mode == ModeStack {
		return herdr.Agent{}, fmt.Errorf(
			"cannot start a pr watcher for project %q: %w; "+
				"a stack watcher wakes the orchestrator session titled \"relay:%s\", "+
				"so start it from the orchestrator's own workspace with that session live",
			projectSlug, err, ownerSlug,
		)
	}
	return herdr.Agent{}, fmt.Errorf(
		"cannot start a pr watcher for project %q: %w; "+
			"a %s watcher wakes the live session titled \"relay:%s\", and a watcher with no owner "+
			"would observe the pull request forever and hand its work to nobody. "+
			"Run this from that project's own session, or watch the pull request by hand with "+
			"`relay pr watch tick %s --json`",
		projectSlug, err, mode, ownerSlug, projectSlug,
	)
}

// RequireManagedProject confirms a managed watcher is watching a real managed
// project.
//
// Managed mode makes a specific claim: this project is one program's work item,
// and its owner is that item's worker — never the program's tech lead. Nothing
// about the mode flag proves it. A watcher started as managed on a project that
// carries no assignment, names a program that does not exist, or is claimed by
// a work item belonging to some other project would route wakes on a fiction,
// so the claim is checked before any tab is created.
func RequireManagedProject(slug string) error {
	manifestPath, err := project.Find(slug)
	if err != nil {
		return fmt.Errorf("pr watch managed project: %w", err)
	}
	manifest, err := project.Load(manifestPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(manifest.Program) == "" || strings.TrimSpace(manifest.ProgramItem) == "" {
		return fmt.Errorf(
			"project %q is not a managed project: its manifest records program %q and work item %q; "+
				"use `relay pr watch start %s` without --mode managed, or dispatch it from a program first",
			slug, manifest.Program, manifest.ProgramItem, slug,
		)
	}
	assignment := filepath.Join(filepath.Dir(manifestPath), "assignment.md")
	info, err := os.Stat(assignment)
	if err != nil {
		return fmt.Errorf(
			"managed project %q has no readable assignment: %w; "+
				"a managed watcher wakes the worker that owns this assignment, so it must exist",
			slug, err,
		)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf(
			"managed project %q has an unusable assignment at %s: want a non-empty regular file",
			slug, assignment,
		)
	}
	if _, err := os.ReadFile(assignment); err != nil {
		return fmt.Errorf("read managed assignment for project %q: %w", slug, err)
	}

	programPath, err := program.Find(manifest.Program)
	if err != nil {
		return fmt.Errorf(
			"managed project %q names program %q, which was not found: %w",
			slug, manifest.Program, err,
		)
	}
	managing, err := program.Load(programPath)
	if err != nil {
		return err
	}
	item, found := managing.Item(manifest.ProgramItem)
	if !found {
		return fmt.Errorf(
			"managed project %q names work item %q, which program %q does not have",
			slug, manifest.ProgramItem, manifest.Program,
		)
	}
	if item.ProjectSlug != slug {
		return fmt.Errorf(
			"managed project %q is claimed by work item %s of program %q, but that item names project %q; "+
				"a managed watcher must wake the worker that owns this exact project",
			slug, item.ID, manifest.Program, item.ProjectSlug,
		)
	}
	return nil
}
