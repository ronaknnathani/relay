"use strict";

/* Relay Program UI.
   Read-only. Every value reaches the DOM through textContent or createElement;
   no markup is ever parsed from data. */

const THEME_KEY = "relay.program.theme";
const THEMES = ["light", "dark"];

function applyTheme(theme) {
  document.documentElement.dataset.theme = THEMES.indexOf(theme) === -1 ? "light" : theme;
}

const POLL_INTERVAL = 3000;
const BACKOFF = [3000, 6000, 12000];
const INITIAL_ROADMAP_CARDS = 2;
const ROADMAP_RENDER_BATCH = 128;
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

const dom = {};
const cardTemplate = document.createElement("button");
cardTemplate.className = "card";
cardTemplate.type = "button";
const stageTemplate = make("div", "stage");
stageTemplate.append(cardTemplate);

const state = {
  snapshot: null,
  signature: "",
  tab: "roadmap",
  selected: "",
  filter: "",
  statuses: new Set(),
  programFile: "goal.md",
  artifactByItem: new Map(),
  contractByItem: new Map(),
  artifactSelection: new Map(),
  artifactCache: new Map(),
  itemsByID: new Map(),
  artifactController: null,
  artifactGeneration: 0,
  programGeneration: 0,
  roadmapRenderGeneration: 0,
  dirtyTabs: new Set(TABS),
  cards: new Map(),
  connectorPaths: [],
  drawerOpen: false,
  drawerReturn: null,
  detailItem: "",
  detailSection: "",
  drawerTimer: null,
  pendingDrawer: false,
  copyTimer: null,
  failures: 0,
  live: false,
  bundlePending: "",
  bundleError: "",
  pollError: "",
  pendingTab: null,
  pendingHash: false,
};

const initialProgramController = new AbortController();
const initialProgramElement = document.getElementById("initial-program");
const initialProgramSnapshot = initialProgramElement
  ? JSON.parse(initialProgramElement.textContent)
  : null;
const initialProgramRequest = initialProgramSnapshot
  ? null
  : requestProgram(initialProgramController, "roadmap");
let deferredUIReady = false;
let deferredUIPromise = null;
let deferredStyleReady = false;
let deferredStylePromise = null;
let deferredScriptReady = false;
let deferredScriptPromise = null;
let fullSnapshotPromise = null;

function loadDeferredStyle() {
  if (deferredStyleReady) {
    return Promise.resolve();
  }
  if (deferredStylePromise) {
    return deferredStylePromise;
  }
  deferredStylePromise = new Promise((resolve, reject) => {
    const styles = document.createElement("link");
    styles.rel = "stylesheet";
    styles.href = "/app-deferred.css";
    styles.onload = () => {
      deferredStyleReady = true;
      resolve();
    };
    styles.onerror = () => {
      styles.remove();
      deferredStylePromise = null;
      reject(new Error("Deferred stylesheet failed to load."));
    };
    document.head.append(styles);
  });
  return deferredStylePromise;
}

function loadDeferredScript() {
  if (deferredScriptReady) {
    return Promise.resolve();
  }
  if (deferredScriptPromise) {
    return deferredScriptPromise;
  }
  deferredScriptPromise = new Promise((resolve, reject) => {
    const script = document.createElement("script");
    script.src = "/app-deferred.js";
    script.onload = () => {
      deferredScriptReady = true;
      resolve();
    };
    script.onerror = () => {
      script.remove();
      deferredScriptPromise = null;
      reject(new Error("Deferred script failed to load."));
    };
    document.head.append(script);
  });
  return deferredScriptPromise;
}

function loadDeferredUI() {
  if (deferredUIReady) {
    return Promise.resolve();
  }
  if (deferredUIPromise) {
    return deferredUIPromise;
  }
  deferredUIPromise = Promise.all([loadDeferredStyle(), loadDeferredScript()]).then(() => {
    deferredUIReady = true;
    state.bundlePending = "";
    state.bundleError = "";
    ensureDeferredDom();
    renderReconnect();
    flushPendingNavigation();
  }).catch((error) => {
    deferredUIPromise = null;
    state.bundlePending = "";
    state.bundleError =
      `${error.message || "Deferred interface failed to load."} Click Refresh to retry.`;
    renderReconnect();
    throw error;
  });
  return deferredUIPromise;
}

