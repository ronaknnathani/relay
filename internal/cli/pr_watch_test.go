package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/prwatch"
	"github.com/spf13/cobra"
)

func runPRCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	command := newCmdPR()
	command.SetArgs(args)
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	err := command.Execute()
	return out.String(), err
}

// installPRWatchFakes points every watcher seam at a test double and restores
// the real ones afterwards.
func installPRWatchFakes(t *testing.T, client *fakeHerdrClient) {
	t.Helper()
	previous := struct {
		herdrClient  func() herdrRuntimeClient
		patrolClient func(context.Context) herdrRuntimeClient
		available    func() bool
		running      func(string) (bool, error)
		read         func(string) (prwatch.State, error)
		run          func(context.Context, string, prwatch.Options) error
		tick         func(context.Context, string, prwatch.Options) (prwatch.Digest, error)
		locate       func(string) (prwatch.Target, error)
		now          func() time.Time
		sleep        func(time.Duration)
		signal       func(int, os.Signal) error
	}{
		newHerdrClient, newPatrolHerdrClient, herdrAvailable, prWatchIsRunning, prWatchReadState,
		prWatchRunLoop, prWatchTickOnce, prWatchLocate, prWatchNow, prWatchSleep, prWatchSignal,
	}
	newHerdrClient = func() herdrRuntimeClient { return client }
	newPatrolHerdrClient = func(context.Context) herdrRuntimeClient { return client }
	herdrAvailable = func() bool { return true }
	now := time.Unix(100, 0)
	prWatchNow = func() time.Time { return now }
	prWatchSleep = func(delay time.Duration) { now = now.Add(delay) }
	prWatchLocate = func(slug string) (prwatch.Target, error) {
		return prwatch.Target{Slug: slug, Dir: "/repo/" + slug, PRNumber: 42}, nil
	}
	t.Cleanup(func() {
		newHerdrClient = previous.herdrClient
		newPatrolHerdrClient = previous.patrolClient
		herdrAvailable = previous.available
		prWatchIsRunning = previous.running
		prWatchReadState = previous.read
		prWatchRunLoop = previous.run
		prWatchTickOnce = previous.tick
		prWatchLocate = previous.locate
		prWatchNow = previous.now
		prWatchSleep = previous.sleep
		prWatchSignal = previous.signal
	})
}

func TestPRWatchStartCreatesAWatcherTabAndRunsTheHiddenProcess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
	client := &fakeHerdrClient{tab: herdr.Tab{ID: "tab-1", RootPaneID: "pane-1"}}
	installPRWatchFakes(t, client)
	calls := 0
	prWatchIsRunning = func(string) (bool, error) {
		calls++
		return calls > 1, nil
	}
	prWatchReadState = func(slug string) (prwatch.State, error) {
		return prwatch.State{
			Schema: prwatch.SchemaVersion, Version: 1, Project: slug, PID: 77,
			Status: prwatch.StatusRunning, Mode: prwatch.ModeStandalone, OwnerSlug: slug,
		}, nil
	}

	out, err := runPRCommand(t, "watch", "start", "demo", "--json")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var got prWatchStartOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if got.Project != "demo" || !got.Running || got.Adopted {
		t.Fatalf("start output = %+v", got)
	}
	if want := []fakeCreatedTab{{
		workspace: "workspace-1", cwd: "/repo/demo", label: "relay-pr-watch:demo",
	}}; !reflect.DeepEqual(client.created, want) {
		t.Fatalf("created tabs = %#v, want %#v", client.created, want)
	}
	if want := []fakePaneCommand{{
		pane: "pane-1", command: "relay pr watch run 'demo' --mode 'standalone' --owner 'demo'",
	}}; !reflect.DeepEqual(client.runPane, want) {
		t.Fatalf("pane commands = %#v, want %#v", client.runPane, want)
	}
}

