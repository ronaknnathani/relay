package agent

// AutomatedTurnEnvVar marks an unattended Relay process so governance
// commands can fail closed. Patrol does not set this marker.
const AutomatedTurnEnvVar = "RELAY_AUTOMATED_TURN"

// AutomatedTurnSessionEnvVar identifies an unattended Relay process for
// durable attribution. Patrol does not create or identify agent sessions.
const AutomatedTurnSessionEnvVar = "RELAY_AUTOMATED_TURN_SESSION_ID"
