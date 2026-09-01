package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"time"

	"github.com/ronaknnathani/relay/internal/mailbox"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/spf13/cobra"
)

type programMessageTarget struct {
	program    program.Program
	item       program.WorkItem
	manifest   project.Manifest
	projectDir string
}

type programMessageOutput struct {
	mailbox.Message
	ItemTitle  string             `json:"item_title"`
	ItemStatus program.ItemStatus `json:"item_status"`
	Project    string             `json:"project"`
	ProjectDir string             `json:"project_dir"`
	Worktree   string             `json:"worktree"`
}

type programMessageListOutput struct {
	Messages []programMessageOutput `json:"messages"`
	Warnings []programItemWarning   `json:"warnings"`
}

func newCmdProgramMessage() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "message",
		Short: "Exchange durable managed-worker messages",
	}
	cmd.AddCommand(
		newCmdProgramMessageSend(),
		newCmdProgramMessageList(),
		newCmdProgramMessageInbox(),
		newCmdProgramMessageOutbox(),
		newCmdProgramMessageReply(),
		newCmdProgramMessageNotify(),
		newCmdProgramMessageAck(),
	)
	return cmd
}

func newCmdProgramMessageSend() *cobra.Command {
	var kind, body, options string
	cmd := &cobra.Command{
		Use:   "send <program> <item>",
		Short: "Send a worker message to the tech lead",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramMessageSend(cmd.OutOrStdout(), args[0], args[1], kind, body, options)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "message kind: question, conflict, plan, or pr-open")
	cmd.Flags().StringVar(&body, "body", "", "message body")
	cmd.Flags().StringVar(&options, "options", "", "pipe-separated options")
	return cmd
}

func newCmdProgramMessageList() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list <program>",
		Short: "List unread worker messages for a program",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramMessageList(cmd.OutOrStdout(), args[0], jsonOutput)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

func newCmdProgramMessageInbox() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "inbox <program> <item>",
		Short: "List unread tech lead messages for a worker",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramMessageInbox(cmd.OutOrStdout(), args[0], args[1], jsonOutput)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

func newCmdProgramMessageOutbox() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "outbox <program> <item>",
		Short: "List unread worker messages sent to the tech lead",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramMessageOutbox(cmd.OutOrStdout(), args[0], args[1], jsonOutput)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

func newCmdProgramMessageReply() *cobra.Command {
	var kind, body, decisionID string
	cmd := &cobra.Command{
		Use:   "reply <program> <item> <outbox-id>",
		Short: "Reply to and acknowledge a worker message",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramMessageReply(cmd.OutOrStdout(), args[0], args[1], args[2], kind, body, decisionID)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", string(mailbox.KindDecision), "message kind: decision, feedback, or instruction")
	cmd.Flags().StringVar(&body, "body", "", "message body")
	cmd.Flags().StringVar(&decisionID, "decision", "", "related program decision id")
	return cmd
}

func newCmdProgramMessageNotify() *cobra.Command {
	var kind, body, decisionID string
	cmd := &cobra.Command{
		Use:   "notify <program> <item>",
		Short: "Send an unsolicited tech lead message to a worker",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramMessageNotify(cmd.OutOrStdout(), args[0], args[1], kind, body, decisionID)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "message kind: decision, feedback, or instruction")
	cmd.Flags().StringVar(&body, "body", "", "message body")
	cmd.Flags().StringVar(&decisionID, "decision", "", "related program decision id")
	return cmd
}

func newCmdProgramMessageAck() *cobra.Command {
	return &cobra.Command{
		Use:   "ack <program> <item> <inbox-id>",
		Short: "Acknowledge a processed worker inbox message",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramMessageAck(cmd.OutOrStdout(), args[0], args[1], args[2])
		},
	}
}

func runProgramMessageSend(out io.Writer, programSlug, itemID, kind, body, options string) error {
	target, err := loadProgramMessageTarget(programSlug, itemID)
	if err != nil {
		return err
	}
	message, err := sendProgramMail(target.projectDir, mailbox.Outbox, mailbox.Message{
		Kind:    mailbox.Kind(kind),
		Program: target.program.Slug,
		Item:    target.item.ID,
		From:    mailbox.ActorWorker,
		To:      mailbox.ActorTL,
		Body:    body,
		Options: parseProgramOptions(options),
	})
	if err != nil {
		return fmt.Errorf("send program message for %s/%s: %w", programSlug, itemID, err)
	}
	fmt.Fprintln(out, message.ID)
	return nil
}

