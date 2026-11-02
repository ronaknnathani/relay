package herdr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
)

// The Herdr desktop app (herdr-app) shows terminals that run in herdr-core and
// are driven by the `hc` CLI. It has no workspace/pane/agent commands of its
// own, so the desktop backend maps Relay's model onto core terminals:
//
//   - a workspace is a desktop space (the terminal label herdr.space)
//   - a tab is a desktop tab (the terminal label herdr.tab)
//   - a pane, and its terminal, is one core terminal address such as t7
//   - an agent is a terminal whose program reports status through OSC 7501
const (
	desktopSpaceLabel     = "herdr.space"
	desktopTabLabel       = "herdr.tab"
	desktopRelayTabLabel  = "relay.tab"
	desktopRelayNameLabel = "relay.name"
)

// DesktopEnvironment reports whether Relay runs in a Herdr desktop app
// terminal rather than a Herdr TUI pane. The TUI wins when both are present,
// because a TUI running inside a desktop terminal owns its own panes.
func DesktopEnvironment() bool {
	return os.Getenv("HERDR_ENV") != "1" && strings.TrimSpace(os.Getenv("HC_TERMINAL_ID")) != ""
}

// InManagedTerminal reports whether Relay runs inside either Herdr frontend.
func InManagedTerminal() bool {
	return os.Getenv("HERDR_ENV") == "1" || DesktopEnvironment()
}

type desktopBackend struct {
	hc       CommandRunner
	app      CommandRunner
	notify   func(title, body string) error
	newTabID func() (string, error)
}

// NewDesktopClientWithRunners creates a Client for the Herdr desktop app.
// hc runs `hc` commands and app runs `herdr-app` commands.
func NewDesktopClientWithRunners(hc, app CommandRunner) *Client {
	client := newClientWithRunners(nil, nil)
	client.desktop = &desktopBackend{hc: hc, app: app, notify: macNotification, newTabID: randomTabID}
	return client
}

func newDesktopClient(ctx context.Context, timeout time.Duration) *Client {
	if ctx == nil {
		ctx = context.Background()
	}
	runner := func(binary string) CommandRunner {
		return func(args ...string) ([]byte, error) {
			commandCtx, cancel := ctx, context.CancelFunc(func() {})
			if timeout > 0 {
				commandCtx, cancel = context.WithTimeout(ctx, timeout)
			}
			defer cancel()
			command := exec.CommandContext(commandCtx, binary, args...)
			// hc wraps text sent from inside one of its terminals in an <hc from=...>
			// note, which turns /exit into an ordinary message. Relay speaks for the
			// operator, so it calls hc as an outside client.
			command.Env = withoutEnv(os.Environ(), "HC_TERMINAL_ID")
			output, err := command.CombinedOutput()
			if commandCtx.Err() != nil {
				return output, commandCtx.Err()
			}
			return output, err
		}
	}
	return NewDesktopClientWithRunners(runner("hc"), runner("herdr-app"))
}

func withoutEnv(env []string, name string) []string {
	kept := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, name+"=") {
			kept = append(kept, entry)
		}
	}
	return kept
}

func randomTabID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate Herdr desktop tab id: %w", err)
	}
	return "tb" + hex.EncodeToString(raw[:]), nil
}

func macNotification(title, body string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("desktop notifications are only implemented on macOS")
	}
	script := fmt.Sprintf("display notification %q with title %q", body, title)
	if output, err := exec.Command("osascript", "-e", script).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// hcTerminal is the subset of `hc get --json` Relay uses.
type hcTerminal struct {
	Address string            `json:"address"`
	CWD     string            `json:"cwd"`
	State   string            `json:"state"`
	Title   string            `json:"title"`
	Labels  map[string]string `json:"labels"`
	Status  *struct {
		State string `json:"state"`
	} `json:"status"`
	StatusMS   int64 `json:"status_ms"`
	Foreground *struct {
		CWD string `json:"cwd"`
	} `json:"foreground"`
}

func (t hcTerminal) status() Status {
	if t.Status == nil || t.Status.State == "" {
		return ""
	}
	return Status(t.Status.State)
}

func (t hcTerminal) foregroundCWD() string {
	if t.Foreground == nil {
		return ""
	}
	return t.Foreground.CWD
}

func (d *desktopBackend) run(target any, args ...string) error {
	output, err := d.hc(args...)
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			return fmt.Errorf("run hc %s: %w", strings.Join(args, " "), err)
		}
		return fmt.Errorf("run hc %s: %w: %s", strings.Join(args, " "), err, detail)
	}
	if target == nil {
		return nil
	}
	if err := json.Unmarshal(output, target); err != nil {
		return fmt.Errorf("parse hc %s JSON: %w", strings.Join(args, " "), err)
	}
	return nil
}

