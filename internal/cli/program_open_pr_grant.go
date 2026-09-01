package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ronaknnathani/relay/internal/mailbox"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/role"
	"github.com/spf13/cobra"
)

type programOpenPRGrantOutput struct {
	Program            string           `json:"program"`
	Item               string           `json:"item"`
	GrantedAt          string           `json:"granted_at"`
	GrantedBy          string           `json:"granted_by"`
	Capacity           program.Capacity `json:"capacity"`
	MessageID          string           `json:"message_id"`
	MessageReplyTo     string           `json:"message_reply_to,omitempty"`
	WorkerNotification string           `json:"worker_notification"`
	Warnings           []string         `json:"warnings,omitempty"`
}

func newCmdProgramGrantOpenPR() *cobra.Command {
	var by string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "grant-open-pr <program> <item>",
		Short: "Reserve PR capacity for a managed worker",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramGrantOpenPR(cmd.OutOrStdout(), args[0], args[1], by, jsonOutput)
		},
	}
	cmd.Flags().StringVar(&by, "by", role.TL, "granting program actor")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output grant and capacity as JSON")
	return cmd
}

func newCmdProgramRevokeOpenPR() *cobra.Command {
	var by, reason string
	cmd := &cobra.Command{
		Use:   "revoke-open-pr <program> <item>",
		Short: "Revoke a managed worker's PR capacity grant",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramRevokeOpenPR(cmd.OutOrStdout(), args[0], args[1], by, reason)
		},
	}
	cmd.Flags().StringVar(&by, "by", role.TL, "revoking program actor")
	cmd.Flags().StringVar(&reason, "reason", "", "reason for revoking the grant")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

func runProgramGrantOpenPR(out io.Writer, programSlug, itemID, by string, jsonOutput bool) error {
	// The granting actor is forced for a bounded automated turn: capacity that
	// automation reserved must never be recorded as a human decision.
	by = programActor(by)
	path, p, err := loadActiveProgram(programSlug)
	if err != nil {
		return err
	}
	if err := p.VerifyHashes(filepath.Dir(path)); err != nil {
		return err
	}
	views, viewWarnings, err := programProjectViews(p)
	if err != nil {
		return err
	}
	if err := p.GrantOpenPR(itemID, by, views); err != nil {
		return err
	}
	if err := program.Save(path, p); err != nil {
		return err
	}

	item, _ := p.Item(itemID)
	message, messageErr := sendOpenPRGrantMessage(p, item)
	if message.ID == "" {
		if messageErr == nil {
			messageErr = fmt.Errorf("mailbox returned no durable inbox message")
		}
		return fmt.Errorf(
			"grant-open-pr for %s/%s: the grant exists durably, but the worker inbox message failed: %w; "+
				"repair it with: relay program message notify %s %s --kind instruction --body %q",
			p.Slug, item.ID, messageErr, p.Slug, item.ID, openPRGrantMessage(p.Slug, item.ID),
		)
	}

	result := programOpenPRGrantOutput{
		Program:        p.Slug,
		Item:           item.ID,
		GrantedAt:      item.PRGrantedAt,
		GrantedBy:      item.PRGrantedBy,
		Capacity:       p.Plan(views).Capacity,
		MessageID:      message.ID,
		MessageReplyTo: message.ReplyTo,
		Warnings:       append([]string{}, viewWarnings...),
	}
	if messageErr != nil {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("grant and inbox message are durable, but the pr-open request could not be acknowledged: %v", messageErr))
	}
	progress := fmt.Sprintf("Granted open-PR capacity to item %s by %s", item.ID, item.PRGrantedBy)
	if err := appendProgramProgress(filepath.Dir(path), progress); err != nil {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("grant and inbox message are durable, but progress could not be recorded: %v", err))
	}
	result.WorkerNotification, err = notifyProgramWorkerBestEffort(p.Slug, item.ID)
	if err != nil {
		result.Warnings = append(result.Warnings, err.Error())
	}
	return renderOpenPRGrant(out, result, jsonOutput)
}