function withDeferredUI(action) {
  loadFullSnapshot();
  loadDeferredUI()
    .then(loadFullSnapshot)
    .catch(() => false)
    .then((snapshotReady) => {
      if (snapshotReady) {
        action();
      }
    });
}

function loadFullSnapshot() {
  if (fullSnapshotReady()) {
    return Promise.resolve(true);
  }
  if (fullSnapshotPromise) {
    return fullSnapshotPromise;
  }
  fullSnapshotPromise = poll().then((loaded) => {
    if (!loaded) {
      fullSnapshotPromise = null;
    }
    return loaded;
  });
  return fullSnapshotPromise;
}

function fullSnapshotReady() {
  return Boolean(state.snapshot && state.snapshot.schema === "relay.program.v1");
}

function flushPendingTab() {
  if (!deferredUIReady || !fullSnapshotReady() || !state.pendingTab) {
    return;
  }
  const pendingTab = state.pendingTab;
  state.pendingTab = null;
  state.bundlePending = "";
  renderReconnect();
  selectTab(pendingTab.name, pendingTab.options);
}

function flushPendingNavigation() {
  if (!deferredUIReady || !fullSnapshotReady()) {
    return;
  }
  if (state.pendingHash) {
    state.pendingHash = false;
    state.pendingTab = null;
    state.bundlePending = "";
    renderReconnect();
    applyHash();
    return;
  }
  flushPendingTab();
}

function loadPendingTab() {
  Promise.all([loadDeferredUI(), loadFullSnapshot()])
    .then((results) => {
      if (results[1]) {
        flushPendingNavigation();
      }
    })
    .catch(() => {});
}

/* ---------- DOM helpers ---------- */

function collectDom() {
  const byID = (id) => document.getElementById(id);
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
  return state.itemsByID.get(id) || null;
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
  renderHeaderIdentity();
  renderHeaderDetails();
}

function renderHeaderIdentity() {
  const program = programOf();
  const displayTitle = text(program.display_title) ||
    truncate(text(program.title, "Untitled program"), 72);

  document.title = `${displayTitle} · Relay`;
  dom.title.textContent = displayTitle;
  dom.summary.textContent = text(program.summary);
  dom.slug.textContent = text(program.slug, "program");
  if (program.archived) {
    dom.slug.textContent = `${text(program.slug, "program")} (archived)`;
  }
}

