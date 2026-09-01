"use strict";

/* Relay Program UI.
   Read-only. Every value reaches the DOM through textContent or createElement;
   no markup is ever parsed from data. This file loads in <head> so the stored
   theme is applied before the first paint; the app itself boots on DOMContentLoaded. */

const THEME_KEY = "relay.program.theme";
const THEMES = ["light", "dark"];

/* Light is the default for everyone, regardless of the operating system
   preference, so the brief always opens as paper. */
function storedTheme() {
  try {
    const value = window.localStorage.getItem(THEME_KEY);
    return THEMES.indexOf(value) === -1 ? "" : value;
  } catch (error) {
    return "";
  }
}

function applyTheme(theme) {
  document.documentElement.dataset.theme = THEMES.indexOf(theme) === -1 ? "light" : theme;
}

applyTheme(storedTheme() || "light");

const POLL_INTERVAL = 3000;
const BACKOFF = [3000, 6000, 12000];
const LANES = ["pending", "dispatched", "in-review", "blocked", "merged", "cancelled"];
const TABS = ["roadmap", "tasks", "decisions", "goal"];
const ACTIVE_STATUSES = ["dispatched", "in-review"];

/* Work item IDs must match what /api/program accepts, or a stale hash makes
   every poll fail with 400 and the view never recovers. Keep in sync with
   normalizeDetailItem in cache.go. */
const ITEM_ID = /^w[1-9][0-9]*$/;
const MAX_ITEM_ID = 32;

const STATUS_META = {
  pending: { glyph: "○", word: "Pending" },
  dispatched: { glyph: "▶", word: "Dispatched" },
  "in-review": { glyph: "◆", word: "In review" },
  blocked: { glyph: "✕", word: "Blocked" },
  merged: { glyph: "●", word: "Merged" },
  cancelled: { glyph: "⊘", word: "Cancelled" },
};

const WORKER_META = {
  idle: { glyph: "○", word: "Idle", lane: "pending" },
  working: { glyph: "▶", word: "Working", lane: "dispatched" },
  blocked: { glyph: "✕", word: "Blocked", lane: "blocked" },
  done: { glyph: "●", word: "Done", lane: "merged" },
  unknown: { glyph: "?", word: "Unknown", lane: "pending" },
};

const VERDICT_GOOD = ["passing", "success", "approved", "mergeable", "clean"];
const VERDICT_BAD = ["failing", "failure", "error", "changes_requested", "conflicting", "dirty", "blocked"];

const ARTIFACT_HINTS = {
  "assignment.md": "Relay writes the assignment when the task is dispatched.",
  "task.md": "The worker copies the assignment into task.md when it starts.",
  "context.md": "The worker records repository context during the first phase.",
  "requirements.md": "Appears when the worker finishes the requirements phase.",
  "clarify.md": "Appears when the worker finishes the clarify phase.",
  "plan.md": "Appears when the worker finishes the plan phase.",
  "notes.md": "The worker appends notes while it implements.",
  "todos.md": "The worker keeps its own checklist here while implementing.",
  "progress.md": "The worker updates progress as phases complete.",
  "tradeoffs.md": "Appears when the worker records a design tradeoff.",
  "questions.md": "Appears when the worker has something it cannot decide alone.",
  "follow-ups.md": "Appears when the worker defers work out of this task.",
  "review.md": "Appears when the worker finishes the review phase.",
  "validation.md": "Appears when the worker finishes the validate phase.",
  "pr-body.md": "Appears when the worker drafts the pull request body.",
  "goal.md": "The program goal has not been written yet.",
  "decisions.md": "No decisions have been written to this file yet.",
};

const dom = {};

const state = {
  snapshot: null,
  signature: "",
  tab: "roadmap",
  selected: "",
  filter: "",
  statuses: new Set(),
  programFile: "goal.md",
  artifactByItem: new Map(),
  cards: new Map(),
  drawerOpen: false,
  drawerReturn: null,
  detailItem: "",
  detailSection: "",
  drawerTimer: null,
  pendingDrawer: false,
  copyTimer: null,
  failures: 0,
  live: false,
};

/* ---------- DOM helpers ---------- */

function collectDom() {
  const byID = (id) => document.querySelector(`#${id}`);
  dom.title = byID("program-title");
  dom.summary = byID("program-summary");
  dom.slug = byID("program-slug");
  dom.programState = byID("program-state");
  dom.updated = byID("program-updated");
  dom.repo = byID("program-repo");
  dom.agent = byID("program-agent");
  dom.feedState = byID("feed-state");
  dom.warningCount = byID("warning-count");
  dom.themeToggle = byID("theme-toggle");
  dom.themeGlyph = byID("theme-glyph");
  dom.themeText = byID("theme-text");
  dom.refresh = byID("refresh");
  dom.percent = byID("progress-percent");
  dom.progressCounts = byID("progress-counts");
  dom.taskTotal = byID("task-total");
  dom.taskBreakdown = byID("task-breakdown");
  dom.capacity = byID("capacity-readout");
  dom.capacityNote = byID("capacity-note");
  dom.decisionCount = byID("decision-count");
  dom.decisionNote = byID("decision-note");
  dom.workerCount = byID("worker-count");
  dom.workerNote = byID("worker-note");
  dom.patrolStatus = byID("patrol-status");
  dom.patrolNote = byID("patrol-note");
  dom.patrolTurn = byID("patrol-turn");
  dom.nextAction = byID("next-action");
  dom.nextRun = byID("next-run");
  dom.nextCommand = byID("next-command");
  dom.copyCommand = byID("copy-command");
  dom.reconnect = byID("reconnect");
  dom.tablist = document.querySelector('[role="tablist"]');
  dom.tabs = Array.from(document.querySelectorAll('[role="tab"]'));
  dom.panels = new Map(TABS.map((tab) => [tab, byID(`panel-${tab}`)]));
  dom.roadmapNote = byID("roadmap-note");
  dom.roadmapCycle = byID("roadmap-cycle");
  dom.roadmapScroll = byID("roadmap-scroll");
  dom.roadmapContent = byID("roadmap-content");
  dom.roadmapEmpty = byID("roadmap-empty");
  dom.graph = byID("graph");
  dom.graphEdges = byID("graph-edges");
  dom.graphNodes = byID("graph-nodes");
  dom.filter = byID("filter");
  dom.statusFilters = byID("status-filters");
  dom.ledgerCount = byID("ledger-count");
  dom.ledgerRows = byID("ledger-rows");
  dom.ledgerEmpty = byID("ledger-empty");
  dom.tableScroll = document.querySelector(".table-scroll");
  dom.decisionsNote = byID("decisions-note");
  dom.openDecisions = byID("open-decisions");
  dom.resolvedWrap = byID("resolved-wrap");
  dom.resolvedSummary = byID("resolved-summary");
  dom.resolvedDecisions = byID("resolved-decisions");
  dom.goalNav = byID("goal-nav");
  dom.goalBody = byID("goal-body");
  dom.goalMeta = byID("goal-meta");
  dom.contractsWrap = byID("contracts-wrap");
  dom.contractsSummary = byID("contracts-summary");
  dom.contracts = byID("contracts");
  dom.diagnostics = byID("diagnostics");
  dom.diagnosticsSummary = byID("diagnostics-summary");
  dom.warnings = byID("warnings");
  dom.drawer = byID("drawer");
  dom.drawerScrim = byID("drawer-scrim");
  dom.drawerPanel = byID("detail-panel");
  dom.drawerID = byID("drawer-id");
  dom.drawerTitle = byID("drawer-title");
  dom.drawerMeta = byID("drawer-meta");
  dom.drawerClose = byID("drawer-close");
  dom.drawerScroll = byID("drawer-scroll");
  dom.drawerNav = byID("drawer-nav");
  dom.detailBody = byID("detail-body");
  dom.schemaVersion = byID("schema-version");
  dom.svgNamespace = dom.graph.namespaceURI;
}

function make(tag, className, content) {
  const node = document.createElement(tag);
  if (className) {
    node.className = className;
  }
  if (content !== undefined && content !== null) {
    node.textContent = String(content);
  }
  return node;
}

function svg(tag, className) {
  const node = document.createElementNS(dom.svgNamespace, tag);
  if (className) {
    node.setAttribute("class", className);
  }
  return node;
}

function text(value, fallback) {
  const trimmed = typeof value === "string" ? value.trim() : "";
  if (trimmed) {
    return trimmed;
  }
  return fallback === undefined ? "" : fallback;
}

function list(value) {
  return Array.isArray(value) ? value : [];
}

function count(value) {
  return Number(value) || 0;
}

function emptyNote(message) {
  return make("p", "empty", message);
}

function setText(node, value) {
  node.textContent = value;
  node.hidden = !value;
}

/* announce writes to an aria-live region only when the message actually
   changes, so a 3s poll that reports the same thing does not make screen
   readers repeat it forever. */
function announce(node, message) {
  if (node.textContent === message) {
    return;
  }
  node.textContent = message;
}

/* ---------- formatting ---------- */

function formatTimestamp(value) {
  const raw = text(value);
  if (!raw) {
    return "";
  }
  const parsed = new Date(raw);
  if (Number.isNaN(parsed.getTime())) {
    return raw;
  }
  return parsed.toLocaleString([], {
    month: "short", day: "2-digit", hour: "2-digit", minute: "2-digit",
  });
}

function formatRelative(value) {
  const raw = text(value);
  if (!raw) {
    return "";
  }
  const parsed = new Date(raw);
  if (Number.isNaN(parsed.getTime())) {
    return raw;
  }
  const seconds = Math.round((Date.now() - parsed.getTime()) / 1000);
  if (seconds < 0) {
    return "just now";
  }
  if (seconds < 45) {
    return `${seconds}s ago`;
  }
  if (seconds < 3600) {
    return `${Math.round(seconds / 60)}m ago`;
  }
  if (seconds < 86400) {
    return `${Math.round(seconds / 3600)}h ago`;
  }
  return `${Math.round(seconds / 86400)}d ago`;
}

function formatSize(bytes) {
  const size = count(bytes);
  if (size < 1024) {
    return `${size} B`;
  }
  if (size < 1024 * 1024) {
    return `${(size / 1024).toFixed(1)} KB`;
  }
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}

function actorStamp(when, who) {
  const moment = formatTimestamp(when);
  if (!moment) {
    return "";
  }
  const actor = text(who);
  return actor ? `${moment} by ${actor}` : moment;
}