func TestPRWatchStartAdoptsARunningWatcherAndWarnsOnADifferentTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &fakeHerdrClient{}
	installPRWatchFakes(t, client)
	prWatchIsRunning = func(string) (bool, error) { return true, nil }
	prWatchReadState = func(slug string) (prwatch.State, error) {
		return prwatch.State{
			Project: slug, PID: 5, Status: prwatch.StatusRunning,
			Mode: prwatch.ModeStandalone, OwnerSlug: slug,
		}, nil
	}

	out, err := runPRCommand(t, "watch", "start", "demo", "--json")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var adopted prWatchStartOutput
	if err := json.Unmarshal([]byte(out), &adopted); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if !adopted.Adopted || adopted.Warning != "" {
		t.Fatalf("adopted output = %+v, want a clean adoption", adopted)
	}
	if len(client.created) != 0 {
		t.Fatalf("adoption created tabs: %+v", client.created)
	}

	out, err = runPRCommand(t, "watch", "start", "demo", "--mode", "stack", "--owner", "stack-run", "--json")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var retarget prWatchStartOutput
	if err := json.Unmarshal([]byte(out), &retarget); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if !strings.Contains(retarget.Warning, "relay pr watch stop demo") {
		t.Errorf("warning = %q, want the stop command for a differently targeted watcher", retarget.Warning)
	}
}

func TestPRWatchStartRequiresHerdr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &fakeHerdrClient{}
	installPRWatchFakes(t, client)
	prWatchIsRunning = func(string) (bool, error) { return false, nil }

	_, err := runPRCommand(t, "watch", "start", "demo")
	if err == nil || !strings.Contains(err.Error(), "HERDR_ENV=1") {
		t.Fatalf("start outside Herdr = %v, want a Herdr readiness failure", err)
	}
	if len(client.created) != 0 {
		t.Fatalf("start outside Herdr created tabs: %+v", client.created)
	}
}

func TestPRWatchStackModeRequiresAnOwner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installPRWatchFakes(t, &fakeHerdrClient{})
	prWatchIsRunning = func(string) (bool, error) { return false, nil }

	_, err := runPRCommand(t, "watch", "start", "demo", "--mode", "stack")
	if err == nil || !strings.Contains(err.Error(), "--owner") {
		t.Fatalf("stack start without an owner = %v, want an owner requirement", err)
	}

	_, err = runPRCommand(t, "watch", "start", "demo", "--mode", "managed", "--owner", "program-tl")
	if err == nil || !strings.Contains(err.Error(), "not a valid owner") {
		t.Fatalf("managed start with a foreign owner = %v, want a rejection", err)
	}
}

func TestPRWatchRunRequiresHerdrAndPassesTheOwner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &fakeHerdrClient{}
	installPRWatchFakes(t, client)
	herdrAvailable = func() bool { return false }

	_, err := runPRCommand(t, "watch", "run", "demo", "--mode", "stack", "--owner", "stack-run")
	if err == nil || !strings.Contains(err.Error(), "requires Herdr") {
		t.Fatalf("run without Herdr = %v, want a readiness failure", err)
	}

	herdrAvailable = func() bool { return true }
	var got prwatch.Options
	prWatchRunLoop = func(_ context.Context, slug string, options prwatch.Options) error {
		got = options
		if slug != "demo" {
			t.Errorf("run slug = %q", slug)
		}
		return nil
	}
	prWatchReadState = func(slug string) (prwatch.State, error) {
		return prwatch.State{Project: slug, Status: prwatch.StatusStopped}, nil
	}
	out, err := runPRCommand(t, "watch", "run", "demo", "--mode", "stack", "--owner", "stack-run")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got.Mode != prwatch.ModeStack || got.Owner != "stack-run" {
		t.Errorf("run options = %+v, want the stack orchestrator", got)
	}
	if got.Client == nil {
		t.Error("run options carried no Herdr client")
	}
	if !strings.Contains(out, "PR watch stopped for demo") {
		t.Errorf("run output = %q", out)
	}
}

