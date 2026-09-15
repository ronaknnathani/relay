"use strict";

const WORKER_META = {
  idle: { glyph: "○", word: "Idle", lane: "pending" },
  working: { glyph: "▶", word: "Working", lane: "dispatched" },
  blocked: { glyph: "✕", word: "Blocked", lane: "blocked" },
  done: { glyph: "●", word: "Done", lane: "merged" },
  unknown: { glyph: "?", word: "Unknown", lane: "pending" },
};

const VERDICT_GOOD = ["passing", "success", "approved", "mergeable", "clean"];
const VERDICT_BAD = ["failing", "failure", "error", "changes_requested", "conflicting", "dirty", "blocked"];

const TASK_ARTIFACT_NAMES = [
  "assignment.md",
  "task.md",
  "requirements.md",
  "clarify.md",
  "plan.md",
  "notes.md",
  "todos.md",
  "progress.md",
  "tradeoffs.md",
  "questions.md",
  "follow-ups.md",
  "review.md",
  "validation.md",
  "pr-body.md",
  "context.md",
];

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
  const fragment = new DocumentFragment();
  visible.forEach((item) => fragment.append(ledgerRow(item)));
  dom.ledgerRows.append(fragment);
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
  if (selected.present && selected.text === undefined) {
    dom.goalBody.replaceChildren(emptyNote("File content loads with the external refresh."));
    dom.goalMeta.textContent = [
      selected.path || selected.name,
      formatSize(selected.size),
    ].filter(Boolean).join(" · ");
    return;
  }
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
  reconcileArtifactSelection(item);
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
  const github = (snapshotOf().source_health || {}).github || {};
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
      github.status === "loading"
        ? "GitHub refresh pending. Showing recorded program state until the source responds."
        : "GitHub did not return live data for this pull request, so these values come from recorded program state."));
  }
  if (!url && text(pr.url)) {
    section.append(make("p", "banner banner--warn",
      "The recorded pull request address is not a secure link, so it is shown as text only."));
  }
  return section;
}