function humanize(value) {
  const raw = text(value);
  if (!raw) {
    return "";
  }
  const words = raw.replace(/[_-]+/g, " ").toLowerCase();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

function truncate(value, limit) {
  const raw = text(value);
  if (raw.length <= limit) {
    return raw;
  }
  return `${raw.slice(0, Math.max(1, limit - 1))}…`;
}

function plural(value, word) {
  return `${value} ${word}${value === 1 ? "" : "s"}`;
}

function formatPatrolCadence(seconds) {
  if (seconds <= 0) {
    return "";
  }
  return `${Math.round(seconds / 60)}m cadence`;
}

/* ---------- status vocabulary ---------- */

function statusMeta(status) {
  return STATUS_META[text(status)] || { glyph: "·", word: humanize(status) || "Unknown" };
}

function statusNode(status) {
  const lane = text(status);
  const meta = statusMeta(lane);
  const wrapper = make("span", "status");
  wrapper.dataset.lane = lane;
  const glyph = make("span", "status__glyph", meta.glyph);
  glyph.setAttribute("aria-hidden", "true");
  wrapper.append(glyph, make("span", "status__word", meta.word));
  return wrapper;
}

function workerNode(worker) {
  const meta = WORKER_META[text(worker.status)] || WORKER_META.unknown;
  const badge = make("span", "status");
  badge.dataset.lane = meta.lane;
  const glyph = make("span", "status__glyph", meta.glyph);
  glyph.setAttribute("aria-hidden", "true");
  badge.append(glyph, make("span", "", meta.word));
  return badge;
}

function verdictNode(value) {
  const raw = text(value);
  if (!raw) {
    return make("span", "muted", "—");
  }
  const key = raw.toLowerCase();
  let lane = "dispatched";
  let glyph = "○";
  if (VERDICT_GOOD.indexOf(key) !== -1) {
    lane = "merged";
    glyph = "●";
  } else if (VERDICT_BAD.indexOf(key) !== -1) {
    lane = "blocked";
    glyph = "✕";
  }
  const wrapper = make("span", "status");
  wrapper.dataset.lane = lane;
  const mark = make("span", "status__glyph", glyph);
  mark.setAttribute("aria-hidden", "true");
  wrapper.append(mark, make("span", "", humanize(raw)));
  return wrapper;
}

/* ---------- markdown to DOM ---------- */

const BULLET = /^\s*[-*+]\s+/;
const NUMBERED = /^\s*\d+[.)]\s+/;
const HEADING = /^(#{1,6})\s+(.*)$/;
const RULE = /^\s*(-{3,}|\*{3,}|_{3,})\s*$/;

function isBlockStart(line) {
  return HEADING.test(line) || RULE.test(line) || BULLET.test(line) ||
    NUMBERED.test(line) || line.trim().startsWith("```");
}

function appendInline(node, value) {
  const raw = String(value === undefined || value === null ? "" : value);
  const parts = raw.split("`");
  if (parts.length % 2 === 0) {
    node.append(document.createTextNode(raw));
    return;
  }
  parts.forEach((part, index) => {
    if (!part) {
      return;
    }
    if (index % 2 === 1) {
      node.append(make("code", "", part));
      return;
    }
    node.append(document.createTextNode(part));
  });
}

function renderMarkdown(container, source) {
  container.replaceChildren();
  const lines = String(source === undefined || source === null ? "" : source)
    .replace(/\r\n?/g, "\n")
    .split("\n");
  let index = 0;

  while (index < lines.length) {
    const line = lines[index];
    if (!line.trim()) {
      index += 1;
      continue;
    }
    if (line.trim().startsWith("```")) {
      const block = make("pre");
      const code = make("code");
      const body = [];
      index += 1;
      while (index < lines.length && !lines[index].trim().startsWith("```")) {
        body.push(lines[index]);
        index += 1;
      }
      if (index < lines.length) {
        index += 1;
      }
      code.textContent = body.join("\n");
      block.append(code);
      container.append(block);
      continue;
    }
    const heading = HEADING.exec(line);
    if (heading) {
      const level = Math.min(6, heading[1].length + 2);
      const node = make(`h${level}`);
      appendInline(node, heading[2]);
      container.append(node);
      index += 1;
      continue;
    }
    if (RULE.test(line)) {
      container.append(make("hr"));
      index += 1;
      continue;
    }
    if (BULLET.test(line) || NUMBERED.test(line)) {
      const ordered = !BULLET.test(line);
      const matcher = ordered ? NUMBERED : BULLET;
      const collection = make(ordered ? "ol" : "ul");
      while (index < lines.length && matcher.test(lines[index])) {
        const item = make("li");
        appendInline(item, lines[index].replace(matcher, ""));
        collection.append(item);
        index += 1;
      }
      container.append(collection);
      continue;
    }
    const paragraph = make("p");
    const buffer = [];
    while (index < lines.length && lines[index].trim() && !isBlockStart(lines[index])) {
      buffer.push(lines[index].trim());
      index += 1;
    }
    appendInline(paragraph, buffer.join(" "));
    container.append(paragraph);
  }
}

/* ---------- snapshot accessors ---------- */

function snapshotOf() {
  return state.snapshot || {};
}

function programOf() {
  return snapshotOf().program || {};
}

function planOf() {
  return snapshotOf().plan || {};
}

function items() {
  return list(snapshotOf().items);
}

function itemByID(id) {
  return items().find((item) => item.id === id) || null;
}

function selectedItem() {
  return state.selected ? itemByID(state.selected) : null;
}

function programArtifact(name) {
  return list(snapshotOf().program_artifacts).find((artifact) => artifact.name === name) || null;
}

/* ---------- theme ---------- */

function currentTheme() {
  return document.documentElement.dataset.theme === "dark" ? "dark" : "light";
}

function renderThemeToggle() {
  const dark = currentTheme() === "dark";
  dom.themeGlyph.textContent = dark ? "☀" : "☾";
  dom.themeText.textContent = dark ? "Light" : "Dark";
  dom.themeToggle.setAttribute("aria-label", dark ? "Switch to light theme" : "Switch to dark theme");
}

function toggleTheme() {
  const next = currentTheme() === "dark" ? "light" : "dark";
  applyTheme(next);
  try {
    window.localStorage.setItem(THEME_KEY, next);
  } catch (error) {
    /* Private browsing can refuse storage; the choice still applies for this session. */
  }
  renderThemeToggle();
}

/* ---------- header ---------- */

function renderHeader() {
  const program = programOf();
  const displayTitle = text(program.display_title) ||
    truncate(text(program.title, "Untitled program"), 72);

  document.title = `${displayTitle} · Relay`;
  dom.title.textContent = displayTitle;
  dom.summary.textContent = text(program.summary);
  dom.slug.textContent = text(program.slug, "program");

  const programState = text(program.state, "unknown");
  dom.programState.replaceChildren(
    stateGlyph(programState),
    make("span", "", humanize(programState)),
  );
  dom.programState.dataset.lane = stateLane(programState);
  setText(dom.updated, program.updated_at ? `updated ${formatRelative(program.updated_at)}` : "");
  setText(dom.repo, text(program.repo));
  setText(dom.agent, program.agent ? `agent ${program.agent}` : "");
  if (program.archived) {
    dom.slug.textContent = `${text(program.slug, "program")} (archived)`;
  }

  renderOverview();
  renderNextAction();
  renderWarningCount();
  if (snapshotOf().schema) {
    dom.schemaVersion.textContent = snapshotOf().schema;
  }
}

function stateLane(programState) {
  switch (programState) {
    case "active":
      return "dispatched";
    case "completed":
      return "merged";
    case "held":
    case "draft":
      return "in-review";
    case "abandoned":
      return "blocked";
    default:
      return "pending";
  }
}

function stateGlyph(programState) {
  const glyph = make("span", "status__glyph", statusMeta(stateLane(programState)).glyph);
  glyph.setAttribute("aria-hidden", "true");
  return glyph;
}

function renderOverview() {
  const progress = snapshotOf().progress || {};
  const capacity = planOf().capacity || {};

  const percent = Math.max(0, Math.min(100, count(progress.percent)));
  dom.percent.textContent = String(percent);
  dom.progressCounts.textContent = `${count(progress.merged)} of ${count(progress.total)} merged`;

  dom.taskTotal.textContent = String(count(progress.total));
  dom.taskBreakdown.textContent = [
    count(progress.dispatched) ? `${count(progress.dispatched)} dispatched` : "",
    count(progress.in_review) ? `${count(progress.in_review)} in review` : "",
    count(progress.blocked) ? `${count(progress.blocked)} blocked` : "",
    count(progress.pending) ? `${count(progress.pending)} pending` : "",
  ].filter(Boolean).join(" · ") || "Nothing in flight";

  dom.capacity.textContent = `${count(capacity.open)} / ${count(capacity.limit)}`;
  dom.capacityNote.textContent = [
    `${plural(count(capacity.available), "slot")} available`,
    count(capacity.reserved) ? `${count(capacity.reserved)} reserved` : "",
  ].filter(Boolean).join(" · ");

  const open = list(snapshotOf().open_decisions).length;
  dom.decisionCount.textContent = String(open);
  dom.decisionNote.textContent = open === 0
    ? "Nothing waiting on you"
    : `${plural(open, "answer")} needed`;

  const workers = items().filter((item) => item.worker);
  const active = workers.filter((item) => text(item.worker.status) === "working").length;
  let unread = 0;
  items().forEach((item) => {
    const mailbox = item.mailbox || {};
    if (mailbox.available) {
      unread += count(mailbox.inbox) + count(mailbox.outbox);
    }
  });
  dom.workerCount.textContent = String(workers.length);
  dom.workerNote.textContent = [
    `${active} working`,
    unread ? `${plural(unread, "unread message")}` : "no unread mail",
  ].join(" · ");

  const patrol = snapshotOf().patrol || {};
  dom.patrolStatus.textContent = humanize(text(patrol.status, "not-running"));
  dom.patrolNote.textContent = [
    formatPatrolCadence(count(patrol.delay_seconds)),
    patrol.next_tick_at ? `next ${formatTimestamp(patrol.next_tick_at)}` : "",
    patrol.running ? (patrol.tl_present ? "TL present" : "TL unavailable") : "",
    patrol.doorbell_suppressed ? "TL wakes suppressed" : "",
  ].filter(Boolean).join(" · ") || "Start with relay program patrol";
  dom.patrolTurn.textContent = patrolTurnNote(patrol.turn || {});
}

/* Older patrol state may retain bounded-turn log metadata. New patrols report
   only whether the live tech lead doorbell was confirmed. */
function patrolTurnNote(turn) {
  const status = text(turn.status);
  if (!status) {
    return "";
  }
  const parts = [`last TL wake ${humanize(status)}`];
  if (turn.ended_at) {
    parts.push(formatTimestamp(turn.ended_at));
  }
  if (count(turn.failures) > 0) {
    parts.push(`${plural(count(turn.failures), "consecutive failure")}`);
  }
  if (turn.log_path) {
    parts.push(`log ${turn.log_path}`);
  }
  return parts.join(" · ");
}

function renderNextAction() {
  const plan = planOf();
  dom.nextAction.textContent = text(plan.next_action, "Nothing to do right now.");
  const command = text(plan.next_command);
  dom.nextRun.hidden = !command;
  dom.nextCommand.textContent = command;
}

function copyCommand() {
  const command = text(planOf().next_command);
  if (!command) {
    return;
  }
  const clipboard = window.navigator.clipboard;
  if (!clipboard || typeof clipboard.writeText !== "function") {
    markCopy("Select to copy");
    return;
  }
  clipboard.writeText(command).then(
    () => markCopy("Copied"),
    () => markCopy("Copy failed"),
  );
}

function markCopy(label) {
  dom.copyCommand.textContent = label;
  if (state.copyTimer !== null) {
    clearTimeout(state.copyTimer);
  }
  state.copyTimer = setTimeout(() => {
    state.copyTimer = null;
    dom.copyCommand.textContent = "Copy";
  }, 1400);
}

function setFeed(live, message) {
  state.live = live;
  dom.feedState.dataset.live = live ? "true" : "false";
  announce(dom.feedState, message);
}

function showReconnect(message) {
  announce(dom.reconnect, message);
  dom.reconnect.hidden = false;
}

function hideReconnect() {
  if (dom.reconnect.hidden) {
    return;
  }
  dom.reconnect.hidden = true;
  announce(dom.reconnect, "");
}

/* ---------- warnings and diagnostics ---------- */

function patrolReasons() {
  const patrol = snapshotOf().patrol || {};
  const reasons = list(patrol.reasons).map((reason) => {
    const code = text(reason && reason.code);
    const message = text(reason && reason.text);
    return code && message ? `${code}: ${message}` : message || code;
  }).filter(Boolean);
  if (text(patrol.error)) {
    reasons.push(`error: ${patrol.error}`);
  }
  if (text(patrol.warning)) {
    reasons.push(`warning: ${patrol.warning}`);
  }
  const turn = patrol.turn || {};
  if (text(turn.error)) {
    reasons.push(`last turn error: ${turn.error}`);
  }
  return reasons;
}

function warningGroups() {
  const snapshot = snapshotOf();
  const health = snapshot.source_health || {};
  /* Sources come first: the program list repeats every source warning, so
     leading with it would swallow the attribution and leave the source
     groups empty. Program keeps only what no source claimed. */
  const raw = [
    ["Child projects", list(health.projects && health.projects.warnings)],
    ["GitHub", list(health.github && health.github.warnings)],
    ["Herdr", list(health.herdr && health.herdr.warnings)],
    ["Mailbox", list(health.mailbox && health.mailbox.warnings)],
    ["Patrol source", list(health.patrol && health.patrol.warnings)],
    ["Patrol", patrolReasons()],
    ["Program", list(snapshot.warnings)],
  ];
  const seen = new Set();
  const groups = [];
  raw.forEach(([source, entries]) => {
    const unique = [];
    entries.forEach((entry) => {
      const message = text(entry);
      if (!message || seen.has(message)) {
        return;
      }
      seen.add(message);
      unique.push(message);
    });
    if (unique.length > 0) {
      groups.push([source, unique]);
    }
  });
  return groups;
}

function renderWarnings() {
  const groups = warningGroups();
  dom.warnings.replaceChildren();
  if (groups.length === 0) {
    dom.warnings.append(emptyNote("No warnings. Every source answered cleanly."));
    dom.diagnosticsSummary.textContent = "Diagnostics";
    return;
  }
  let total = 0;
  groups.forEach(([source, entries]) => {
    total += entries.length;
    const group = make("div", "warning-group");
    group.append(make("h3", "", source));
    const listing = make("ul", "warning-list");
    entries.forEach((entry) => listing.append(make("li", "", entry)));
    group.append(listing);
    dom.warnings.append(group);
  });
  dom.diagnosticsSummary.textContent = `Diagnostics (${total})`;
}

function renderWarningCount() {
  const total = warningGroups().reduce((sum, group) => sum + group[1].length, 0);
  dom.warningCount.hidden = total === 0;
  dom.warningCount.textContent = plural(total, "warning");
}

/* ---------- tabs ---------- */

function selectTab(name, options) {
  const tab = TABS.indexOf(name) === -1 ? "roadmap" : name;
  state.tab = tab;
  dom.tabs.forEach((button) => {
    const active = button.dataset.tab === tab;
    button.setAttribute("aria-selected", active ? "true" : "false");
    button.setAttribute("tabindex", active ? "0" : "-1");
  });
  dom.panels.forEach((panel, key) => {
    panel.hidden = key !== tab;
  });
  writeHash();
  if (tab === "roadmap") {
    renderRoadmap();
  }
  if (options && options.focus) {
    const button = dom.tabs.find((entry) => entry.dataset.tab === tab);
    if (button) {
      button.focus();
    }
  }
}

function onTabKey(event) {
  const current = dom.tabs.findIndex((button) => button.dataset.tab === state.tab);
  let next = -1;
  if (event.key === "ArrowRight") {
    next = (current + 1) % dom.tabs.length;
  } else if (event.key === "ArrowLeft") {
    next = (current - 1 + dom.tabs.length) % dom.tabs.length;
  } else if (event.key === "Home") {
    next = 0;
  } else if (event.key === "End") {
    next = dom.tabs.length - 1;
  }
  if (next === -1) {
    return;
  }
  event.preventDefault();
  selectTab(dom.tabs[next].dataset.tab, { focus: true });
}

/* ---------- roadmap ---------- */

function relatedSet(id) {
  const related = new Set();
  if (!id) {
    return related;
  }
  const byID = new Map(items().map((item) => [item.id, item]));
  const walk = (start, key) => {
    const queue = [start];
    while (queue.length > 0) {
      const current = queue.shift();
      const item = byID.get(current);
      if (!item) {
        continue;
      }
      list(item[key]).forEach((next) => {
        if (!related.has(next)) {
          related.add(next);
          queue.push(next);
        }
      });
    }
  };
  related.add(id);
  walk(id, "dependencies");
  walk(id, "dependents");
  return related;
}

function renderRoadmap() {
  const graph = snapshotOf().graph || {};
  const nodes = list(graph.nodes);
  const plan = planOf();

  dom.roadmapNote.textContent = [
    `${list(plan.ready).length} ready`,
    `${list(plan.in_flight).length} in flight`,
    `${list(plan.blocked).length} blocked`,
    list(plan.orphaned).length ? `${list(plan.orphaned).length} orphaned` : "",
  ].filter(Boolean).join(" · ");

  dom.roadmapCycle.hidden = !graph.cyclic;
  if (graph.cyclic) {
    dom.roadmapCycle.textContent =
      "This program has a dependency cycle. Cyclic links are drawn dashed and stage order is approximate.";
  }

  state.cards.clear();
  dom.graphNodes.replaceChildren();
  dom.graphEdges.replaceChildren();

  if (nodes.length === 0) {
    dom.roadmapEmpty.hidden = false;
    dom.roadmapEmpty.textContent = state.snapshot
      ? "No work items yet. The tech lead adds tasks when the program is planned."
      : "Loading the program…";
    dom.roadmapScroll.hidden = true;
    dom.graph.setAttribute("aria-label", "Dependency flow: no work items yet.");
    return;
  }
  dom.roadmapEmpty.hidden = true;
  dom.roadmapScroll.hidden = false;
  dom.graph.setAttribute("aria-label", roadmapLabel(nodes, list(graph.edges)));

  const stages = stageLists(graph, nodes);
  const hasSelection = Boolean(selectedItem());
  let position = 0;
  stages.forEach((ids, index) => {
    const stage = make("div", "stage");
    stage.dataset.stage = String(index);
    stage.append(make("p", "stage__label", `Stage ${index + 1} · ${plural(ids.length, "task")}`));
    const row = make("div", "stage__row");
    ids.forEach((id) => {
      const node = nodes.find((entry) => entry.id === id) || { id, title: "", lane: "" };
      const card = taskCard(node, position, hasSelection);
      card.dataset.stage = String(index);
      position += 1;
      state.cards.set(id, card);
      row.append(card);
    });
    stage.append(row);
    dom.graphNodes.append(stage);
  });

  drawConnectors(list(graph.edges));
}

function stageLists(graph, nodes) {
  const layers = list(graph.layers).map((layer) => list(layer).slice());
  const placed = new Set();
  layers.forEach((layer) => layer.forEach((id) => placed.add(id)));
  const loose = nodes.filter((node) => !placed.has(node.id)).map((node) => node.id);
  if (loose.length > 0) {
    if (layers.length === 0) {
      layers.push(loose);
    } else {
      layers[0] = layers[0].concat(loose);
    }
  }
  return layers.filter((layer) => layer.length > 0);
}

function roadmapLabel(nodes, edges) {
  const counts = new Map();
  nodes.forEach((node) => {
    const lane = text(node.lane, "unknown");
    counts.set(lane, (counts.get(lane) || 0) + 1);
  });
  const breakdown = LANES
    .filter((lane) => counts.has(lane))
    .map((lane) => `${counts.get(lane)} ${statusMeta(lane).word.toLowerCase()}`)
    .join(", ");
  return `Dependency flow: ${plural(nodes.length, "task")}, ${plural(edges.length, "dependency link")}. ` +
    `${breakdown}. The Tasks tab carries the same information as a table.`;
}

function taskCard(node, position, hasSelection) {
  const item = itemByID(node.id);
  const lane = text(node.lane, item ? text(item.status) : "pending");
  const card = make("button", "card");
  card.type = "button";
  card.dataset.lane = lane;
  card.dataset.item = node.id;
  card.dataset.focusKey = `card:${node.id}`;
  card.dataset.selected = node.id === state.selected ? "true" : "false";
  card.setAttribute("tabindex", hasSelection
    ? (node.id === state.selected ? "0" : "-1")
    : (position === 0 ? "0" : "-1"));
  card.setAttribute("aria-label", cardLabel(node, item, lane));

  const top = make("div", "card__top");
  top.append(make("span", "card__id", node.id), statusNode(lane));
  card.append(top);
  card.append(make("p", "card__title", text(node.title, "Untitled task")));

  const foot = make("div", "card__foot");
  if (item) {
    foot.append(make("span", "", text(item.priority, "P?")));
    const dependencies = list(item.dependencies).length;
    if (dependencies > 0) {
      foot.append(make("span", "", plural(dependencies, "dep")));
    }
    const pr = item.live_pr || item.recorded_pr;
    if (pr && pr.number) {
      foot.append(make("span", "card__pr", `PR #${pr.number}`));
    }
    if (item.orphaned) {
      foot.append(make("span", "flag flag--orphan", "orphan"));
    } else if (item.ready) {
      foot.append(make("span", "flag flag--ready", "ready"));
    }
  }
  card.append(foot);

  card.addEventListener("click", () => {
    selectItem(node.id);
    openDrawer(false);
  });
  card.addEventListener("keydown", (event) => onCardKey(event, node.id));
  return card;
}

function cardLabel(node, item, lane) {
  const parts = [`${node.id}, ${statusMeta(lane).word}`, text(node.title)];
  if (item) {
    parts.push(`priority ${text(item.priority, "unset")}`);
    const dependencies = list(item.dependencies);
    parts.push(dependencies.length ? `depends on ${dependencies.join(", ")}` : "no dependencies");
  }
  return parts.filter(Boolean).join(". ");
}

function onCardKey(event, id) {
  if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    selectItem(id);
    openDrawer(true);
    return;
  }
  const order = Array.from(state.cards.values());
  const current = order.findIndex((card) => card.dataset.item === id);
  let target = null;
  if (event.key === "ArrowRight") {
    target = order[Math.min(order.length - 1, current + 1)];
  } else if (event.key === "ArrowLeft") {
    target = order[Math.max(0, current - 1)];
  } else if (event.key === "ArrowDown") {
    target = cardInStage(order, current, 1);
  } else if (event.key === "ArrowUp") {
    target = cardInStage(order, current, -1);
  } else if (event.key === "Home") {
    target = order[0];
  } else if (event.key === "End") {
    target = order[order.length - 1];
  }
  if (!target) {
    return;
  }
  event.preventDefault();
  order.forEach((card) => card.setAttribute("tabindex", "-1"));
  target.setAttribute("tabindex", "0");
  target.focus({ preventScroll: true });
  selectItem(target.dataset.item);
  revealCard(target.dataset.item);
}