func runProgramMessageList(out io.Writer, programSlug string, jsonOutput bool) error {
	_, p, err := loadActiveProgram(programSlug)
	if err != nil {
		return err
	}
	if p.State != program.StateActive {
		return fmt.Errorf("list program messages: program %q is %s, want active", p.Slug, p.State)
	}
	result := programMessageListOutput{
		Messages: make([]programMessageOutput, 0),
		Warnings: make([]programItemWarning, 0),
	}
	for _, item := range p.Items {
		if item.ProjectSlug == "" || !activeProgramWorkerStatus(item.Status) {
			continue
		}
		target, err := programMessageTargetFor(p, item)
		if err != nil {
			result.Warnings = append(result.Warnings, programItemWarning{
				Item: item.ID, Project: item.ProjectSlug, Error: err.Error(),
			})
			continue
		}
		unread, err := mailbox.List(target.projectDir, mailbox.Outbox)
		if err != nil {
			result.Warnings = append(result.Warnings, programItemWarning{
				Item:    item.ID,
				Project: item.ProjectSlug,
				Error:   fmt.Errorf("list program messages for %s/%s: %w", p.Slug, item.ID, err).Error(),
			})
			continue
		}
		for _, message := range unread {
			if message.Program != p.Slug || message.Item != item.ID {
				result.Warnings = append(result.Warnings, programItemWarning{
					Item:    item.ID,
					Project: item.ProjectSlug,
					Error: fmt.Sprintf(
						"list program messages: outbox message %q belongs to %s/%s, want %s/%s",
						message.ID, message.Program, message.Item, p.Slug, item.ID,
					),
				})
				continue
			}
			result.Messages = append(result.Messages, newProgramMessageOutput(item, target.manifest, message))
		}
	}
	sortProgramMessages(result.Messages)
	return renderProgramMessageList(out, result, jsonOutput)
}

func runProgramMessageInbox(out io.Writer, programSlug, itemID string, jsonOutput bool) error {
	target, err := loadProgramMessageTarget(programSlug, itemID)
	if err != nil {
		return err
	}
	unread, err := mailbox.List(target.projectDir, mailbox.Inbox)
	if err != nil {
		return fmt.Errorf("list program inbox for %s/%s: %w", programSlug, itemID, err)
	}
	messages := make([]programMessageOutput, 0, len(unread))
	for _, message := range unread {
		if message.Program != target.program.Slug || message.Item != target.item.ID {
			return fmt.Errorf(
				"list program inbox: message %q belongs to %s/%s, want %s/%s",
				message.ID, message.Program, message.Item, target.program.Slug, target.item.ID,
			)
		}
		messages = append(messages, newProgramMessageOutput(target.item, target.manifest, message))
	}
	return renderProgramMessages(out, messages, jsonOutput)
}

func runProgramMessageOutbox(out io.Writer, programSlug, itemID string, jsonOutput bool) error {
	target, err := loadProgramMessageTarget(programSlug, itemID)
	if err != nil {
		return err
	}
	unread, err := mailbox.List(target.projectDir, mailbox.Outbox)
	if err != nil {
		return fmt.Errorf("list program outbox for %s/%s: %w", programSlug, itemID, err)
	}
	messages := make([]programMessageOutput, 0, len(unread))
	for _, message := range unread {
		if err := validateProgramMessageAssociation(target, message); err != nil {
			return err
		}
		messages = append(messages, newProgramMessageOutput(target.item, target.manifest, message))
	}
	return renderProgramMessages(out, messages, jsonOutput)
}

func runProgramMessageReply(
	out io.Writer,
	programSlug, itemID, outboxID, kind, body, decisionID string,
) error {
	target, err := loadProgramMessageTarget(programSlug, itemID)
	if err != nil {
		return err
	}
	outbox, err := mailbox.Find(target.projectDir, mailbox.Outbox, outboxID)
	if err != nil {
		return fmt.Errorf("reply to program message %q for %s/%s: %w", outboxID, programSlug, itemID, err)
	}
	if err := validateProgramMessageAssociation(target, outbox); err != nil {
		return err
	}
	reply, err := sendProgramMail(target.projectDir, mailbox.Inbox, mailbox.Message{
		Kind:       mailbox.Kind(kind),
		Program:    target.program.Slug,
		Item:       target.item.ID,
		From:       mailbox.ActorTL,
		To:         mailbox.ActorWorker,
		Body:       body,
		ReplyTo:    outbox.ID,
		DecisionID: decisionID,
	})
	if err != nil {
		return fmt.Errorf("reply to program message %q for %s/%s: %w", outboxID, programSlug, itemID, err)
	}
	if err := mailbox.Acknowledge(target.projectDir, mailbox.Outbox, outbox.ID); err != nil {
		return fmt.Errorf(
			"reply inbox message %q was written for %s/%s, but outbox message %q could not be acknowledged: %w; "+
				"the inbox reply exists and is safe to inspect with: relay program message inbox %s %s --json",
			reply.ID, programSlug, itemID, outbox.ID, err, programSlug, itemID,
		)
	}
	fmt.Fprintln(out, reply.ID)
	return nil
}