func (d *desktopBackend) runApp(command string, args map[string]string) error {
	if d.app == nil {
		return errors.New("herdr-app command runner is not configured")
	}
	payload, err := json.Marshal(args)
	if err != nil {
		return err
	}
	output, err := d.app("do", command, string(payload))
	if err != nil {
		return fmt.Errorf("run herdr-app do %s: %w: %s", command, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// terminals lists live local terminals carrying every label in filter, with
// their full facts. `hc ls` omits labels and titles, so each one is read.
// onlyAgents skips terminals that never reported a status.
func (d *desktopBackend) terminals(onlyAgents bool, filter ...string) ([]hcTerminal, error) {
	args := []string{"ls", "--json"}
	for _, label := range filter {
		args = append(args, "--label", label)
	}
	var listing struct {
		Terminals []struct {
			Address string `json:"address"`
			State   string `json:"state"`
			Status  string `json:"status"`
		} `json:"terminals"`
	}
	if err := d.run(&listing, args...); err != nil {
		return nil, err
	}
	terminals := make([]hcTerminal, 0, len(listing.Terminals))
	for _, entry := range listing.Terminals {
		// Remote machines use machine/tN addresses and their own label ids.
		if entry.State != "live" || strings.Contains(entry.Address, "/") {
			continue
		}
		if onlyAgents && entry.Status == "" {
			continue
		}
		terminal, err := d.get(entry.Address)
		if err != nil {
			if strings.Contains(err.Error(), "not_found") {
				continue
			}
			return nil, err
		}
		if terminal.State != "live" {
			continue
		}
		terminals = append(terminals, terminal)
	}
	return terminals, nil
}

func (d *desktopBackend) get(address string) (hcTerminal, error) {
	var terminal hcTerminal
	if err := d.run(&terminal, "get", "--json", address); err != nil {
		return hcTerminal{}, err
	}
	if terminal.Address == "" {
		terminal.Address = address
	}
	return terminal, nil
}

func (d *desktopBackend) workspaces() ([]Workspace, error) {
	terminals, err := d.terminals(false)
	if err != nil {
		return nil, fmt.Errorf("list Herdr desktop spaces: %w", err)
	}
	seen := map[string]bool{}
	var workspaces []Workspace
	for _, terminal := range terminals {
		space := terminal.Labels[desktopSpaceLabel]
		if space == "" || seen[space] {
			continue
		}
		seen[space] = true
		workspaces = append(workspaces, Workspace{ID: space})
	}
	return workspaces, nil
}

func (d *desktopBackend) tabs(workspaceID string) ([]TabInfo, error) {
	terminals, err := d.terminals(false, desktopSpaceLabel+"="+workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list Herdr desktop tabs in space %q: %w", workspaceID, err)
	}
	byID := map[string]*TabInfo{}
	var order []string
	for _, terminal := range terminals {
		id := terminal.Labels[desktopTabLabel]
		if id == "" {
			continue
		}
		tab, ok := byID[id]
		if !ok {
			tab = &TabInfo{ID: id, WorkspaceID: workspaceID, Label: terminal.Labels[desktopRelayTabLabel]}
			byID[id] = tab
			order = append(order, id)
		}
		tab.PaneCount++
		if statusUrgency(terminal.status()) > statusUrgency(tab.Status) {
			tab.Status = terminal.status()
		}
		if tab.Label == "" {
			tab.Label = terminal.Labels[desktopRelayTabLabel]
		}
	}
	tabs := make([]TabInfo, 0, len(order))
	for _, id := range order {
		tabs = append(tabs, *byID[id])
	}
	return tabs, nil
}

// statusUrgency orders statuses the way the desktop app summarizes a tab.
func statusUrgency(status Status) int {
	switch status {
	case StatusBlocked, "error":
		return 4
	case StatusDone:
		return 3
	case StatusWorking:
		return 2
	case StatusIdle:
		return 1
	}
	return 0
}

func (d *desktopBackend) panes(workspaceID string) ([]Pane, error) {
	terminals, err := d.terminals(false, desktopSpaceLabel+"="+workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list Herdr desktop terminals in space %q: %w", workspaceID, err)
	}
	panes := make([]Pane, 0, len(terminals))
	for _, terminal := range terminals {
		panes = append(panes, Pane{
			ID:            terminal.Address,
			TabID:         terminal.Labels[desktopTabLabel],
			WorkspaceID:   workspaceID,
			TerminalID:    terminal.Address,
			Status:        terminal.status(),
			CWD:           terminal.CWD,
			ForegroundCWD: terminal.foregroundCWD(),
		})
	}
	return panes, nil
}

func (d *desktopBackend) agents() ([]Agent, error) {
	terminals, err := d.terminals(true)
	if err != nil {
		return nil, fmt.Errorf("list Herdr desktop agents: %w", err)
	}
	agents := make([]Agent, 0, len(terminals))
	for _, terminal := range terminals {
		agents = append(agents, Agent{
			Status:         terminal.status(),
			StateChangeSeq: terminal.StatusMS,
			PaneID:         terminal.Address,
			TabID:          terminal.Labels[desktopTabLabel],
			WorkspaceID:    terminal.Labels[desktopSpaceLabel],
			TerminalID:     terminal.Address,
			TerminalTitle:  terminal.Title,
			CWD:            terminal.CWD,
			ForegroundCWD:  terminal.foregroundCWD(),
		})
	}
	sort.SliceStable(agents, func(i, j int) bool { return agents[i].PaneID < agents[j].PaneID })
	return agents, nil
}

// createTab starts a shell in a new desktop tab. The app adopts any terminal
// labeled with an existing space and an unknown tab id as a new tab there.
// The terminal has no parent, so it outlives the Relay command that made it.
func (d *desktopBackend) createTab(workspaceID, cwd, label string) (Tab, error) {
	tabID, err := d.newTabID()
	if err != nil {
		return Tab{}, err
	}
	var started struct {
		Address string `json:"address"`
	}
	if err := d.run(
		&started,
		"run", "--bg", "--json", "--keep", "--no-parent", "--outlive-parent", "--no-notify",
		"--cwd", cwd,
		"--label", desktopSpaceLabel+"="+workspaceID,
		"--label", desktopTabLabel+"="+tabID,
		"--label", desktopRelayTabLabel+"="+label,
	); err != nil {
		return Tab{}, fmt.Errorf("create Herdr desktop tab in space %q: %w", workspaceID, err)
	}
	if started.Address == "" {
		return Tab{}, fmt.Errorf("create Herdr desktop tab in space %q: hc reported no terminal address", workspaceID)
	}
	// Naming the tab is cosmetic and needs the window; Relay identifies the
	// tab by its relay.tab label.
	_ = d.runApp("tab.rename", map[string]string{"id": tabID, "name": label})
	return Tab{ID: tabID, RootPaneID: started.Address, WorkspaceID: workspaceID, TerminalID: started.Address}, nil
}

// closeTab removes every terminal in the tab. Finished terminals still hold a
// pane in the app, so it lists them too and removes them without ending them.
func (d *desktopBackend) closeTab(tabID string) error {
	var listing struct {
		Terminals []struct {
			Address string `json:"address"`
			State   string `json:"state"`
		} `json:"terminals"`
	}
	if err := d.run(&listing, "ls", "--all", "--json", "--label", desktopTabLabel+"="+tabID); err != nil {
		return err
	}
	for _, terminal := range listing.Terminals {
		if strings.Contains(terminal.Address, "/") {
			continue
		}
		if terminal.State == "live" {
			if err := d.closeTerminal(terminal.Address); err != nil {
				return err
			}
			continue
		}
		if err := d.remove(terminal.Address); err != nil {
			return err
		}
	}
	return nil
}

// closeTerminal ends a terminal's program, then removes the finished terminal
// so the desktop app drops its pane.
func (d *desktopBackend) closeTerminal(address string) error {
	if err := d.run(nil, "kill", "--json", address); err != nil && !strings.Contains(err.Error(), "not_found") {
		return err
	}
	return d.remove(address)
}

func (d *desktopBackend) remove(address string) error {
	if err := d.run(nil, "rm", "--json", address); err != nil && !strings.Contains(err.Error(), "not_found") {
		return err
	}
	return nil
}

func (d *desktopBackend) send(address, text string) error {
	return d.run(nil, "send", "--json", address, text)
}

func (d *desktopBackend) pressEnter(address string) error {
	return d.run(nil, "keys", "--json", address, "enter")
}

func (d *desktopBackend) rename(address, name string) error {
	return d.run(nil, "label", "--json", address, desktopRelayNameLabel+"="+name)
}

func (d *desktopBackend) focus(address string) error {
	return d.runApp("agent.focus", map[string]string{"terminal": address})
}

// currentDesktopSpace is the space of the terminal Relay runs in.
var currentDesktopSpace = func() (string, error) {
	address := strings.TrimSpace(os.Getenv("HC_TERMINAL_ID"))
	output, err := exec.Command("hc", "get", "--json", address).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("read Herdr desktop terminal %s: %w: %s", address, err, strings.TrimSpace(string(output)))
	}
	var terminal hcTerminal
	if err := json.Unmarshal(output, &terminal); err != nil {
		return "", fmt.Errorf("parse Herdr desktop terminal %s: %w", address, err)
	}
	space := strings.TrimSpace(terminal.Labels[desktopSpaceLabel])
	if space == "" {
		return "", fmt.Errorf(
			"terminal %s is not shown in a Herdr desktop space (no %s label); run Relay from a tab in the Herdr app",
			address, desktopSpaceLabel,
		)
	}
	return space, nil
}