/* Up and down move between stages now that the roadmap runs vertically,
   landing on whichever card in that stage sits closest horizontally. */
function cardInStage(order, current, step) {
  const from = order[current];
  if (!from) {
    return null;
  }
  const stage = Number(from.dataset.stage) + step;
  const candidates = order.filter((card) => Number(card.dataset.stage) === stage);
  if (candidates.length === 0) {
    return null;
  }
  const anchor = from.getBoundingClientRect();
  const centre = anchor.left + anchor.width / 2;
  let best = candidates[0];
  let bestGap = Infinity;
  candidates.forEach((card) => {
    const box = card.getBoundingClientRect();
    const gap = Math.abs(box.left + box.width / 2 - centre);
    if (gap < bestGap) {
      bestGap = gap;
      best = card;
    }
  });
  return best;
}

function drawConnectors(edges) {
  const base = dom.roadmapContent.getBoundingClientRect();
  if (base.width === 0) {
    return;
  }
  const boxes = new Map();
  state.cards.forEach((card, id) => {
    const box = card.getBoundingClientRect();
    boxes.set(id, {
      center: box.left - base.left + box.width / 2,
      top: box.top - base.top,
      bottom: box.bottom - base.top,
      stage: Number(card.dataset.stage) || 0,
    });
  });
  const width = Math.ceil(base.width);
  const height = Math.ceil(base.height);
  dom.graph.setAttribute("width", String(width));
  dom.graph.setAttribute("height", String(height));
  dom.graph.setAttribute("viewBox", `0 0 ${width} ${height}`);

  const related = state.selected ? relatedSet(state.selected) : null;
  edges.forEach((edge) => {
    const from = boxes.get(edge.from);
    const to = boxes.get(edge.to);
    if (!from || !to) {
      return;
    }
    const downward = to.top > from.bottom + 4;
    const path = svg("path", downward ? "edge" : "edge edge--back");
    if (downward) {
      path.setAttribute("d", downwardPath(from, to));
    } else {
      path.setAttribute(
        "d",
        `M ${round(from.center)} ${round(from.bottom + 1)} L ${round(to.center)} ${round(to.top - 7)}`,
      );
    }
    const touched = Boolean(related && (edge.from === state.selected || edge.to === state.selected));
    if (touched && downward) {
      path.setAttribute("class", "edge edge--active");
    }
    path.setAttribute("marker-end", touched && downward ? "url(#flow-arrow-active)" : "url(#flow-arrow)");
    dom.graphEdges.append(path);
  });
}