func runProgramMessageNotify(
	out io.Writer,
	programSlug, itemID, kind, body, decisionID string,
) error {
	target, err := loadProgramMessageTarget(programSlug, itemID)
	if err != nil {
		return err
	}
	message, err := sendProgramMail(target.projectDir, mailbox.Inbox, mailbox.Message{
		Kind:       mailbox.Kind(kind),
		Program:    target.program.Slug,
		Item:       target.item.ID,
		From:       mailbox.ActorTL,
		To:         mailbox.ActorWorker,
		Body:       body,
		DecisionID: decisionID,
	})
	if err != nil {
		return fmt.Errorf("notify program worker for %s/%s: %w", programSlug, itemID, err)
	}
	fmt.Fprintln(out, message.ID)
	return nil
}

func runProgramMessageAck(out io.Writer, programSlug, itemID, inboxID string) error {
	target, err := loadProgramMessageTarget(programSlug, itemID)
	if err != nil {
		return err
	}
	message, err := mailbox.Find(target.projectDir, mailbox.Inbox, inboxID)
	if err != nil {
		return fmt.Errorf("acknowledge program message %q for %s/%s: %w", inboxID, programSlug, itemID, err)
	}
	if err := validateProgramMessageAssociation(target, message); err != nil {
		return err
	}
	if err := mailbox.Acknowledge(target.projectDir, mailbox.Inbox, message.ID); err != nil {
		return fmt.Errorf("acknowledge program message %q for %s/%s: %w", inboxID, programSlug, itemID, err)
	}
	fmt.Fprintln(out, message.ID)
	return nil
}

func validateProgramMessageAssociation(target programMessageTarget, message mailbox.Message) error {
	if message.Program != target.program.Slug || message.Item != target.item.ID {
		return fmt.Errorf(
			"program message %q belongs to %s/%s, want %s/%s",
			message.ID, message.Program, message.Item, target.program.Slug, target.item.ID,
		)
	}
	return nil
}

func loadProgramMessageTarget(programSlug, itemID string) (programMessageTarget, error) {
	_, p, err := loadActiveProgram(programSlug)
	if err != nil {
		return programMessageTarget{}, err
	}
	if p.State != program.StateActive {
		return programMessageTarget{}, fmt.Errorf("program message %q: program %q is %s, want active", itemID, p.Slug, p.State)
	}
	item, ok := p.Item(itemID)
	if !ok {
		return programMessageTarget{}, fmt.Errorf("program message %q: item not found", itemID)
	}
	if !activeProgramWorkerStatus(item.Status) {
		return programMessageTarget{}, fmt.Errorf(
			"program message %q: item status is %q, want dispatched, in-review, or blocked",
			itemID, item.Status,
		)
	}
	return programMessageTargetFor(p, item)
}

func programMessageTargetFor(p program.Program, item program.WorkItem) (programMessageTarget, error) {
	if item.ProjectSlug == "" {
		return programMessageTarget{}, fmt.Errorf(
			"program message %q: item is not linked to a child project; dispatch or link it first",
			item.ID,
		)
	}
	manifest, err := loadProgramWorkerManifest(p, item)
	if err != nil {
		return programMessageTarget{}, err
	}
	projectDir := filepath.Dir(project.ManifestPath(project.ActiveDir(), manifest.Slug))
	if err := mailbox.Ensure(projectDir); err != nil {
		return programMessageTarget{}, fmt.Errorf(
			"program message %s/%s: ensure mailbox for child project %q: %w; "+
				"repair the project mail path and retry",
			p.Slug, item.ID, manifest.Slug, err,
		)
	}
	return programMessageTarget{
		program:    p,
		item:       item,
		manifest:   manifest,
		projectDir: projectDir,
	}, nil
}

func newProgramMessageOutput(
	item program.WorkItem,
	manifest project.Manifest,
	message mailbox.Message,
) programMessageOutput {
	return programMessageOutput{
		Message:    message,
		ItemTitle:  item.Title,
		ItemStatus: item.Status,
		Project:    manifest.Slug,
		ProjectDir: filepath.Dir(project.ManifestPath(project.ActiveDir(), manifest.Slug)),
		Worktree:   *manifest.Worktree,
	}
}

func sortProgramMessages(messages []programMessageOutput) {
	sort.Slice(messages, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339Nano, messages[i].CreatedAt)
		right, _ := time.Parse(time.RFC3339Nano, messages[j].CreatedAt)
		if left.Equal(right) {
			return messages[i].ID < messages[j].ID
		}
		return left.Before(right)
	})
}

func renderProgramMessages(out io.Writer, messages []programMessageOutput, jsonOutput bool) error {
	if jsonOutput {
		return writeProgramJSON(out, messages)
	}
	for _, message := range messages {
		fmt.Fprintf(out, "%s  %-11s %-6s %-20s %s\n",
			message.ID, message.Kind, message.Item, message.Project, message.Body)
	}
	return nil
}

func renderProgramMessageList(out io.Writer, result programMessageListOutput, jsonOutput bool) error {
	if jsonOutput {
		return writeProgramJSON(out, result)
	}
	if err := renderProgramMessages(out, result.Messages, false); err != nil {
		return err
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(out, "Warning: %s (%s): %s\n", warning.Item, warning.Project, warning.Error)
	}
	return nil
}