func TestPRWatchStatusWorksWithoutHerdr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installPRWatchFakes(t, &fakeHerdrClient{})
	herdrAvailable = func() bool { return false }
	prWatchIsRunning = func(string) (bool, error) { return true, nil }
	prWatchReadState = func(slug string) (prwatch.State, error) {
		return prwatch.State{
			Project: slug, Status: prwatch.StatusRunning, Mode: prwatch.ModeStandalone,
			OwnerSlug: slug, PRNumber: 42, PRState: "OPEN", ScheduledChecks: 3,
			NextCheckAt: "2026-03-01T09:00:00Z", ActionableCount: 2,
			CurrentFingerprint: strings.Repeat("a", 64), RelayVersion: version,
		}, nil
	}

	out, err := runPRCommand(t, "watch", "status", "demo", "--json")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var got prWatchStatusOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if got.Status != string(prwatch.StatusRunning) || got.State.ActionableCount != 2 {
		t.Fatalf("status = %+v", got)
	}
	if got.Warning != "" {
		t.Errorf("status warning = %q, want none", got.Warning)
	}

	text, err := runPRCommand(t, "watch", "status", "demo")
	if err != nil {
		t.Fatalf("status text: %v", err)
	}
	for _, want := range []string{"PR watch: running", "Owner: demo", "PR: #42 OPEN", "Scheduled checks: 3"} {
		if !strings.Contains(text, want) {
			t.Errorf("status text %q is missing %q", text, want)
		}
	}
}

func TestPRWatchStatusReportsACompletedWatcher(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installPRWatchFakes(t, &fakeHerdrClient{})
	prWatchIsRunning = func(string) (bool, error) { return false, nil }
	prWatchReadState = func(slug string) (prwatch.State, error) {
		return prwatch.State{
			Project: slug, Status: prwatch.StatusComplete, StopReason: "pull request merged",
			RelayVersion: version,
		}, nil
	}
	out, err := runPRCommand(t, "watch", "status", "demo", "--json")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var got prWatchStatusOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if got.Status != string(prwatch.StatusComplete) || got.StopReason != "pull request merged" {
		t.Fatalf("status = %+v, want the completed watch", got)
	}
}

func TestPRWatchStatusOfARecordWithoutALifecycleStaysNotRunning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installPRWatchFakes(t, &fakeHerdrClient{})
	prWatchIsRunning = func(string) (bool, error) { return false, nil }
	// A record written before any watcher process ran carries no lifecycle
	// status; status must still read as not-running.
	prWatchReadState = func(slug string) (prwatch.State, error) {
		return prwatch.State{Project: slug, NextCheckAt: "2026-03-01T09:00:00Z"}, nil
	}

	out, err := runPRCommand(t, "watch", "status", "demo", "--json")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var got prWatchStatusOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if got.Status != "not-running" {
		t.Errorf("status = %q, want not-running", got.Status)
	}
}

func TestPRWatchStopSignalsTheRecordedProcess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installPRWatchFakes(t, &fakeHerdrClient{})
	calls := 0
	prWatchIsRunning = func(string) (bool, error) {
		calls++
		return calls == 1, nil
	}
	prWatchReadState = func(slug string) (prwatch.State, error) {
		return prwatch.State{Project: slug, PID: 4242, Status: prwatch.StatusRunning}, nil
	}
	var signaled []int
	prWatchSignal = func(pid int, _ os.Signal) error {
		signaled = append(signaled, pid)
		return nil
	}

	out, err := runPRCommand(t, "watch", "stop", "demo")
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if len(signaled) != 1 || signaled[0] != 4242 {
		t.Fatalf("signaled = %v, want the recorded pid", signaled)
	}
	if !strings.Contains(out, "PR watcher stopped for demo") {
		t.Errorf("stop output = %q", out)
	}
}