function round(value) {
  return Math.round(value * 10) / 10;
}

/* downwardPath routes a dependency from the bottom of one card to the top of a
   later one: straight down, across a shared channel just above the target
   stage, then down again. Orthogonal with soft corners, drawn behind cards. */
function downwardPath(from, to) {
  const y1 = from.bottom + 1;
  const y2 = to.top - 7;
  /* The turn sits in the band directly above the target stage's cards, so
     every arrow into a stage lines up instead of wandering. */
  const channel = Math.max(y1 + 6, to.top - 13);
  const x1 = from.center;
  const x2 = to.center;
  if (Math.abs(x2 - x1) < 1.5) {
    return `M ${round(x1)} ${round(y1)} V ${round(y2)}`;
  }
  const direction = x2 > x1 ? 1 : -1;
  const radius = Math.max(0, Math.min(10, Math.abs(x2 - x1) / 2, channel - y1, y2 - channel));
  return `M ${round(x1)} ${round(y1)}` +
    ` V ${round(channel - radius)}` +
    ` Q ${round(x1)} ${round(channel)} ${round(x1 + direction * radius)} ${round(channel)}` +
    ` H ${round(x2 - direction * radius)}` +
    ` Q ${round(x2)} ${round(channel)} ${round(x2)} ${round(channel + radius)}` +
    ` V ${round(y2)}`;
}

function revealCard(id) {
  const card = state.cards.get(id);
  if (!card || dom.roadmapScroll.hidden) {
    return;
  }
  card.scrollIntoView({ block: "nearest", inline: "nearest" });
}

/* ---------- tasks ---------- */

function matchesFilter(item) {
  if (state.statuses.size > 0 && !state.statuses.has(text(item.status))) {
    return false;
  }
  const needle = state.filter.trim().toLowerCase();
  if (!needle) {
    return true;
  }
  const pr = item.live_pr || item.recorded_pr || {};
  const haystack = [
    item.id, item.title, item.status, item.priority, item.project_slug,
    pr.ref, pr.title, pr.number ? `#${pr.number}` : "",
    item.worker ? item.worker.terminal_title : "",
    item.worker ? item.worker.status : "",
    list(item.dependencies).join(" "),
  ].map((value) => String(value === undefined || value === null ? "" : value).toLowerCase());
  return haystack.some((value) => value.indexOf(needle) !== -1);
}

function visibleItems() {
  return items().filter(matchesFilter);
}

function buildStatusFilters() {
  dom.statusFilters.replaceChildren();
  const all = make("button", "chip");
  all.type = "button";
  all.dataset.lane = "all";
  all.append(make("span", "", "All"), make("span", "chip__count", ""));
  all.addEventListener("click", () => {
    state.statuses.clear();
    renderLedger();
    updateStatusFilters();
  });
  dom.statusFilters.append(all);

  LANES.forEach((lane) => {
    const meta = statusMeta(lane);
    const chip = make("button", "chip");
    chip.type = "button";
    chip.dataset.lane = lane;
    chip.append(make("span", "", meta.word), make("span", "chip__count", "0"));
    chip.addEventListener("click", () => {
      if (state.statuses.has(lane)) {
        state.statuses.delete(lane);
      } else {
        state.statuses.add(lane);
      }
      renderLedger();
      updateStatusFilters();
    });
    dom.statusFilters.append(chip);
  });
}

function updateStatusFilters() {
  const counts = new Map();
  items().forEach((item) => {
    const lane = text(item.status);
    counts.set(lane, (counts.get(lane) || 0) + 1);
  });
  Array.from(dom.statusFilters.querySelectorAll(".chip")).forEach((chip) => {
    const lane = chip.dataset.lane;
    const badge = chip.querySelector(".chip__count");
    if (lane === "all") {
      chip.setAttribute("aria-pressed", state.statuses.size === 0 ? "true" : "false");
      badge.textContent = String(items().length);
      return;
    }
    chip.setAttribute("aria-pressed", state.statuses.has(lane) ? "true" : "false");
    badge.textContent = String(counts.get(lane) || 0);
  });
}

function renderLedger() {
  const visible = visibleItems();
  const total = items().length;
  dom.ledgerRows.replaceChildren();
  announce(dom.ledgerCount, total === 0
    ? "No tasks"
    : `${visible.length} of ${plural(total, "task")}`);

  if (total === 0) {
    dom.ledgerEmpty.hidden = false;
    dom.ledgerEmpty.textContent = "This program has no work items yet.";
    return;
  }
  if (visible.length === 0) {
    dom.ledgerEmpty.hidden = false;
    dom.ledgerEmpty.textContent = "No tasks match this filter. Press Esc in the filter box to clear it.";
    return;
  }
  dom.ledgerEmpty.hidden = true;
  visible.forEach((item) => dom.ledgerRows.append(ledgerRow(item)));
}