function pullRequestHint(item) {
  const github = (snapshotOf().source_health || {}).github || {};
  if (github.status === "loading") {
    return "GitHub refresh pending. Live pull request state is not known yet.";
  }
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
  const herdr = (snapshotOf().source_health || {}).herdr || {};
  if (!worker) {
    if (herdr.status === "loading") {
      section.append(emptyNote("Herdr refresh pending. Live worker state is not known yet."));
    } else {
      section.append(emptyNote(item.status === "dispatched"
        ? "Herdr did not report a live agent for this worktree. The worker may have exited."
        : "No live worker is attached to this task."));
    }
  } else {
    if (worker.stale) {
      const fetched = worker.fetched_at ? ` from ${formatRelative(worker.fetched_at)}` : "";
      section.append(make("p", "banner banner--warn",
        `Showing last-known worker state${fetched}. ${text(worker.stale_reason)}`.trim()));
    }
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
  let selected = state.contractByItem.get(item.id);
  if (!selected || refs.indexOf(selected) === -1) {
    selected = refs[0];
    state.contractByItem.set(item.id, selected);
  }
  const nav = make("div", "link-row");
  refs.forEach((ref) => {
    const contract = all.find((entry) => entry.ref === ref);
    if (!contract) {
      nav.append(make("span", "muted", `${ref} · not published`));
      return;
    }
    const button = make("button", "link-button", ref);
    button.type = "button";
    button.dataset.focusKey = `contract:${item.id}:${ref}`;
    button.setAttribute("aria-current",
      currentArtifactKey(item.id) === artifactCacheKey({ kind: "contract", ref }) ? "true" : "false");
    button.addEventListener("click", () => {
      const selector = { kind: "contract", ref };
      state.contractByItem.set(item.id, ref);
      state.artifactSelection.set(item.id, selector);
      renderDetail();
      restoreFocus(`contract:${item.id}:${ref}`);
      loadArtifact(selector, true);
    });
    nav.append(button);
  });
  section.append(nav);
  const contract = all.find((entry) => entry.ref === selected);
  if (contract) {
    section.append(contractCard(contract, false));
    if (currentArtifactKey(item.id) === artifactCacheKey({ kind: "contract", ref: selected })) {
      appendArtifactContent(section, { kind: "contract", ref: selected }, contract.artifact || {});
    }
  }
  return section;
}

function reconcileArtifactSelection(item) {
  const contracts = list(snapshotOf().contracts);
  const validContractRefs = list(item.contracts)
    .filter((ref) => contracts.some((contract) => contract.ref === ref));
  let selectedContract = state.contractByItem.get(item.id);
  if (!selectedContract || validContractRefs.indexOf(selectedContract) === -1) {
    selectedContract = validContractRefs[0] || "";
    if (selectedContract) {
      state.contractByItem.set(item.id, selectedContract);
    } else {
      state.contractByItem.delete(item.id);
    }
  }

  const artifacts = list(item.artifacts).length > 0
    ? list(item.artifacts)
    : (item.child_available ? TASK_ARTIFACT_NAMES.map((name) => ({ name })) : []);
  const validArtifactNames = new Set(artifacts.map((artifact) => artifact.name));
  const selection = state.artifactSelection.get(item.id);
  if (!selection) {
    return;
  }
  if (selection.kind === "contract") {
    if (validContractRefs.indexOf(selection.ref) !== -1) {
      state.contractByItem.set(item.id, selection.ref);
      return;
    }
    if (selectedContract) {
      state.artifactSelection.set(item.id, { kind: "contract", ref: selectedContract });
      return;
    }
  } else if (selection.kind === "task" && validArtifactNames.has(selection.name)) {
    return;
  }

  const first = artifacts.find((artifact) => artifact.present) || artifacts[0];
  if (first) {
    state.artifactByItem.set(item.id, first.name);
    state.artifactSelection.set(
      item.id,
      { kind: "task", item: item.id, name: first.name },
    );
  } else {
    state.artifactSelection.delete(item.id);
  }
}

function artifactSection(item) {
  const section = detailSection("Files");
  const metadataAvailable = list(item.artifacts).length > 0;
  const artifacts = metadataAvailable
    ? list(item.artifacts)
    : (item.child_available ? TASK_ARTIFACT_NAMES.map((name) => ({ name })) : []);
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
  const activeSelection = state.artifactSelection.get(item.id);
  if (!activeSelection ||
      (activeSelection.kind === "task" &&
       (activeSelection.name !== selected ||
        !artifacts.some((artifact) => artifact.name === activeSelection.name)))) {
    state.artifactSelection.set(item.id, { kind: "task", item: item.id, name: selected });
  }

  const nav = make("div", "link-row");
  artifacts.forEach((artifact) => {
    const button = make("button", "link-button", artifact.name);
    button.type = "button";
    button.dataset.focusKey = `art:${item.id}:${artifact.name}`;
    button.setAttribute("aria-current",
      currentArtifactKey(item.id) === artifactCacheKey({ kind: "task", item: item.id, name: artifact.name })
        ? "true"
        : "false");
    if (metadataAvailable && !artifact.present) {
      button.title = "Not written yet";
    }
    button.addEventListener("click", () => {
      const selector = { kind: "task", item: item.id, name: artifact.name };
      state.artifactByItem.set(item.id, artifact.name);
      state.artifactSelection.set(item.id, selector);
      renderDetail();
      restoreFocus(`art:${item.id}:${artifact.name}`);
      loadArtifact(selector, true);
    });
    nav.append(button);
  });
  section.append(nav);
  section.append(make("p", "muted", metadataAvailable
    ? `${present.length} of ${artifacts.length} files written`
    : "File metadata loads with the background refresh."));

  const artifact = artifacts.find((entry) => entry.name === selected);
  if (!artifact) {
    section.append(emptyNote("That file is not part of this task."));
    return section;
  }
  const selector = { kind: "task", item: item.id, name: selected };
  if (currentArtifactKey(item.id) === artifactCacheKey(selector)) {
    appendArtifactContent(section, selector, artifact);
  }
  return section;
}

function artifactCacheKey(selector) {
  return selector.kind === "task"
    ? `task:${selector.item}:${selector.name}`
    : `contract:${selector.ref}`;
}

function currentArtifactKey(itemID) {
  const selector = state.artifactSelection.get(itemID);
  return selector ? artifactCacheKey(selector) : "";
}

function artifactURL(selector) {
  const query = new URLSearchParams({ kind: selector.kind });
  if (selector.kind === "task") {
    query.set("item", selector.item);
    query.set("name", selector.name);
  } else {
    query.set("ref", selector.ref);
  }
  return `/api/artifact?${query.toString()}`;
}

function appendArtifactContent(section, selector, metadata) {
  const key = artifactCacheKey(selector);
  const entry = state.artifactCache.get(key);
  const label = metadata.name || selector.ref;
  if (!entry) {
    section.append(emptyNote(`Loading ${label}…`));
    loadArtifact(selector, false);
    return;
  }
  if (entry.status === "loading" && !entry.lastValid) {
    section.append(emptyNote(`Loading ${label}…`));
    return;
  }
  if (entry.status === "error") {
    section.append(make("p", "banner banner--warn", entry.error));
    section.append(emptyNote("Content is unavailable."));
    return;
  }
  const historical = entry.status === "loading" || entry.status === "stale";
  const valid = historical ? entry.lastValid : entry;
  const envelope = valid && valid.envelope;
  const artifact = envelope ? envelope.artifact || metadata : metadata;
  if (entry.status === "loading") {
    section.append(make("p", "banner banner--warn",
      `Refreshing ${label} · showing last-known result.`));
  } else if (entry.status === "stale") {
    section.append(make("p", "banner banner--warn",
      `${entry.error} Showing the last-known result; current state is unknown.`));
  }
  if (!envelope) {
    section.append(emptyNote("Content is unavailable."));
    return;
  }
  if (envelope.state === "missing") {
    if (historical) {
      section.append(emptyNote(
        `Last successful check found ${artifact.name || selector.ref} missing; current state is unknown.`,
      ));
      return;
    }
    section.append(emptyNote(
      ARTIFACT_HINTS[artifact.name] || `${artifact.name || selector.ref} has not been written yet.`,
    ));
    return;
  }
  if (envelope.state === "empty") {
    if (historical) {
      section.append(emptyNote(
        `Last successful check found ${artifact.name || selector.ref} empty; current state is unknown.`,
      ));
      return;
    }
    section.append(emptyNote(`${artifact.name || selector.ref} exists but is empty.`));
    return;
  }
  if (typeof artifact.text === "string") {
    section.append(make("pre", "artifact-text", artifact.text));
  }
  section.append(make("p", "muted",
    [artifact.path, formatSize(artifact.size), artifact.updated_at
      ? `updated ${formatRelative(artifact.updated_at)}`
      : ""].filter(Boolean).join(" · ")));
  if (envelope.state === "truncated" || artifact.truncated) {
    section.append(make("p", "banner banner--warn",
      "This file was truncated for display. Open it on disk to read the rest."));
  }
}

function loadCurrentArtifact(revalidate) {
  const item = selectedItem();
  if (!item || !state.drawerOpen) {
    return;
  }
  let selector = state.artifactSelection.get(item.id);
  if (!selector) {
    const artifacts = list(item.artifacts);
    const first = artifacts.find((artifact) => artifact.present) || artifacts[0];
    if (!first) {
      return;
    }
    selector = { kind: "task", item: item.id, name: first.name };
    state.artifactSelection.set(item.id, selector);
  }
  loadArtifact(selector, revalidate);
}

async function loadArtifact(selector, revalidate) {
  const key = artifactCacheKey(selector);
  const existing = state.artifactCache.get(key);
  const current = existing;
  if (current && current.status === "loading" &&
      current.controller === state.artifactController &&
      !current.controller.signal.aborted &&
      current.generation === state.artifactGeneration) {
    return;
  }
  const lastValid = current && current.status === "ready"
    ? { envelope: current.envelope, etag: current.etag }
    : current && current.lastValid;
  if (!revalidate && lastValid) {
    return;
  }
  if (state.artifactController) {
    state.artifactController.abort();
  }
  const controller = new AbortController();
  const generation = ++state.artifactGeneration;
  state.artifactController = controller;
  state.artifactCache.set(key, {
    status: "loading", lastValid, controller, generation,
  });
  const headers = { Accept: "application/json" };
  if (lastValid && lastValid.etag) {
    headers["If-None-Match"] = lastValid.etag;
  }
  try {
    const response = await fetch(artifactURL(selector), {
      cache: "no-cache", signal: controller.signal, headers,
    });
    if (generation !== state.artifactGeneration || !artifactMatchesSelection(selector)) {
      return;
    }
    if (response.status === 304 && lastValid) {
      state.artifactCache.set(key, {
        status: "ready", envelope: lastValid.envelope, etag: lastValid.etag,
      });
      renderDetailPreservingFocus();
      return;
    }
    const envelope = await response.json();
    if (generation !== state.artifactGeneration || !artifactMatchesSelection(selector)) {
      return;
    }
    if (!response.ok) {
      throw new Error(envelope.error || `Artifact request failed with status ${response.status}`);
    }
    state.artifactCache.set(key, {
      status: "ready", envelope, etag: response.headers.get("ETag") || "",
    });
    renderDetailPreservingFocus();
  } catch (error) {
    if (controller.signal.aborted || generation !== state.artifactGeneration ||
        !artifactMatchesSelection(selector)) {
      return;
    }
    const message = error.message || "Artifact content could not be loaded.";
    state.artifactCache.set(key, lastValid
      ? { status: "stale", lastValid, error: message }
      : { status: "error", error: message });
    renderDetailPreservingFocus();
  } finally {
    const latest = state.artifactCache.get(key);
    if (latest && latest.status === "loading" && latest.generation === generation) {
      if (lastValid) {
        state.artifactCache.set(key, { status: "ready", ...lastValid });
      } else {
        state.artifactCache.delete(key);
      }
    }
    if (state.artifactController === controller) {
      state.artifactController = null;
    }
  }
}

function artifactMatchesSelection(selector) {
  const itemID = selector.item || state.selected;
  if (!state.drawerOpen || state.selected !== itemID) {
    return false;
  }
  return currentArtifactKey(state.selected) === artifactCacheKey(selector);
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
  loadCurrentArtifact(true);
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


function ensureDeferredDom() {
  if (dom.drawer) {
    return;
  }
  const byID = (id) => document.getElementById(id);
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
  dom.filter.addEventListener("input", () => {
    state.filter = dom.filter.value;
    renderLedger();
  });
  dom.drawerClose.addEventListener("click", closeDrawer);
  dom.drawerScrim.addEventListener("click", closeDrawer);
  dom.drawer.addEventListener("keydown", onDrawerKey);
  dom.drawerNav.addEventListener("keydown", onDetailNavKey);
  dom.drawerScroll.addEventListener("scroll", () => {
    if (state.drawerOpen) {
      syncDetailSection();
    }
  }, { passive: true });
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

function renderDetailPreservingFocus() {
  const focusKey = captureFocus();
  renderDetail();
  restoreFocus(focusKey);
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
  if (!deferredUIReady && (state.tab !== "roadmap" || state.drawerOpen)) {
    withDeferredUI(render);
    return;
  }
  const focusKey = captureFocus();
  const scroll = captureScroll();
  renderHeader();
  state.dirtyTabs = new Set(TABS);
  renderActiveTab();
  if (state.drawerOpen) {
    renderDetail();
  }
  restoreFocus(focusKey);
  restoreScroll(scroll);
}


/* ---------- selection and hash ---------- */

function applyHash() {
  const parsed = readHash();
  if (parsed.tab && parsed.tab !== state.tab) {
    selectTab(parsed.tab);
  }
  if (parsed.task && parsed.task !== state.selected) {
    selectItem(parsed.task);
    withDeferredUI(() => openDrawer(true));
  }
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
  const arrows = (dom.tableScroll && dom.tableScroll.contains(event.target)) ||
    event.target === document.body;
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
    withDeferredUI(() => openDrawer(true));
  }
}
