package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/mailbox"
	"github.com/ronaknnathani/relay/internal/program"
)

// automatedTurn reports whether the current process is a bounded automated
// Relay turn. relay program cto turn sets the marker for the agent it runs.
var automatedTurn = func() bool {
	return os.Getenv(agent.AutomatedTurnEnvVar) == "1"
}

// automatedTurnSession returns the fresh agent session id of the current
// bounded automated turn, which relay program cto turn exports alongside the
// marker so every durable action can name the turn that made it.
var automatedTurnSession = func() string {
	return os.Getenv(agent.AutomatedTurnSessionEnvVar)
}

const (
	// automatedActorPrefix prefixes every program actor a bounded automated
	// turn writes, so no durable record can be read as a human CTO or CEO.
	automatedActorPrefix = "cto-automated:"
	// automatedSessionPrefixLen keeps the durable identity short and quotable
	// while staying unique enough to find the turn's transcript.
	automatedSessionPrefixLen = 8
	// unknownAutomatedSession names an automated turn that somehow lost its
	// session id. It still reads as automated, which is what must never be lost.
	unknownAutomatedSession = "unknown"
)

// requireCEOTurn fails closed for mutations that only the CEO may authorize.
// A bounded automated turn can surface a recommendation as a decision, but it
// can never approve, reject, resolve, or end a program on the CEO's behalf.
func requireCEOTurn(command string) error {
	if !automatedTurn() {
		return nil
	}
	return fmt.Errorf(
		"%s is a CEO-only decision and is blocked in a bounded automated turn (%s=1); "+
			"record the recommendation with `relay program decision open` and let the CEO run it "+
			"from the interactive CTO session",
		command, agent.AutomatedTurnEnvVar,
	)
}

// requirePlanShapingTurn blocks work-item scope changes inside a bounded
// automated turn. Adding, re-scoping, or canceling work is a CEO-facing
// conversation, not routine governance an unattended turn should perform.
// Repairs that only change status (`item block`, `item unblock`, `item link`)
// stay allowed because `program tick` prints them as its own next action.
func requirePlanShapingTurn(command string) error {
	if !automatedTurn() {
		return nil
	}
	return fmt.Errorf(
		"%s reshapes the program plan and is blocked in a bounded automated turn (%s=1); "+
			"record the proposal with `relay program decision open` and let the CEO shape the plan "+
			"in the interactive CTO session",
		command, agent.AutomatedTurnEnvVar,
	)
}

// automatedSessionPrefix returns the short, stable prefix of the current
// bounded turn's session id. Only a prefix is ever published: it is enough to
// find the turn's transcript and never exposes the full session identifier.
func automatedSessionPrefix() string {
	var prefix strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(automatedTurnSession())) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			prefix.WriteRune(r)
		}
		if prefix.Len() >= automatedSessionPrefixLen {
			break
		}
	}
	if prefix.Len() == 0 {
		return unknownAutomatedSession
	}
	return prefix.String()
}

// automatedActor returns the forced program identity of the current process.
// It is the single source of automated attribution: no flag, prompt, or agent
// instruction can change it, so a bounded turn can never sign as `cto` or `ceo`.
func automatedActor() (string, bool) {
	if !automatedTurn() {
		return "", false
	}
	return automatedActorPrefix + automatedSessionPrefix(), true
}

// programActor forces the automated identity over whatever actor the caller
// requested. Outside a bounded automated turn the requested actor is used
// unchanged, so human CTO and CEO commands keep their existing behavior.
func programActor(requested string) string {
	if actor, ok := automatedActor(); ok {
		return actor
	}
	return requested
}

// attributeProgramEntry appends human-readable automated attribution to a
// durable progress, decision, or mailbox entry. Outside a bounded automated
// turn the text is returned unchanged. An empty or whitespace-only entry is
// also returned unchanged: attribution is metadata, not content, and appending
// it would fabricate a body and carry it past mailbox validation.
func attributeProgramEntry(text string) string {
	if !automatedTurn() || strings.TrimSpace(text) == "" {
		return text
	}
	return fmt.Sprintf(
		"%s [automated CTO turn %s, on behalf of CEO]",
		strings.TrimRight(text, " "), automatedSessionPrefix(),
	)
}

// sendProgramMail writes one durable mailbox message, stamping a bounded
// automated turn with its forced identity and the same human-readable
// attribution the progress and decision logs carry.
func sendProgramMail(projectDir string, box mailbox.Box, message mailbox.Message) (mailbox.Message, error) {
	if actor, ok := automatedActor(); ok {
		message.AutomatedBy = actor
		message.Body = attributeProgramEntry(message.Body)
	}
	return mailbox.Send(projectDir, box, message)
}

// appendProgramProgress writes one durable progress entry, attributed to the
// bounded automated turn that produced it when there is one.
func appendProgramProgress(programDir, message string) error {
	return program.AppendProgress(program.ProgressPath(programDir), attributeProgramEntry(message))
}

// appendProgramDecisionLog writes one durable decision entry, attributed to the
// bounded automated turn that produced it when there is one.
func appendProgramDecisionLog(programDir, message string) error {
	return program.AppendDecisionLog(program.DecisionLogPath(programDir), attributeProgramEntry(message))
}