func TestPRWatchTickAndDigestWorkWithoutHerdr(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installPRWatchFakes(t, &fakeHerdrClient{})
	herdrAvailable = func() bool { return false }
	items := []prwatch.Item{{
		Reason: prwatch.ReasonNewComment, Source: prwatch.SourceComment, ID: "1",
		Key: "comment:1:2026-01-01T00:00:00Z", Body: "please rename this", Author: "reviewer",
	}}
	digest := prwatch.Digest{
		Project: "demo", Mode: prwatch.ModeStandalone, Fingerprint: prwatch.Fingerprint(items),
		ObservedAt: "2026-03-01T08:00:00Z", HeadSHA: "head222",
		PR:      prwatch.PullRequest{Number: 42, State: "OPEN", HeadSHA: "head222"},
		Items:   items,
		Waiting: []string{},
	}
	prWatchTickOnce = func(context.Context, string, prwatch.Options) (prwatch.Digest, error) {
		if err := prwatch.WriteDigest(digest); err != nil {
			t.Fatalf("WriteDigest: %v", err)
		}
		return digest, nil
	}

	out, err := runPRCommand(t, "watch", "tick", "demo", "--json")
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	var ticked prwatch.Digest
	if err := json.Unmarshal([]byte(out), &ticked); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if ticked.Fingerprint != digest.Fingerprint {
		t.Fatalf("tick digest = %+v", ticked)
	}

	out, err = runPRCommand(t, "watch", "digest", "demo", "--fingerprint", digest.Fingerprint, "--json")
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	var read prwatch.Digest
	if err := json.Unmarshal([]byte(out), &read); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if len(read.Items) != 1 || read.Items[0].Body != "please rename this" {
		t.Fatalf("digest items = %+v, want the recorded body", read.Items)
	}

	text, err := runPRCommand(t, "watch", "digest", "demo", "--fingerprint", digest.Fingerprint)
	if err != nil {
		t.Fatalf("digest text: %v", err)
	}
	if strings.Contains(text, "please rename this") {
		t.Errorf("digest text printed a body: %q", text)
	}
	if !strings.Contains(text, "new-comment comment 1") {
		t.Errorf("digest text = %q, want the item summary", text)
	}
}

func TestPRWatchRejectsInvalidFingerprints(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installPRWatchFakes(t, &fakeHerdrClient{})

	if _, err := runPRCommand(t, "watch", "digest", "demo", "--fingerprint", "../escape"); err == nil {
		t.Error("digest accepted a traversing fingerprint")
	}
	if _, err := runPRCommand(t, "watch", "digest", "demo",
		"--fingerprint", strings.Repeat("a", 64)); err == nil {
		t.Error("digest accepted a fingerprint with no record")
	}
}

// The acknowledgement subsystem is gone: a watcher never asserts locally that
// attention was handled, so there is no command that could claim it.
func TestPRWatchHasNoAcknowledgeCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installPRWatchFakes(t, &fakeHerdrClient{})
	out, err := runPRCommand(t, "watch", "--help")
	if err != nil {
		t.Fatalf("watch help: %v", err)
	}
	if strings.Contains(out, "acknowledge") {
		t.Errorf("`relay pr watch --help` still offers acknowledge:\n%s", out)
	}
	out, _ = runPRCommand(t, "watch", "acknowledge", "demo")
	if !strings.Contains(out, "Available Commands:") || strings.Contains(out, "acknowledge ") {
		t.Errorf("`relay pr watch acknowledge` did not fall back to usage:\n%s", out)
	}
}

func TestPRWatchTickReportsAProjectWithoutAPullRequest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(project.ActiveDir(), "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	if err := project.Save(project.ManifestPath(project.ActiveDir(), "demo"), project.Manifest{
		Slug: "demo", Repo: home,
	}); err != nil {
		t.Fatalf("save manifest: %v", err)
	}
	if _, err := runPRCommand(t, "watch", "tick", "demo"); err == nil ||
		!strings.Contains(err.Error(), "relay state pr demo") {
		t.Fatalf("tick without a pull request = %v, want the recording command", err)
	}
}

func TestPRWatchIsRegisteredOnTheRootCommand(t *testing.T) {
	root := newRootCmd()
	var pr *cobra.Command
	for _, command := range root.Commands() {
		if command.Name() == "pr" {
			pr = command
		}
	}
	if pr == nil {
		t.Fatal("root command has no `pr` command")
	}
	var watch *cobra.Command
	for _, command := range pr.Commands() {
		if command.Name() == "watch" {
			watch = command
		}
	}
	if watch == nil {
		t.Fatal("`relay pr` has no `watch` command")
	}
	want := map[string]bool{
		"start": true, "run": true, "status": true, "stop": true,
		"tick": true, "digest": true,
	}
	for _, command := range watch.Commands() {
		if command.Name() == "acknowledge" {
			t.Error("`relay pr watch acknowledge` is still registered")
		}
		delete(want, command.Name())
		if command.Name() == "run" && !command.Hidden {
			t.Error("`relay pr watch run` is not hidden")
		}
	}
	if len(want) != 0 {
		t.Errorf("`relay pr watch` is missing %v", want)
	}
}