function ledgerRow(item) {
  const row = make("tr");
  row.dataset.item = item.id;
  row.setAttribute("aria-selected", item.id === state.selected ? "true" : "false");
  row.addEventListener("click", () => {
    selectItem(item.id);
    openDrawer(false);
  });

  const identity = make("td", "cell-id");
  const button = make("button", "row-id", item.id);
  button.type = "button";
  button.dataset.focusKey = `row:${item.id}`;
  button.setAttribute("tabindex", item.id === state.selected ? "0" : "-1");
  button.addEventListener("click", (event) => {
    event.stopPropagation();
    selectItem(item.id);
    openDrawer(false);
  });
  identity.append(button);

  const status = make("td", "cell-status");
  status.append(statusNode(item.status));

  const title = make("td", "cell-title", text(item.title, "—"));
  const priority = make("td", "cell-pri", text(item.priority, "—"));
  const dependencies = make("td", "cell-deps", list(item.dependencies).join(" ") || "—");
  const trailing = make("td", "cell-pr");
  trailing.append(prAndWorkerCell(item));

  row.append(identity, status, title, priority, dependencies, trailing);
  return row;
}

function prAndWorkerCell(item) {
  const wrapper = make("span", "link-row");
  const pr = item.live_pr || item.recorded_pr;
  if (pr) {
    const label = pr.number ? `#${pr.number}` : text(pr.ref, "PR");
    wrapper.append(make("span", "card__pr", label));
  }
  if (item.worker) {
    wrapper.append(workerNode(item.worker));
  }
  const mailbox = item.mailbox || {};
  const unread = mailbox.available ? count(mailbox.inbox) + count(mailbox.outbox) : 0;
  if (unread > 0) {
    wrapper.append(make("span", "flag", `${unread} mail`));
  }
  if (wrapper.childNodes.length === 0) {
    wrapper.append(make("span", "muted", "—"));
  }
  return wrapper;
}

/* ---------- decisions ---------- */

function decisionCard(decision, open) {
  const card = make("article", open ? "decision decision--open" : "decision decision--resolved");
  card.append(make("p", "decision__flag", open ? "Needs a decision" : "Resolved"));
  card.append(make("p", "decision__question", text(decision.question, "No question recorded.")));
  const options = list(decision.options);
  if (options.length > 0) {
    const listing = make("ul", "decision__options");
    options.forEach((option) => listing.append(make("li", "", option)));
    card.append(listing);
  }
  if (!open && text(decision.answer)) {
    card.append(make("p", "", `Answer: ${decision.answer}`));
  }
  const meta = make("p", "decision__meta");
  [
    text(decision.id),
    humanize(decision.kind),
    decision.raised_by ? `raised by ${decision.raised_by}` : "",
    decision.contract_ref ? `contract ${decision.contract_ref}` : "",
    open ? `opened ${formatRelative(decision.created_at)}` : `resolved ${formatRelative(decision.resolved_at)}`,
    !open && decision.resolved_by ? `by ${decision.resolved_by}` : "",
  ].filter(Boolean).forEach((part) => meta.append(make("span", "", part)));
  if (text(decision.item_id)) {
    const link = make("button", "link-button", decision.item_id);
    link.type = "button";
    link.dataset.focusKey = `decision:${decision.id}:${decision.item_id}`;
    link.addEventListener("click", () => {
      selectItem(decision.item_id);
      openDrawer(false);
    });
    meta.append(link);
  }
  card.append(meta);
  return card;
}

function renderDecisions() {
  const open = list(snapshotOf().open_decisions);
  const resolved = list(snapshotOf().resolved_decisions);
  dom.decisionsNote.textContent = open.length === 0
    ? "Nothing is waiting on you."
    : `${plural(open.length, "decision")} waiting on you.`;

  dom.openDecisions.replaceChildren();
  if (open.length === 0) {
    dom.openDecisions.append(emptyNote("Workers raise a decision here when they cannot choose alone."));
  } else {
    open.forEach((decision) => dom.openDecisions.append(decisionCard(decision, true)));
  }

  dom.resolvedDecisions.replaceChildren();
  dom.resolvedSummary.textContent = `Resolved decisions (${resolved.length})`;
  dom.resolvedWrap.hidden = resolved.length === 0;
  resolved.forEach((decision) => dom.resolvedDecisions.append(decisionCard(decision, false)));
}

/* ---------- goal ---------- */

function renderGoal() {
  const artifacts = list(snapshotOf().program_artifacts);
  dom.goalNav.replaceChildren();
  if (artifacts.length > 1) {
    artifacts.forEach((artifact) => {
      const button = make("button", "link-button", artifact.name);
      button.type = "button";
      button.dataset.focusKey = `program:${artifact.name}`;
      button.setAttribute("aria-current", artifact.name === state.programFile ? "true" : "false");
      button.addEventListener("click", () => {
        state.programFile = artifact.name;
        renderGoal();
      });
      dom.goalNav.append(button);
    });
  }

  const selected = programArtifact(state.programFile) || programArtifact("goal.md");
  if (!selected) {
    dom.goalBody.replaceChildren(emptyNote("No program files were found on disk."));
    dom.goalMeta.textContent = "";
    return;
  }
  const body = typeof selected.text === "string" ? selected.text : "";
  if (!selected.present || !body.trim()) {
    dom.goalBody.replaceChildren(emptyNote(
      ARTIFACT_HINTS[selected.name] || `${selected.name} has not been written yet.`,
    ));
    dom.goalMeta.textContent = `${selected.path} · not written`;
    return;
  }
  renderMarkdown(dom.goalBody, body);
  const parts = [selected.path, formatSize(selected.size)];
  if (selected.updated_at) {
    parts.push(`updated ${formatRelative(selected.updated_at)}`);
  }
  if (selected.truncated) {
    parts.push("truncated for display");
  }
  dom.goalMeta.textContent = parts.join(" · ");
}

function renderContracts() {
  const contracts = list(snapshotOf().contracts);
  dom.contractsSummary.textContent = `Contracts (${contracts.length})`;
  dom.contracts.replaceChildren();
  if (contracts.length === 0) {
    dom.contracts.append(emptyNote("No contracts published. Programs publish one when tasks share an interface."));
    return;
  }
  contracts.forEach((contract) => dom.contracts.append(contractCard(contract, false)));
}

function contractCard(contract, includeText) {
  const card = make("article", "contract");
  card.dataset.status = text(contract.status, "pending");
  card.append(make("p", "contract__name", `${text(contract.name)} v${count(contract.version)}`));
  const artifact = contract.artifact || {};
  const facts = keyValues([
    ["Ref", text(contract.ref)],
    ["Status", humanize(contract.status)],
    ["Path", text(contract.path)],
    ["Digest", text(contract.sha256) ? truncate(contract.sha256, 20) : ""],
    ["Published", formatTimestamp(contract.published_at)],
    ["Approved", actorStamp(contract.approved_at, contract.approved_by)],
    ["Rejected", actorStamp(contract.rejected_at, contract.rejected_by)],
    ["Reason", text(contract.rejection_reason)],
    ["File", artifact.present
      ? `${formatSize(artifact.size)} · updated ${formatRelative(artifact.updated_at)}`
      : "not on disk"],
  ]);
  if (facts) {
    card.append(facts);
  }
  if (includeText && typeof artifact.text === "string" && artifact.text.trim()) {
    card.append(make("pre", "artifact-text", artifact.text));
    if (artifact.truncated) {
      card.append(make("p", "banner banner--warn", "This contract was truncated for display."));
    }
  }
  return card;
}

/* ---------- drawer ---------- */

function detailSection(heading) {
  const section = make("section", "detail-section");
  section.append(make("h3", "", heading));
  return section;
}

function keyValues(pairs) {
  const listing = make("dl", "kv");
  let wrote = false;
  pairs.forEach(([term, value]) => {
    if (value === undefined || value === null || value === "") {
      return;
    }
    listing.append(make("dt", "", term));
    const definition = make("dd");
    if (value instanceof Node) {
      definition.append(value);
    } else {
      definition.textContent = String(value);
    }
    listing.append(definition);
    wrote = true;
  });
  return wrote ? listing : null;
}

function itemLinkRow(ids, prefix) {
  const row = make("div", "link-row");
  ids.forEach((id) => {
    const button = make("button", "link-button", id);
    button.type = "button";
    button.dataset.focusKey = `${prefix}:${id}`;
    const target = itemByID(id);
    if (target) {
      button.title = `${target.title} · ${statusMeta(target.status).word}`;
      button.addEventListener("click", () => {
        selectItem(id);
        renderDetail();
      });
    } else {
      button.disabled = true;
      button.title = "This task is not in the program.";
    }
    row.append(button);
  });
  return row;
}

/* Section order in the drawer body and in the sticky section bar. A section
   only appears when the task actually has that content, so no tab is dead. */
const DETAIL_SECTIONS = [
  ["overview", "Overview"],
  ["blockers", "Blockers"],
  ["dependencies", "Dependencies"],
  ["workflow", "Workflow"],
  ["pull-request", "Pull request"],
  ["worker", "Worker"],
  ["notes", "Notes"],
  ["decisions", "Decisions"],
  ["contracts", "Contracts"],
  ["files", "Files"],
  ["warnings", "Warnings"],
];