function renderHeaderDetails() {
  const program = programOf();
  const programState = text(program.state, "unknown");
  dom.programState.replaceChildren(
    stateGlyph(programState),
    make("span", "", humanize(programState)),
  );
  dom.programState.dataset.lane = stateLane(programState);
  setText(dom.updated, program.updated_at ? `updated ${formatRelative(program.updated_at)}` : "");
  setText(dom.repo, text(program.repo));
  setText(dom.agent, program.agent ? `agent ${program.agent}` : "");
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
  const overview = snapshotOf().overview || {};
  const openDecisions = Number.isFinite(overview.open_decisions)
    ? overview.open_decisions
    : open;
  dom.decisionCount.textContent = String(openDecisions);
  dom.decisionNote.textContent = openDecisions === 0
    ? "Nothing waiting on you"
    : `${plural(openDecisions, "answer")} needed`;

  const health = snapshotOf().source_health || {};
  const herdr = health.herdr || {};
  if (herdr.status === "loading") {
    dom.workerCount.textContent = "—";
    dom.workerNote.textContent = "Awaiting Herdr · worker count unknown";
  } else {
    let workers = overview.workers;
    let active = overview.active_workers;
    let unread = overview.unread_messages;
    if (!Number.isFinite(workers) || !Number.isFinite(active) || !Number.isFinite(unread)) {
      workers = 0;
      active = 0;
      unread = 0;
      for (const item of items()) {
        if (item.worker) {
          workers += 1;
          if (item.worker.status === "working") {
            active += 1;
          }
        }
        const mailbox = item.mailbox || {};
        if (mailbox.available) {
          unread += count(mailbox.inbox) + count(mailbox.outbox);
        }
      }
    }
    dom.workerCount.textContent = String(workers);
    dom.workerNote.textContent = [
      herdr.stale ? `${active} working · last-known worker data` : `${active} working`,
      unread ? `${plural(unread, "unread message")}` : "no unread mail",
    ].join(" · ");
  }

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

function setSnapshotFeed(snapshot) {
  const refresh = snapshot.refresh || {};
  if (refresh.status === "failed" && refresh.refreshing) {
    setFeed(false, `Retrying · last refresh failed · ${text(refresh.error, "program refresh failed")}`);
    return;
  }
  if (refresh.status === "partial") {
    setFeed(false, `Loading external sources · showing local snapshot from ${formatRelative(snapshot.generated_at)}`);
    return;
  }
  if (refresh.status === "failed") {
    setFeed(false, `Stale · ${text(refresh.error, "program refresh failed")}`);
    return;
  }
  if (refresh.refreshing) {
    setFeed(false, `Refreshing · last update ${formatRelative(snapshot.generated_at)}`);
    return;
  }
  if (sourceHealthDegraded(snapshot)) {
    setFeed(false, "Updated · source data degraded");
    return;
  }
  setFeed(true, "Live · every 3s");
}

function sourceHealthDegraded(snapshot) {
  const health = snapshot.source_health || {};
  return Object.values(health).some((source) =>
    source && (source.status === "loading" || source.status === "degraded" || source.stale));
}

function showReconnect(message) {
  state.pollError = message;
  renderReconnect();
}

function hideReconnect() {
  if (dom.reconnect.hidden) {
    return;
  }
  state.pollError = "";
  renderReconnect();
}

function renderReconnect() {
  const message = [state.bundlePending, state.bundleError, state.pollError].filter(Boolean).join(" ");
  if (!message) {
    if (!dom.reconnect.hidden) {
      dom.reconnect.hidden = true;
      announce(dom.reconnect, "");
    }
    return;
  }
  announce(dom.reconnect, message);
  dom.reconnect.hidden = false;
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

function renderWarningCount() {
  const total = warningGroups().reduce((sum, group) => sum + group[1].length, 0);
  dom.warningCount.hidden = total === 0;
  dom.warningCount.textContent = plural(total, "warning");
}

/* ---------- tabs ---------- */

function selectTab(name, options) {
  const tab = TABS.indexOf(name) === -1 ? "roadmap" : name;
  if (tab !== "roadmap" && (!deferredUIReady || !fullSnapshotReady())) {
    state.pendingTab = { name: tab, options };
    state.bundlePending = `Loading ${tab.charAt(0).toUpperCase()}${tab.slice(1)}…`;
    renderReconnect();
    loadPendingTab();
    return;
  }
  if (tab === "roadmap" && state.pendingTab) {
    state.pendingTab = null;
    state.bundlePending = "";
    renderReconnect();
  }
  if (tab !== "roadmap" && deferredUIReady) {
    ensureDeferredDom();
  }
  state.tab = tab;
  dom.tabs.forEach((button) => {
    const active = button.dataset.tab === tab;
    button.setAttribute("aria-selected", active ? "true" : "false");
    button.setAttribute("tabindex", active ? "0" : "-1");
  });
  dom.panels.forEach((panel, key) => {
    panel.hidden = key !== tab;
  });
  if (state.snapshot && deferredUIReady) {
    writeHash();
  }
  renderActiveTab();
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

function renderRoadmap() {
  const generation = ++state.roadmapRenderGeneration;
  const graph = snapshotOf().graph || {};
  const nodes = list(graph.nodes);
  const plan = planOf();
  renderRoadmapSummary(graph, plan, nodes);

  if (nodes.length === 0) {
    state.cards.clear();
    state.connectorPaths = [];
    dom.graphEdges.replaceChildren();
    dom.roadmapEmpty.hidden = false;
    dom.roadmapEmpty.textContent = state.snapshot
      ? "No work items yet. The tech lead adds tasks when the program is planned."
      : "Loading the program…";
    dom.roadmapScroll.hidden = true;
    dom.graph.setAttribute("aria-label", "Dependency flow: no work items yet.");
    return true;
  }
  if (!dom.roadmapEmpty.hidden) {
    dom.roadmapEmpty.hidden = true;
  }
  if (dom.roadmapScroll.hidden) {
    dom.roadmapScroll.hidden = false;
  }
  const stages = stageLists(graph, nodes);
  const nodesByID = graph.layers
    ? new Map(nodes.map((node) => [node.id, node]))
    : null;
  const hasSelection = Boolean(selectedItem());
  const existingCards = Array.from(dom.graphNodes.querySelectorAll(".card"));
  const existingStages = Array.from(dom.graphNodes.children);
  const sameStages = existingStages.length === stages.length &&
    stages.every((entries, index) => {
      const expected = entries.map((entry) => typeof entry === "string" ? entry : entry.id);
      const rendered = Array.from(existingStages[index].children)
        .filter((child) => child.classList.contains("card"))
        .map((card) => card.dataset.item);
      return expected.length === rendered.length &&
        expected.every((id, itemIndex) => id === rendered[itemIndex]);
    });
  if (sameStages &&
      existingCards.length === nodes.length &&
      existingCards.every((card) => nodes.some((node) => node.id === card.dataset.item))) {
    state.cards.clear();
    existingCards.forEach((card, index) => {
      const id = card.dataset.item;
      const item = itemByID(id);
      const node = item || (nodesByID ? nodesByID.get(id) : nodes.find((entry) => entry.id === id));
      taskCard(node, item, index, hasSelection, card);
      state.cards.set(id, card);
    });
    drawConnectorsForCurrentGraph();
    return true;
  }
  const focusedItem = dom.graphNodes.contains(document.activeElement)
    ? document.activeElement.dataset.item
    : "";
  state.cards.clear();
  state.connectorPaths = [];
  dom.graphEdges.replaceChildren();
  let position = 0;
  let stageIndex = 0;
  let itemIndex = 0;
  const fragment = new DocumentFragment();
  const stageNodes = [];
  for (let index = 0; index < stages.length; index += 1) {
    const ids = stages[index];
    const stage = stageTemplate.cloneNode(false);
    stage.classList.toggle("stage--single", ids.length === 1);
    stage.dataset.stage = String(index);
    stage.dataset.label = `Stage ${index + 1} · ${plural(ids.length, "task")}`;
    stageNodes.push(stage);
    fragment.append(stage);
  }
  dom.graphNodes.replaceChildren(fragment);

  const renderBatch = (limit) => {
    let rendered = 0;
    while (stageIndex < stages.length && rendered < limit) {
      const ids = stages[stageIndex];
      const stage = stageNodes[stageIndex];
      while (itemIndex < ids.length && rendered < limit) {
        const entry = ids[itemIndex];
        const id = typeof entry === "string" ? entry : entry.id;
        const item = itemByID(id);
        const node = item || (nodesByID ? nodesByID.get(id) : entry) || { id, title: "", lane: "" };
        const card = taskCard(node, item, position, hasSelection, null);
        card.dataset.stage = String(stageIndex);
        position += 1;
        itemIndex += 1;
        rendered += 1;
        state.cards.set(id, card);
        stage.append(card);
      }
      if (itemIndex === ids.length) {
        stageIndex += 1;
        itemIndex = 0;
      }
    }
    return stageIndex === stages.length;
  };

  const initialBatch = nodes.length <= ROADMAP_RENDER_BATCH
    ? nodes.length
    : INITIAL_ROADMAP_CARDS;
  if (renderBatch(initialBatch)) {
    if (focusedItem && state.cards.has(focusedItem)) {
      state.cards.get(focusedItem).focus({ preventScroll: true });
    }
    drawConnectorsForCurrentGraph();
    return true;
  }
  const finish = () => {
    if (generation !== state.roadmapRenderGeneration || state.tab !== "roadmap") {
      return;
    }
    if (renderBatch(ROADMAP_RENDER_BATCH)) {
      state.dirtyTabs.delete("roadmap");
      const focusStayedInRoadmap =
        document.activeElement === document.body || dom.graphNodes.contains(document.activeElement);
      if (!state.drawerOpen && focusStayedInRoadmap && focusedItem && state.cards.has(focusedItem)) {
        state.cards.get(focusedItem).focus({ preventScroll: true });
      }
      drawConnectorsForCurrentGraph();
      return;
    }
    window.requestAnimationFrame(finish);
  };
  window.requestAnimationFrame(finish);
  return false;
}

function renderRoadmapSummary(graph, plan, nodes) {
  const orphaned = list(plan.orphaned).length;
  dom.roadmapNote.textContent =
    `${list(plan.ready).length} ready · ${list(plan.in_flight).length} in flight · ` +
    `${list(plan.blocked).length} blocked${orphaned ? ` · ${orphaned} orphaned` : ""}`;
  if (dom.roadmapCycle.hidden !== !graph.cyclic) {
    dom.roadmapCycle.hidden = !graph.cyclic;
  }
  if (graph.cyclic) {
    dom.roadmapCycle.textContent =
      "This program has a dependency cycle. Cyclic links are drawn dashed and stage order is approximate.";
  }
  dom.graph.setAttribute("aria-label", roadmapLabel(nodes, list(graph.edges)));
}

function stageLists(graph, nodes) {
  if (!graph.layers) {
    const grouped = [];
    for (const node of nodes) {
      const layer = Math.max(0, count(node.layer));
      while (grouped.length <= layer) {
        grouped.push([]);
      }
      grouped[layer].push(node);
    }
    return grouped.filter((layer) => layer.length > 0);
  }
  const layers = [];
  const placed = new Set();
  for (const rawLayer of list(graph.layers)) {
    const layer = list(rawLayer);
    layers.push(layer);
    for (const id of layer) {
      placed.add(id);
    }
  }
  if (placed.size === nodes.length) {
    return layers.filter((layer) => layer.length > 0);
  }
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
  const progress = snapshotOf().progress || {};
  const counts = {
    pending: count(progress.pending),
    dispatched: count(progress.dispatched),
    "in-review": count(progress.in_review),
    blocked: count(progress.blocked),
    merged: count(progress.merged),
    cancelled: count(progress.cancelled),
  };
  const labels = [
    counts.pending ? `${counts.pending} pending` : "",
    counts.dispatched ? `${counts.dispatched} dispatched` : "",
    counts["in-review"] ? `${counts["in-review"]} in review` : "",
    counts.blocked ? `${counts.blocked} blocked` : "",
    counts.merged ? `${counts.merged} merged` : "",
    counts.cancelled ? `${counts.cancelled} cancelled` : "",
  ].filter(Boolean);
  const breakdown = labels.join(", ");
  return `Dependency flow: ${plural(nodes.length, "task")}, ${plural(edges.length, "dependency link")}. ` +
    `${breakdown}. The Tasks tab carries the same information as a table.`;
}

function taskCard(node, item, position, hasSelection, existingCard) {
  const lane = text(node.lane, item ? text(item.status) : "pending");
  const card = existingCard || cardTemplate.cloneNode(true);
  card.dataset.lane = lane;
  card.dataset.item = node.id;
  card.dataset.focusKey = `card:${node.id}`;
  card.dataset.selected = node.id === state.selected ? "true" : "false";
  card.tabIndex = hasSelection
    ? (node.id === state.selected ? "0" : "-1")
    : (position === 0 ? "0" : "-1");
  decorateTaskCard(card, node, item, lane);
  return card;
}

function decorateTaskCard(card, node, item, lane) {
  const meta = statusMeta(lane);
  let content = `${text(node.title, "Untitled task")}\n${node.id} · ${meta.glyph} ${meta.word}`;
  const details = item || node;
  if (details) {
    const dependencyCount = item
      ? list(item.dependencies).length
      : count(node.dependency_count);
    const pr = item && (item.live_pr || item.recorded_pr);
    const prNumber = item ? (pr && pr.number) : count(node.pr_number);
    let facts = text(details.priority, "P?");
    if (dependencyCount > 0) {
      facts += ` · ${plural(dependencyCount, "dep")}`;
    }
    if (prNumber) {
      facts += ` · PR #${prNumber}`;
    }
    if (details.orphaned) {
      facts += " · orphan";
    } else if (details.ready) {
      facts += " · ready";
    }
    card.dataset.meta = facts;
    content += `\n${facts}`;
  }
  card.textContent = content;
  card.setAttribute("aria-label", taskCardLabel(node, item, lane));
}

function taskCardLabel(node, item, lane) {
  const details = item || node;
  const dependencies = item
    ? list(item.dependencies)
    : list(node.dependencies);
  const dependencyLabel = dependencies.length > 0
    ? `Dependencies: ${dependencies.join(", ")}`
    : "No dependencies";
  return `Task ${node.id}: ${text(node.title, "Untitled task")}. ` +
    `Status ${statusMeta(lane).word}. Priority ${text(details && details.priority, "unknown")}. ` +
    `${dependencyLabel}.`;
}

function onCardKey(event, id) {
  if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    selectItem(id);
    withDeferredUI(() => openDrawer(true));
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

  const connectorPaths = [];
  edges.forEach((edge) => {
    const from = boxes.get(edge.from);
    const to = boxes.get(edge.to);
    if (!from || !to) {
      return;
    }
    const downward = to.top > from.bottom + 4;
    let path;
    if (downward) {
      path = downwardPath(from, to);
    } else {
      path =
        `M ${round(from.center)} ${round(from.bottom + 1)} L ${round(to.center)} ${round(to.top - 7)}`;
    }
    connectorPaths.push({ from: edge.from, to: edge.to, downward, path });
  });
  state.connectorPaths = connectorPaths;
  renderConnectorPaths();
}

function updateConnectorSelection() {
  renderConnectorPaths();
}

function renderConnectorPaths() {
  const groups = {
    normal: [],
    active: [],
    back: [],
  };
  state.connectorPaths.forEach((edge) => {
    const active = edge.downward && Boolean(state.selected) &&
      (edge.from === state.selected || edge.to === state.selected);
    groups[edge.downward ? (active ? "active" : "normal") : "back"].push(edge.path);
  });
  const fragment = new DocumentFragment();
  [
    ["normal", "edge", "url(#flow-arrow)"],
    ["active", "edge edge--active", "url(#flow-arrow-active)"],
    ["back", "edge edge--back", "url(#flow-arrow)"],
  ].forEach(([group, className, marker]) => {
    if (groups[group].length === 0) {
      return;
    }
    const path = svg("path", className);
    path.setAttribute("d", groups[group].join(" "));
    path.setAttribute("marker-end", marker);
    path.dataset.edgeCount = String(groups[group].length);
    fragment.append(path);
  });
  dom.graphEdges.replaceChildren(fragment);
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

function selectItem(id) {
  if (!id) {
    return;
  }
  const changed = state.selected !== id;
  state.selected = id;
  markSelection();
  writeHash();
  if (!deferredUIReady) {
    return;
  }
  if (state.drawerOpen) {
    renderDetail();
  }
  if (changed && state.drawerOpen) {
    loadCurrentArtifact(false);
  }
}

function markSelection() {
  state.cards.forEach((card, id) => {
    const active = id === state.selected;
    card.dataset.selected = active ? "true" : "false";
    card.setAttribute("tabindex", active ? "0" : "-1");
  });
  if (dom.ledgerRows) {
    Array.from(dom.ledgerRows.querySelectorAll("tr")).forEach((row) => {
      const active = row.dataset.item === state.selected;
      row.setAttribute("aria-selected", active ? "true" : "false");
      const button = row.querySelector(".row-id");
      if (button) {
        button.setAttribute("tabindex", active ? "0" : "-1");
      }
    });
  }
  if (state.tab === "roadmap") {
    updateConnectorSelection();
  }
}

function drawConnectorsForCurrentGraph() {
  drawConnectors(list((snapshotOf().graph || {}).edges));
}

/* ---------- render orchestration ---------- */

function renderInitial() {
  state.dirtyTabs = new Set(TABS);
  const bootstrappedRoadmap = state.snapshot.schema === "relay.program.roadmap.bootstrap.v1";
  if (bootstrappedRoadmap) {
    state.dirtyTabs.delete("roadmap");
  } else {
    renderHeader();
    renderActiveTab();
  }
  const markUsable = () => {
    requestAnimationFrame(() => requestAnimationFrame(() => {
      window.__relayUsableAt = performance.now();
      performance.mark("relay-usable");
      const hydrate = () => {
        loadFullSnapshot();
        loadDeferredUI().then(loadFullSnapshot).catch(() => {});
      };
      if (window.requestIdleCallback) {
        window.requestIdleCallback(hydrate, { timeout: 1000 });
      } else {
        setTimeout(hydrate, 0);
      }
    }));
  };
  if (state.tab === "roadmap" && !state.selected) {
    markUsable();
    return;
  }
  queueMicrotask(() => withDeferredUI(() => {
    renderActiveTab();
    markUsable();
  }));
}

function renderCore() {
  renderHeader();
  state.dirtyTabs = new Set(TABS);
  renderActiveTab();
}

function renderActiveTab() {
  if (!state.snapshot) {
    return;
  }
  if (state.tab !== "roadmap" && !deferredUIReady) {
    return;
  }
  if (!state.dirtyTabs.has(state.tab)) {
    return;
  }
  if (state.tab === "roadmap") {
    if (renderRoadmap()) {
      state.dirtyTabs.delete(state.tab);
    }
    return;
  } else if (state.tab === "tasks") {
    if (dom.statusFilters.childElementCount === 0) {
      buildStatusFilters();
    }
    updateStatusFilters();
    renderLedger();
  } else if (state.tab === "decisions") {
    renderDecisions();
  } else if (state.tab === "goal") {
    renderGoal();
    renderContracts();
    renderWarnings();
  }
  state.dirtyTabs.delete(state.tab);
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

function isTypingTarget(node) {
  if (!node) {
    return false;
  }
  const tag = node.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || node.isContentEditable;
}

function tabKeyHandled(event) {
  return ["ArrowRight", "ArrowLeft", "Home", "End"].includes(event.key);
}

function cardKeyHandled(event) {
  return ["Enter", " ", "ArrowRight", "ArrowLeft", "ArrowDown", "ArrowUp", "Home", "End"]
    .includes(event.key);
}

function globalKeyHandled(event) {
  if (event.metaKey || event.ctrlKey || event.altKey) {
    return false;
  }
  if (event.key === "Escape" && state.drawerOpen) {
    return true;
  }
  if (state.drawerOpen) {
    return false;
  }
  if (event.key === "Escape" && dom.filter && document.activeElement === dom.filter) {
    return true;
  }
  if (isTypingTarget(event.target)) {
    return false;
  }
  if (event.key === "/" && state.tab === "tasks") {
    return true;
  }
  if (event.key === "g" && state.tab === "roadmap" && state.selected) {
    return true;
  }
  if (state.tab !== "tasks" && state.tab !== "roadmap") {
    return false;
  }
  const arrows = (dom.tableScroll && dom.tableScroll.contains(event.target)) ||
    event.target === document.body;
  return event.key === "j" || event.key === "k" ||
    ((event.key === "ArrowDown" || event.key === "ArrowUp") && arrows) ||
    (event.key === "Enter" && state.selected);
}

function bindControls() {
  dom.themeToggle.addEventListener("click", toggleTheme);
  dom.refresh.addEventListener("click", () => {
    setFeed(state.live, "Refreshing…");
    if (state.bundleError) {
      loadDeferredUI().catch(() => {});
    }
    poll();
  });
  dom.copyCommand.addEventListener("click", () => withDeferredUI(copyCommand));
  dom.warningCount.addEventListener("click", () => {
    withDeferredUI(() => {
      selectTab("goal");
      dom.diagnostics.open = true;
      dom.diagnostics.scrollIntoView({ block: "nearest" });
    });
  });
  dom.tabs.forEach((button) => {
    button.addEventListener("click", () => withDeferredUI(() => selectTab(button.dataset.tab)));
  });
  dom.tablist.addEventListener("keydown", (event) => {
    if (tabKeyHandled(event)) {
      event.preventDefault();
    }
    withDeferredUI(() => onTabKey(event));
  });
  dom.graphNodes.addEventListener("click", (event) => {
    const card = event.target.closest(".card");
    if (card) {
      selectItem(card.dataset.item);
      withDeferredUI(() => openDrawer(false));
    }
  });
  dom.graphNodes.addEventListener("keydown", (event) => {
    const card = event.target.closest(".card");
    if (card) {
      if (cardKeyHandled(event)) {
        event.preventDefault();
      }
      onCardKey(event, card.dataset.item);
    }
  });
  window.addEventListener("hashchange", () => {
    state.pendingHash = true;
    Promise.all([loadDeferredUI(), loadFullSnapshot()])
      .then((results) => {
        if (results[1]) {
          flushPendingNavigation();
        }
      })
      .catch(() => {});
  });
  window.addEventListener("resize", () => {
    if (state.tab === "roadmap") {
      drawConnectorsForCurrentGraph();
    }
    if (state.drawerOpen) {
      withDeferredUI(fitDetailTail);
    }
  });
  document.addEventListener("keydown", (event) => {
    if (globalKeyHandled(event)) {
      event.preventDefault();
    }
    withDeferredUI(() => onGlobalKey(event));
  });
}

/* ---------- polling ---------- */

let pollTimer = null;
let programController = null;

function requestProgram(controller, view) {
  return fetch(view ? `/api/program?view=${view}` : "/api/program", {
    cache: "no-store",
    signal: controller.signal,
    headers: { Accept: "application/json" },
  });
}

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

function roadmapRenderSignature(snapshot) {
  const graph = snapshot.graph || {};
  const byID = new Map(list(snapshot.items).map((item) => [item.id, item]));
  const nodes = list(graph.nodes).map((node) => {
    const item = byID.get(node.id);
    const pr = item && (item.live_pr || item.recorded_pr);
    return [
      node.id,
      node.title,
      node.lane,
      count(node.layer),
      text(item ? item.priority : node.priority),
      item ? list(item.dependencies) : list(node.dependencies),
      item ? count(pr && pr.number) : count(node.pr_number),
      Boolean(item ? item.ready : node.ready),
      Boolean(item ? item.orphaned : node.orphaned),
    ];
  });
  return JSON.stringify({
    nodes,
    edges: list(graph.edges),
    layers: list(graph.layers),
    cyclic: Boolean(graph.cyclic),
    progress: snapshot.progress || {},
    plan: snapshot.plan || {},
  });
}

async function poll(preloadedRequest, preloadedController, preloadedSnapshot) {
  if (programController) {
    programController.abort();
  }
  const controller = preloadedController || new AbortController();
  const generation = ++state.programGeneration;
  programController = controller;
  try {
    const response = preloadedSnapshot ? null : await (preloadedRequest || requestProgram(controller));
    if (response && !response.ok) {
      throw new Error(`Program request failed with status ${response.status}`);
    }
    const initial = !state.snapshot;
    const body = initial || preloadedSnapshot ? "" : await response.text();
    const snapshot = preloadedSnapshot || (initial ? await response.json() : JSON.parse(body));
    if (generation !== state.programGeneration) {
      return;
    }
    state.failures = 0;
    hideReconnect();
    const roadmapUnchanged =
      roadmapRenderSignature(snapshotOf()) === roadmapRenderSignature(snapshot);
    state.snapshot = snapshot;
    state.itemsByID = new Map();
    for (const item of items()) {
      state.itemsByID.set(item.id, item);
    }
    if (initial) {
      renderInitial();
      flushPendingNavigation();
      window.requestAnimationFrame(() => {
        state.signature = signatureOf(JSON.stringify(snapshot));
        if (snapshot.schema !== "relay.program.roadmap.bootstrap.v1") {
          setSnapshotFeed(snapshot);
        }
        if (state.pendingDrawer && state.selected) {
          withDeferredUI(() => {
            state.pendingDrawer = false;
            openDrawer(true);
          });
        }
      });
      return true;
    }
    const signature = signatureOf(body);
    setSnapshotFeed(snapshot);
    if (signature === state.signature) {
      renderHeader();
    } else if (!deferredUIReady) {
      renderCore();
      state.signature = signature;
    } else if (roadmapUnchanged && state.tab === "roadmap") {
      renderHeader();
      TABS.filter((tab) => tab !== "roadmap").forEach((tab) => state.dirtyTabs.add(tab));
      state.signature = signature;
    } else {
      render();
      state.signature = signature;
    }
    flushPendingNavigation();
    if (state.pendingDrawer && state.selected) {
      withDeferredUI(() => {
        state.pendingDrawer = false;
        openDrawer(true);
      });
    } else if (state.drawerOpen) {
      withDeferredUI(() => loadCurrentArtifact(true, true));
    }
    schedule(POLL_INTERVAL);
    return true;
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
    return false;
  } finally {
    if (programController === controller) {
      programController = null;
    }
  }
}

/* ---------- start ---------- */

function start() {
  collectDom();
  if (window.__relayRoadmapCoreCleanup) {
    window.__relayRoadmapCoreCleanup();
    delete window.__relayRoadmapCoreCleanup;
  }
  Array.from(dom.graphNodes.querySelectorAll(".card")).forEach((card) => {
    state.cards.set(card.dataset.item, card);
  });
  state.connectorPaths = window.__relayRoadmapConnectorPaths || [];
  delete window.__relayRoadmapConnectorPaths;
  renderThemeToggle();
  bindControls();
  const parsed = readHash();
  state.selected = parsed.task || window.__relayRoadmapSelection || "";
  state.pendingDrawer = Boolean(parsed.task);
  selectTab(parsed.tab || "roadmap");
  setFeed(false, "Connecting…");
  poll(initialProgramRequest, initialProgramController, initialProgramSnapshot);
  window.setInterval(() => {
    if (fullSnapshotReady()) {
      renderHeader();
    }
  }, 15000);
}

start();