func runProgramRevokeOpenPR(out io.Writer, programSlug, itemID, by, reason string) error {
	by = programActor(by)
	path, p, err := loadActiveProgram(programSlug)
	if err != nil {
		return err
	}
	if err := p.RevokeOpenPR(itemID, by, reason); err != nil {
		return err
	}
	if err := program.Save(path, p); err != nil {
		return err
	}

	item, _ := p.Item(itemID)
	target, err := programMessageTargetFor(p, item)
	if err != nil {
		return fmt.Errorf(
			"revoke-open-pr for %s/%s: the revocation exists durably, but the worker inbox message failed: %w; "+
				"repair it with: relay program message notify %s %s --kind instruction --body %q",
			p.Slug, item.ID, err, p.Slug, item.ID, openPRRevokeMessage(by, reason),
		)
	}
	message, err := sendProgramMail(target.projectDir, mailbox.Inbox, mailbox.Message{
		Kind:    mailbox.KindInstruction,
		Program: p.Slug,
		Item:    item.ID,
		From:    mailbox.ActorTL,
		To:      mailbox.ActorWorker,
		Body:    openPRRevokeMessage(by, reason),
	})
	if err != nil {
		return fmt.Errorf(
			"revoke-open-pr for %s/%s: the revocation exists durably, but the worker inbox message failed: %w; "+
				"repair it with: relay program message notify %s %s --kind instruction --body %q",
			p.Slug, item.ID, err, p.Slug, item.ID, openPRRevokeMessage(by, reason),
		)
	}

	fmt.Fprintf(out, "Revoked open-PR grant for %s/%s by %s: %s\n", p.Slug, item.ID, by, reason)
	fmt.Fprintf(out, "Inbox message: %s\n", message.ID)
	if err := appendProgramProgress(
		filepath.Dir(path),
		fmt.Sprintf("Revoked open-PR capacity for item %s by %s: %s", item.ID, by, reason),
	); err != nil {
		fmt.Fprintf(out, "Warning: revocation and inbox message are durable, but progress could not be recorded: %v\n", err)
	}
	notification, notifyErr := notifyProgramWorkerBestEffort(p.Slug, item.ID)
	fmt.Fprintf(out, "Worker notification: %s\n", notification)
	if notifyErr != nil {
		fmt.Fprintf(out, "Warning: %v\n", notifyErr)
	}
	return nil
}

func sendOpenPRGrantMessage(p program.Program, item program.WorkItem) (mailbox.Message, error) {
	target, err := programMessageTargetFor(p, item)
	if err != nil {
		return mailbox.Message{}, err
	}
	outbox, err := mailbox.List(target.projectDir, mailbox.Outbox)
	if err != nil {
		return mailbox.Message{}, err
	}
	replyTo := ""
	for _, message := range outbox {
		if message.Program == p.Slug && message.Item == item.ID && message.Kind == mailbox.KindPROpen {
			replyTo = message.ID
			break
		}
	}
	message, err := sendProgramMail(target.projectDir, mailbox.Inbox, mailbox.Message{
		Kind:    mailbox.KindInstruction,
		Program: p.Slug,
		Item:    item.ID,
		From:    mailbox.ActorTL,
		To:      mailbox.ActorWorker,
		Body:    openPRGrantMessage(p.Slug, item.ID),
		ReplyTo: replyTo,
	})
	if err != nil {
		return mailbox.Message{}, err
	}
	if replyTo != "" {
		if err := mailbox.Acknowledge(target.projectDir, mailbox.Outbox, replyTo); err != nil {
			return message, fmt.Errorf("acknowledge pr-open request %q after inbox message %q: %w",
				replyTo, message.ID, err)
		}
	}
	return message, nil
}

func openPRGrantMessage(programSlug, itemID string) string {
	return fmt.Sprintf(
		"Open-PR grant approved. Next action: run `relay program can-open-pr %s %s`, then open and record the PR. "+
			"Acknowledge this message only after open-pr succeeds; if open-pr fails, leave it unread.",
		programSlug, itemID,
	)
}

func openPRRevokeMessage(by, reason string) string {
	actor := role.NormalizeIdentity(strings.TrimSpace(by))
	if actor == "" {
		actor = role.TL
	}
	return fmt.Sprintf(
		"Open-PR grant revoked by %s: %s. Stop before open-pr and request another grant when ready.",
		actor, strings.TrimSpace(reason),
	)
}

func notifyProgramWorkerBestEffort(programSlug, itemID string) (string, error) {
	if os.Getenv("HERDR_ENV") != "1" {
		return "skipped outside Herdr", nil
	}
	result, err := notifyLiveProgramWorker(programSlug, itemID)
	if err != nil {
		return "not notified", fmt.Errorf(
			"grant and inbox message are durable, but the Herdr doorbell failed: %w",
			err,
		)
	}
	switch result.outcome {
	case programWorkerNotified:
		return "notified live worker", nil
	case programWorkerBusy:
		return fmt.Sprintf("worker is %s; durable inbox remains pending", result.agent.Status), nil
	case programWorkerNoMessage:
		return "no unnotified inbox messages", nil
	case programWorkerNoLiveWorker:
		return "no live worker; durable inbox will be read on resume", nil
	default:
		return "not notified", fmt.Errorf(
			"grant and inbox message are durable, but the Herdr doorbell returned unexpected outcome %q",
			result.outcome,
		)
	}
}

func renderOpenPRGrant(out io.Writer, result programOpenPRGrantOutput, jsonOutput bool) error {
	if jsonOutput {
		return writeProgramJSON(out, result)
	}
	fmt.Fprintf(out, "Granted open-PR capacity to %s/%s by %s at %s\n",
		result.Program, result.Item, result.GrantedBy, result.GrantedAt)
	fmt.Fprintf(out, "Capacity: %d/%d open, %d reserved, %d available\n",
		result.Capacity.Open, result.Capacity.Limit, result.Capacity.Reserved, result.Capacity.Available)
	fmt.Fprintf(out, "Inbox message: %s\n", result.MessageID)
	fmt.Fprintf(out, "Worker notification: %s\n", result.WorkerNotification)
	for _, warning := range result.Warnings {
		fmt.Fprintf(out, "Warning: %s\n", strings.TrimSpace(warning))
	}
	return nil
}