function renderDetail() {
  const item = selectedItem();
  const sameTask = item !== null && state.detailItem === item.id;
  const keptScroll = sameTask ? dom.drawerScroll.scrollTop : 0;
  const keptSection = sameTask ? state.detailSection : "";

  dom.detailBody.replaceChildren();
  state.detailItem = item ? item.id : "";

  if (!item) {
    dom.drawerID.textContent = text(state.selected);
    dom.drawerTitle.textContent = "Task detail";
    dom.drawerMeta.replaceChildren();
    dom.detailBody.append(emptyNote(state.selected
      ? `Task ${state.selected} is not in this program. It may have been removed.`
      : "Select a task to inspect it."));
    renderDetailNav([]);
    return;
  }

  dom.drawerID.textContent = item.id;
  dom.drawerTitle.textContent = text(item.title, "Untitled task");
  dom.drawerMeta.replaceChildren(...identityBadges(item));

  const built = new Map();
  built.set("overview", detailHead(item));
  const reasons = itemReasons(item);
  if (reasons.length > 0) {
    built.set("blockers", reasonSection(item, reasons));
  }
  built.set("dependencies", dependencySection(item));
  const workflow = workflowSection(item);
  if (workflow) {
    built.set("workflow", workflow);
  }
  built.set("pull-request", pullRequestSection(item));
  built.set("worker", workerSection(item));
  const notes = notesSection(item);
  if (notes) {
    built.set("notes", notes);
  }
  const decisions = itemDecisionSection(item);
  if (decisions) {
    built.set("decisions", decisions);
  }
  const contracts = itemContractSection(item);
  if (contracts) {
    built.set("contracts", contracts);
  }
  built.set("files", artifactSection(item));
  const warnings = itemWarningSection(item);
  if (warnings) {
    built.set("warnings", warnings);
  }

  const present = [];
  DETAIL_SECTIONS.forEach(([key, label]) => {
    const node = built.get(key);
    if (!node) {
      return;
    }
    node.id = `section-${key}`;
    node.dataset.section = key;
    const heading = node.querySelector("h3");
    if (heading) {
      heading.id = `section-${key}-heading`;
      heading.setAttribute("tabindex", "-1");
      node.setAttribute("aria-labelledby", heading.id);
    }
    dom.detailBody.append(node);
    present.push([key, label]);
  });

  renderDetailNav(present);
  fitDetailTail();
  const active = present.some(([key]) => key === keptSection) ? keptSection : (present[0] || [""])[0];
  state.detailSection = active;
  markDetailSection(active);
  if (sameTask) {
    dom.drawerScroll.scrollTop = keptScroll;
  } else {
    dom.drawerScroll.scrollTop = 0;
  }
}

/* The last section still has to be able to sit under the section bar, so the
   body gets exactly the tail room it needs and no arbitrary empty screenful. */
function fitDetailTail() {
  const sections = dom.detailBody.querySelectorAll("[data-section]");
  const last = sections[sections.length - 1];
  if (!last || dom.drawerScroll.clientHeight === 0) {
    return;
  }
  dom.detailBody.style.paddingBottom = "";
  const offset = dom.drawerNav.hidden ? 0 : dom.drawerNav.offsetHeight;
  const room = dom.drawerScroll.clientHeight - offset - last.offsetHeight;
  dom.detailBody.style.paddingBottom = `${Math.max(24, Math.round(room))}px`;
}

function renderDetailNav(present) {
  dom.drawerNav.replaceChildren();
  dom.drawerNav.hidden = present.length < 2;
  present.forEach(([key, label], index) => {
    const tab = make("button", "drawer__tab", label);
    tab.type = "button";
    tab.dataset.section = key;
    tab.dataset.focusKey = `section:${key}`;
    tab.setAttribute("aria-controls", `section-${key}`);
    tab.setAttribute("aria-current", "false");
    tab.setAttribute("tabindex", index === 0 ? "0" : "-1");
    tab.addEventListener("click", () => showDetailSection(key));
    dom.drawerNav.append(tab);
  });
}

/* showDetailSection jumps the drawer body so the section sits under the
   section bar. Assigning scrollTop is instant by design: this is keyboard
   reachable navigation, which must never animate. Focus deliberately stays on
   the control so the arrow keys keep working; aria-current carries the state. */
function showDetailSection(key) {
  const section = dom.detailBody.querySelector(`[data-section="${key}"]`);
  if (!section) {
    return;
  }
  const scrollBox = dom.drawerScroll.getBoundingClientRect();
  const sectionBox = section.getBoundingClientRect();
  const offset = dom.drawerNav.hidden ? 0 : dom.drawerNav.offsetHeight;
  dom.drawerScroll.scrollTop += sectionBox.top - scrollBox.top - offset;
  state.detailSection = key;
  markDetailSection(key);
}

function markDetailSection(key) {
  Array.from(dom.drawerNav.querySelectorAll(".drawer__tab")).forEach((tab) => {
    const active = tab.dataset.section === key;
    tab.setAttribute("aria-current", active ? "true" : "false");
    tab.setAttribute("tabindex", active ? "0" : "-1");
  });
}

function onDetailNavKey(event) {
  const tabs = Array.from(dom.drawerNav.querySelectorAll(".drawer__tab"));
  if (tabs.length === 0) {
    return;
  }
  const current = Math.max(0, tabs.findIndex((tab) => tab === document.activeElement));
  let next = -1;
  if (event.key === "ArrowRight" || event.key === "ArrowDown") {
    next = (current + 1) % tabs.length;
  } else if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
    next = (current - 1 + tabs.length) % tabs.length;
  } else if (event.key === "Home") {
    next = 0;
  } else if (event.key === "End") {
    next = tabs.length - 1;
  }
  if (next < 0) {
    return;
  }
  event.preventDefault();
  event.stopPropagation();
  tabs.forEach((tab) => tab.setAttribute("tabindex", "-1"));
  tabs[next].setAttribute("tabindex", "0");
  tabs[next].focus();
}

/* Keeps the section bar honest while the reader scrolls the drawer body. */
function syncDetailSection() {
  const tabs = Array.from(dom.drawerNav.querySelectorAll(".drawer__tab"));
  if (tabs.length === 0) {
    return;
  }
  const offset = dom.drawerNav.hidden ? 0 : dom.drawerNav.offsetHeight;
  const top = dom.drawerScroll.getBoundingClientRect().top + offset + 4;
  let active = tabs[0].dataset.section;
  tabs.forEach((tab) => {
    const section = dom.detailBody.querySelector(`[data-section="${tab.dataset.section}"]`);
    if (section && section.getBoundingClientRect().top <= top) {
      active = tab.dataset.section;
    }
  });
  if (active !== state.detailSection) {
    state.detailSection = active;
    markDetailSection(active);
  }
}

/* The task's status, priority and flags live in the drawer header so the
   Overview section can be facts rather than a badge soup. */
function identityBadges(item) {
  const badges = [statusNode(item.status), make("span", "flag", text(item.priority, "P?"))];
  if (item.ready) {
    badges.push(make("span", "flag flag--ready", "ready to dispatch"));
  }
  if (item.orphaned) {
    badges.push(make("span", "flag flag--orphan", "orphaned"));
  }
  return badges;
}

function detailHead(item) {
  const head = detailSection("Overview");
  const stamps = item.timestamps || {};
  const times = keyValues([
    ["Project", text(item.project_slug)],
    ["Updated", formatTimestamp(stamps.updated_at)],
    ["Dispatched", formatTimestamp(stamps.dispatched_at)],
    ["In review", formatTimestamp(stamps.in_review_at)],
    ["Merged", formatTimestamp(stamps.merged_at)],
    ["Cancelled", formatTimestamp(stamps.cancelled_at)],
    ["PR grant", item.grant ? actorStamp(item.grant.granted_at, item.grant.granted_by) : ""],
  ]);
  if (times) {
    head.append(times);
  }
  return head;
}

function itemReasons(item) {
  const reasons = list(item.reasons).slice();
  if (reasons.length > 0) {
    return reasons;
  }
  const blocked = list(planOf().blocked).find((entry) => entry.item_id === item.id);
  return blocked ? list(blocked.reasons) : [];
}

function reasonSection(item, reasons) {
  const section = detailSection(item.status === "blocked" ? "Why it is blocked" : "Why it cannot start");
  section.classList.add("detail-section--blocking");
  const note = make("div", "detail-note");
  const listing = make("ul", "reason-list");
  reasons.forEach((reason) => listing.append(make("li", "", reason)));
  note.append(listing);
  section.append(note);
  return section;
}

function dependencySection(item) {
  const section = detailSection("Dependencies");
  const dependencies = list(item.dependencies);
  const dependents = list(item.dependents);
  const upstream = make("div");
  upstream.append(make("p", "detail-label", "Waits for"));
  upstream.append(dependencies.length
    ? itemLinkRow(dependencies, "dep")
    : emptyNote("Nothing. This task can start as soon as capacity allows."));
  const downstream = make("div");
  downstream.append(make("p", "detail-label", "Unblocks"));
  downstream.append(dependents.length
    ? itemLinkRow(dependents, "dependent")
    : emptyNote("Nothing else waits on this task."));
  section.append(upstream, downstream);
  return section;
}

function workflowSection(item) {
  const child = item.child;
  if (!child) {
    if (item.status === "pending" || item.status === "blocked") {
      const section = detailSection("Workflow");
      section.append(emptyNote("No child project yet. Relay creates one when the task is dispatched."));
      return section;
    }
    return null;
  }
  const section = detailSection("Workflow");
  const manifest = child.manifest || {};
  const facts = keyValues([
    ["Project", text(manifest.slug)],
    ["Branch", text(manifest.branch)],
    ["Base", text(manifest.base_branch)],
    ["Worktree", text(manifest.worktree)],
    ["Status", humanize(manifest.status)],
    ["Workflow", text(manifest.workflow)],
    ["Merged", manifest.merged ? "yes" : "no"],
    ["Archived", manifest.archived ? "yes" : ""],
  ]);
  if (facts) {
    section.append(facts);
  }

  const workflow = child.workflow;
  if (!workflow) {
    section.append(emptyNote("The worker has not recorded workflow state yet."));
    return section;
  }
  const strip = make("div", "phases");
  const phases = list(workflow.phases);
  const order = list(workflow.order);
  const byName = new Map(phases.map((phase) => [phase.name, phase]));
  const names = order.length > 0 ? order : phases.map((phase) => phase.name);
  names.forEach((name) => {
    const phase = byName.get(name) || { name, status: "pending" };
    const cell = make("div", "phase");
    cell.dataset.status = text(phase.status, "pending");
    cell.dataset.current = name === workflow.current_phase ? "true" : "false";
    cell.append(make("span", "phase__name", name));
    cell.append(make("span", "phase__status", humanize(phase.status) || "Pending"));
    strip.append(cell);
  });
  section.append(strip);
  section.append(make("p", "muted",
    `Current phase ${text(workflow.current_phase, "none")} · updated ${formatRelative(workflow.updated_at)}`));
  return section;
}

function trustedURL(value) {
  const raw = text(value);
  if (!raw) {
    return "";
  }
  try {
    const parsed = new URL(raw);
    return parsed.protocol === "https:" ? parsed.href : "";
  } catch (error) {
    return "";
  }
}

function pullRequestSection(item) {
  const live = item.live_pr;
  const pr = live || item.recorded_pr;
  const stale = Boolean(live && live.stale);
  const section = detailSection(stale
    ? "Pull request · stale GitHub cache"
    : live ? "Pull request · live from GitHub" : "Pull request · recorded");
  if (!pr) {
    section.append(emptyNote(pullRequestHint(item)));
    return section;
  }

  const heading = make("p", "");
  const url = trustedURL(pr.url);
  const label = pr.number ? `#${pr.number}` : text(pr.ref, "pull request");
  if (url) {
    const link = make("a", "", `${label} ${text(pr.title, "")}`.trim());
    link.setAttribute("href", url);
    link.setAttribute("target", "_blank");
    link.setAttribute("rel", "noopener noreferrer");
    link.dataset.focusKey = "pr:link";
    heading.append(link);
  } else {
    heading.append(make("span", "", `${label} ${text(pr.title, "")}`.trim()));
  }
  section.append(heading);

  const facts = keyValues([
    ["State", verdictNode(pr.draft ? "draft" : pr.state)],
    ["Checks", verdictNode(pr.checks)],
    ["Review", verdictNode(pr.review_decision)],
    ["Mergeable", verdictNode(pr.mergeable)],
    ["Ref", text(pr.ref)],
    ["Updated", formatTimestamp(pr.updated_at)],
    [stale ? "Stale since" : "Fetched", formatTimestamp(pr.fetched_at)],
  ]);
  if (facts) {
    section.append(facts);
  }
  if (stale) {
    section.append(make("p", "banner banner--warn",
      `GitHub refresh failed. Showing cached data fetched ${formatRelative(pr.fetched_at)}. ${text(pr.stale_reason)}`.trim()));
  } else if (!live && item.recorded_pr) {
    section.append(make("p", "banner banner--note",
      "GitHub did not answer for this pull request, so these values come from recorded program state."));
  }
  if (!url && text(pr.url)) {
    section.append(make("p", "banner banner--warn",
      "The recorded pull request address is not a secure link, so it is shown as text only."));
  }
  return section;
}

function pullRequestHint(item) {
  switch (text(item.status)) {
    case "pending":
      return "No pull request yet. One appears after the task is dispatched and the worker opens it.";
    case "blocked":
      return "No pull request yet. Clear the blockers above first.";
    case "dispatched":
      return "The worker has not opened a pull request yet.";
    case "cancelled":
      return "This task was cancelled before a pull request was recorded.";
    default:
      return "No pull request is recorded for this task.";
  }
}

function workerSection(item) {
  const section = detailSection("Worker");
  const worker = item.worker;
  if (!worker) {
    section.append(emptyNote(item.status === "dispatched"
      ? "Herdr did not report a live agent for this worktree. The worker may have exited."
      : "No live worker is attached to this task."));
  } else {
    const facts = keyValues([
      ["Liveness", workerNode(worker)],
      ["Terminal", text(worker.terminal_title)],
      ["Pane", text(worker.pane_id)],
      ["Tab", text(worker.tab_id)],
      ["Workspace", text(worker.workspace_id)],
      ["Directory", text(worker.cwd)],
      ["Foreground", text(worker.foreground_cwd)],
    ]);
    if (facts) {
      section.append(facts);
    }
  }

  const mailbox = item.mailbox || {};
  if (!mailbox.available) {
    section.append(make("p", "muted", "Mailbox unavailable for this task."));
  } else {
    section.append(make("p", "muted",
      `Mailbox · inbox ${count(mailbox.inbox)} unread · outbox ${count(mailbox.outbox)} unread`));
  }
  return section;
}

function notesSection(item) {
  const notes = list(item.notes);
  if (notes.length === 0) {
    return null;
  }
  const section = detailSection("Notes");
  const listing = make("ul", "note-list");
  notes.forEach((note) => listing.append(make("li", "", note)));
  section.append(listing);
  return section;
}

function itemDecisionSection(item) {
  const decisions = list(item.decisions);
  if (decisions.length === 0) {
    return null;
  }
  const section = detailSection("Decisions");
  decisions.forEach((decision) => {
    section.append(decisionCard(decision, !text(decision.resolved_at)));
  });
  return section;
}

function itemContractSection(item) {
  const refs = list(item.contracts);
  if (refs.length === 0) {
    return null;
  }
  const section = detailSection("Contracts");
  const all = list(snapshotOf().contracts);
  refs.forEach((ref) => {
    const contract = all.find((entry) => entry.ref === ref);
    if (!contract) {
      section.append(make("p", "muted", `${ref} · not published`));
      return;
    }
    section.append(contractCard(contract, true));
  });
  return section;
}

function artifactSection(item) {
  const section = detailSection("Files");
  const artifacts = list(item.artifacts);
  if (artifacts.length === 0) {
    section.append(emptyNote("No worker files yet. They appear once the task has a child project."));
    return section;
  }
  const present = artifacts.filter((artifact) => artifact.present);
  let selected = state.artifactByItem.get(item.id);
  if (!selected || !artifacts.some((artifact) => artifact.name === selected)) {
    selected = present.length > 0 ? present[0].name : artifacts[0].name;
    state.artifactByItem.set(item.id, selected);
  }

  const nav = make("div", "link-row");
  artifacts.forEach((artifact) => {
    const button = make("button", "link-button", artifact.name);
    button.type = "button";
    button.dataset.focusKey = `art:${item.id}:${artifact.name}`;
    button.setAttribute("aria-current", artifact.name === selected ? "true" : "false");
    if (!artifact.present) {
      button.title = "Not written yet";
    }
    button.addEventListener("click", () => {
      state.artifactByItem.set(item.id, artifact.name);
      renderDetail();
      restoreFocus(`art:${item.id}:${artifact.name}`);
    });
    nav.append(button);
  });
  section.append(nav);
  section.append(make("p", "muted", `${present.length} of ${artifacts.length} files written`));

  const artifact = artifacts.find((entry) => entry.name === selected);
  if (!artifact) {
    section.append(emptyNote("That file is not part of this task."));
    return section;
  }
  if (!artifact.present) {
    section.append(emptyNote(
      ARTIFACT_HINTS[artifact.name] || "The worker writes this file when the workflow reaches that phase.",
    ));
    return section;
  }
  if (typeof artifact.text !== "string") {
    section.append(emptyNote(
      `${artifact.name} exists (${formatSize(artifact.size)}) but its text is only loaded for the selected task.`,
    ));
    return section;
  }
  if (!artifact.text.trim()) {
    section.append(emptyNote(`${artifact.name} exists but is empty.`));
    return section;
  }
  section.append(make("pre", "artifact-text", artifact.text));
  section.append(make("p", "muted",
    [artifact.path, formatSize(artifact.size), `updated ${formatRelative(artifact.updated_at)}`].join(" · ")));
  if (artifact.truncated) {
    section.append(make("p", "banner banner--warn",
      "This file was truncated for display. Open it on disk to read the rest."));
  }
  return section;
}

function itemWarningSection(item) {
  const warnings = list(item.warnings);
  if (warnings.length === 0) {
    return null;
  }
  const section = detailSection("Warnings");
  section.classList.add("detail-section--warn");
  const note = make("div", "detail-note");
  const listing = make("ul", "warning-list");
  warnings.forEach((warning) => listing.append(make("li", "", warning)));
  note.append(listing);
  section.append(note);
  return section;
}

function focusables() {
  return Array.from(dom.drawerPanel.querySelectorAll(
    'a[href], button:not([disabled]):not([tabindex="-1"]), input, select, textarea, [tabindex]:not([tabindex="-1"])',
  )).filter((node) => node.offsetParent !== null || node === dom.drawerPanel);
}

function openDrawer(instant) {
  if (state.drawerTimer !== null) {
    clearTimeout(state.drawerTimer);
    state.drawerTimer = null;
  }
  if (!state.drawerOpen) {
    state.drawerReturn = document.activeElement;
    state.detailItem = "";
  }
  state.drawerOpen = true;
  renderDetail();
  dom.drawer.hidden = false;
  dom.drawer.dataset.instant = instant ? "true" : "false";
  /* Reading a layout value commits the closed state so the transition runs. */
  void dom.drawer.offsetWidth;
  dom.drawer.dataset.state = "open";
  fitDetailTail();
  dom.drawerScroll.scrollTop = 0;
  markDetailSection(state.detailSection);
  dom.drawerPanel.focus({ preventScroll: true });
  writeHash();
}

function closeDrawer() {
  if (!state.drawerOpen) {
    return;
  }
  state.drawerOpen = false;
  dom.drawer.dataset.state = "closed";
  const hide = () => {
    state.drawerTimer = null;
    if (!state.drawerOpen) {
      dom.drawer.hidden = true;
    }
  };
  if (dom.drawer.dataset.instant === "true") {
    hide();
  } else {
    state.drawerTimer = setTimeout(hide, 180);
  }
  const back = state.drawerReturn;
  state.drawerReturn = null;
  if (!returnFocusTo(back)) {
    focusSelection();
  }
  writeHash();
}

/* returnFocusTo only claims success when the node really takes focus. A row
   click leaves document.body active, and a re-render can drop the node, so
   both cases must fall through to the current selection. */
function returnFocusTo(node) {
  if (!node || node === document.body || typeof node.focus !== "function") {
    return false;
  }
  if (!document.contains(node)) {
    return false;
  }
  node.focus({ preventScroll: true });
  return document.activeElement === node;
}

function onDrawerKey(event) {
  if (event.key === "Escape") {
    event.preventDefault();
    closeDrawer();
    return;
  }
  if (event.key !== "Tab") {
    return;
  }
  const order = focusables();
  if (order.length === 0) {
    event.preventDefault();
    return;
  }
  const first = order[0];
  const last = order[order.length - 1];
  if (event.shiftKey && (document.activeElement === first || document.activeElement === dom.drawerPanel)) {
    event.preventDefault();
    last.focus();
    return;
  }
  if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
  }
}

/* ---------- render orchestration ---------- */

function captureFocus() {
  const active = document.activeElement;
  return active && active.dataset && active.dataset.focusKey ? active.dataset.focusKey : "";
}

function restoreFocus(key) {
  if (!key) {
    return;
  }
  const candidates = document.querySelectorAll("[data-focus-key]");
  for (const candidate of candidates) {
    if (candidate.dataset.focusKey === key) {
      candidate.focus({ preventScroll: true });
      return;
    }
  }
}

function scrollTargets() {
  return [dom.roadmapScroll, dom.tableScroll, dom.drawerScroll];
}

function captureScroll() {
  const positions = scrollTargets().map((node) => (node ? [node.scrollTop, node.scrollLeft] : [0, 0]));
  positions.push([window.scrollY, window.scrollX]);
  return positions;
}

function restoreScroll(positions) {
  scrollTargets().forEach((node, index) => {
    if (!node) {
      return;
    }
    node.scrollTop = positions[index][0];
    node.scrollLeft = positions[index][1];
  });
  const page = positions[positions.length - 1];
  window.scrollTo(page[1], page[0]);
}

function render() {
  const focusKey = captureFocus();
  const scroll = captureScroll();
  renderHeader();
  renderRoadmap();
  updateStatusFilters();
  renderLedger();
  renderDecisions();
  renderGoal();
  renderContracts();
  renderWarnings();
  if (state.drawerOpen) {
    renderDetail();
  }
  restoreFocus(focusKey);
  restoreScroll(scroll);
}

/* ---------- selection and hash ---------- */

function writeHash() {
  const parts = [];
  if (state.tab !== "roadmap") {
    parts.push(`tab=${state.tab}`);
  }
  if (state.selected) {
    parts.push(`task=${encodeURIComponent(state.selected)}`);
  }
  const hash = parts.length > 0 ? `#${parts.join("&")}` : "";
  if (window.location.hash === hash) {
    return;
  }
  if (window.history && typeof window.history.replaceState === "function") {
    window.history.replaceState(null, "", `${window.location.pathname}${window.location.search}${hash}`);
    return;
  }
  window.location.hash = hash;
}

function readHash() {
  const raw = window.location.hash.replace(/^#/, "");
  const result = { tab: "", task: "" };
  if (!raw) {
    return result;
  }
  if (raw.indexOf("=") === -1) {
    result.task = safeID(raw);
    return result;
  }
  raw.split("&").forEach((pair) => {
    const [key, value] = pair.split("=");
    if (key === "tab" && TABS.indexOf(value) !== -1) {
      result.tab = value;
    }
    if (key === "task") {
      result.task = safeID(value || "");
    }
  });
  return result;
}

function safeID(raw) {
  let decoded = raw;
  try {
    decoded = decodeURIComponent(raw);
  } catch (error) {
    decoded = raw;
  }
  return decoded.length <= MAX_ITEM_ID && ITEM_ID.test(decoded) ? decoded : "";
}

function applyHash() {
  const parsed = readHash();
  if (parsed.tab && parsed.tab !== state.tab) {
    selectTab(parsed.tab);
  }
  if (parsed.task && parsed.task !== state.selected) {
    selectItem(parsed.task);
    openDrawer(true);
  }
}

function selectItem(id) {
  if (!id) {
    return;
  }
  const changed = state.selected !== id;
  state.selected = id;
  markSelection();
  writeHash();
  if (state.drawerOpen) {
    renderDetail();
  }
  if (changed) {
    poll();
  }
}

/* markSelection repaints only the selection affordances so keyboard movement
   never waits on a full re-render. */
function markSelection() {
  state.cards.forEach((card, id) => {
    const active = id === state.selected;
    card.dataset.selected = active ? "true" : "false";
    card.setAttribute("tabindex", active ? "0" : "-1");
  });
  Array.from(dom.ledgerRows.querySelectorAll("tr")).forEach((row) => {
    const active = row.dataset.item === state.selected;
    row.setAttribute("aria-selected", active ? "true" : "false");
    const button = row.querySelector(".row-id");
    if (button) {
      button.setAttribute("tabindex", active ? "0" : "-1");
    }
  });
  if (state.tab === "roadmap") {
    drawConnectorsForCurrentGraph();
  }
}

function drawConnectorsForCurrentGraph() {
  dom.graphEdges.replaceChildren();
  drawConnectors(list((snapshotOf().graph || {}).edges));
}

function focusSelection() {
  if (state.tab === "tasks") {
    restoreFocus(`row:${state.selected}`);
    return;
  }
  if (state.tab === "roadmap") {
    restoreFocus(`card:${state.selected}`);
  }
}

function moveSelection(step) {
  const visible = state.tab === "roadmap"
    ? Array.from(state.cards.keys()).map(itemByID).filter(Boolean)
    : visibleItems();
  if (visible.length === 0) {
    return;
  }
  const current = visible.findIndex((item) => item.id === state.selected);
  let next = current + step;
  if (current === -1) {
    next = step > 0 ? 0 : visible.length - 1;
  }
  next = Math.max(0, Math.min(visible.length - 1, next));
  selectItem(visible[next].id);
  focusSelection();
  if (state.tab === "roadmap") {
    revealCard(visible[next].id);
  }
}

/* ---------- interaction ---------- */

function isTypingTarget(node) {
  if (!node) {
    return false;
  }
  const tag = node.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || node.isContentEditable;
}

function onGlobalKey(event) {
  if (event.metaKey || event.ctrlKey || event.altKey) {
    return;
  }
  if (event.key === "Escape" && state.drawerOpen) {
    event.preventDefault();
    closeDrawer();
    return;
  }
  if (state.drawerOpen) {
    return;
  }
  if (event.key === "Escape" && document.activeElement === dom.filter) {
    if (dom.filter.value) {
      dom.filter.value = "";
      state.filter = "";
      renderLedger();
      return;
    }
    dom.filter.blur();
    focusSelection();
    return;
  }
  if (isTypingTarget(event.target)) {
    return;
  }
  if (event.key === "/" && state.tab === "tasks") {
    event.preventDefault();
    dom.filter.focus();
    dom.filter.select();
    return;
  }
  if (event.key === "g" && state.tab === "roadmap" && state.selected) {
    event.preventDefault();
    revealCard(state.selected);
    restoreFocus(`card:${state.selected}`);
    return;
  }
  const inList = state.tab === "tasks" || state.tab === "roadmap";
  if (!inList) {
    return;
  }
  const arrows = dom.tableScroll.contains(event.target) || event.target === document.body;
  if (event.key === "j" || (event.key === "ArrowDown" && arrows)) {
    event.preventDefault();
    moveSelection(1);
    return;
  }
  if (event.key === "k" || (event.key === "ArrowUp" && arrows)) {
    event.preventDefault();
    moveSelection(-1);
    return;
  }
  if (event.key === "Enter" && state.selected && !isTypingTarget(event.target)) {
    event.preventDefault();
    openDrawer(true);
  }
}

function bindControls() {
  dom.themeToggle.addEventListener("click", toggleTheme);
  dom.refresh.addEventListener("click", () => {
    setFeed(state.live, "Refreshing…");
    poll();
  });
  dom.copyCommand.addEventListener("click", copyCommand);
  dom.warningCount.addEventListener("click", () => {
    selectTab("goal");
    dom.diagnostics.open = true;
    dom.diagnostics.scrollIntoView({ block: "nearest" });
  });
  dom.filter.addEventListener("input", () => {
    state.filter = dom.filter.value;
    renderLedger();
  });
  dom.tabs.forEach((button) => {
    button.addEventListener("click", () => selectTab(button.dataset.tab));
  });
  dom.tablist.addEventListener("keydown", onTabKey);
  dom.drawerClose.addEventListener("click", closeDrawer);
  dom.drawerScrim.addEventListener("click", closeDrawer);
  dom.drawer.addEventListener("keydown", onDrawerKey);
  dom.drawerNav.addEventListener("keydown", onDetailNavKey);
  dom.drawerScroll.addEventListener("scroll", () => {
    if (state.drawerOpen) {
      syncDetailSection();
    }
  }, { passive: true });
  window.addEventListener("hashchange", applyHash);
  window.addEventListener("resize", () => {
    if (state.tab === "roadmap") {
      drawConnectorsForCurrentGraph();
    }
    if (state.drawerOpen) {
      fitDetailTail();
    }
  });
  document.addEventListener("keydown", onGlobalKey);
}

/* ---------- polling ---------- */

let pollTimer = null;
let inFlight = null;

function schedule(delay) {
  if (pollTimer !== null) {
    clearTimeout(pollTimer);
  }
  pollTimer = setTimeout(() => {
    pollTimer = null;
    poll();
  }, delay);
}

function signatureOf(body) {
  return body.replace(/"generated_at":"[^"]*"/g, "");
}

async function poll() {
  if (inFlight) {
    inFlight.abort();
  }
  const controller = new AbortController();
  inFlight = controller;
  /* Only ask for detail the API will accept; anything else drops back to the
     base snapshot instead of failing every poll with 400. */
  const query = ITEM_ID.test(state.selected) ? `?item=${encodeURIComponent(state.selected)}` : "";
  try {
    const response = await fetch(`/api/program${query}`, {
      cache: "no-store",
      signal: controller.signal,
      headers: { Accept: "application/json" },
    });
    if (!response.ok) {
      throw new Error(`Program request failed with status ${response.status}`);
    }
    const body = await response.text();
    const snapshot = JSON.parse(body);
    state.failures = 0;
    hideReconnect();
    setFeed(true, "Live · every 3s");
    const signature = signatureOf(body);
    state.snapshot = snapshot;
    if (signature === state.signature) {
      renderHeader();
    } else {
      state.signature = signature;
      render();
    }
    if (state.pendingDrawer && state.selected) {
      state.pendingDrawer = false;
      openDrawer(true);
    }
    schedule(POLL_INTERVAL);
  } catch (error) {
    if (controller.signal.aborted) {
      return;
    }
    state.failures += 1;
    const delay = BACKOFF[Math.min(state.failures - 1, BACKOFF.length - 1)];
    setFeed(false, "Reconnecting…");
    showReconnect(
      `${error.message || "Cannot reach Relay"} · retrying in ${delay / 1000}s · ` +
      `showing the last snapshot${state.snapshot ? "" : " (none yet)"}.`,
    );
    schedule(delay);
  } finally {
    if (inFlight === controller) {
      inFlight = null;
    }
  }
}

/* ---------- start ---------- */

function start() {
  collectDom();
  renderThemeToggle();
  buildStatusFilters();
  bindControls();
  const parsed = readHash();
  state.selected = parsed.task;
  state.pendingDrawer = Boolean(parsed.task);
  selectTab(parsed.tab || "roadmap");
  renderDetail();
  setFeed(false, "Connecting…");
  poll();
  window.setInterval(() => {
    if (state.snapshot) {
      renderHeader();
    }
  }, 15000);
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", start);
} else {
  start();
}
