"use strict";

const STATUSES = ["dispatched", "in-review", "blocked"];

const byID = (id) => document.getElementById(id);
const dom = {
  connectionStatus: byID("connection-status"),
  programCount: byID("program-count"),
  programList: byID("program-list"),
  programEmpty: byID("program-empty"),
  workCount: byID("work-count"),
  workEmpty: byID("work-empty"),
  diagnostics: byID("diagnostics"),
  diagnosticCount: byID("diagnostic-count"),
  diagnosticList: byID("diagnostic-list"),
};

const make = (tag, className, text) => {
  const node = document.createElement(tag);
  if (className) {
    node.className = className;
  }
  if (text !== undefined) {
    node.textContent = text;
  }
  return node;
};

function programCard(program) {
  const link = make("a", "program-card");
  link.href = `programs/${encodeURIComponent(program.slug)}/`;
  link.dataset.focusKey = `program:${program.slug}`;

  const top = make("div", "program-card__top");
  top.append(
    make("span", "program-state", program.state.replaceAll("-", " ")),
    make("span", "program-updated", program.updated_at),
  );
  const title = make("h3", "", program.display_title);
  const slug = make("p", "program-meta program-slug", program.slug);
  const summary = make("p", "program-summary", program.summary || "No program summary.");
  const stats = make("div", "program-card__stats");
  [
    [program.progress.percent + "%", "merged"],
    [program.in_flight, "in flight"],
    [program.blocked, "blocked"],
    [program.open_decisions, "decisions"],
  ].forEach(([value, label]) => {
    const stat = make("span");
    stat.append(make("strong", "", String(value)), make("small", "", label));
    stats.append(stat);
  });
  const progress = make("div", "program-progress");
  progress.setAttribute("aria-label", `${program.progress.percent}% merged`);
  const fill = make("span");
  fill.style.width = `${program.progress.percent}%`;
  progress.append(fill);
  const next = make("p", "program-meta", `Next: ${program.next_action}`);
  const queue = make(
    "p",
    "program-meta",
    `${program.ready} ready · ${program.progress.merged} of ${program.progress.total} merged`,
  );
  link.append(top, title, slug, summary, stats, progress, next, queue);
  return link;
}

function workCard(item) {
  const link = make("a", "work-card");
  link.href = `programs/${encodeURIComponent(item.program_slug)}/#task=${encodeURIComponent(item.id)}`;
  link.dataset.focusKey = `work:${item.program_slug}:${item.id}`;
  const meta = make("div", "work-card__meta");
  meta.append(
    make("span", "priority", item.priority),
    make("span", "work-card__status", item.status.replaceAll("-", " ")),
    make("span", "work-card__id", item.id),
  );
  link.append(
    meta,
    make("h4", "", item.title),
    make("p", "work-card__program", `${item.program_title} · ${item.program_slug}`),
  );
  if (item.reasons && item.reasons.length > 0) {
    link.append(make("p", "work-card__reasons", item.reasons.join(" · ")));
  }
  return link;
}

function renderPrograms(programs) {
  dom.programCount.textContent = String(programs.length);
  dom.programList.replaceChildren(...programs.map(programCard));
  dom.programEmpty.hidden = programs.length !== 0;
}

function renderWork(work) {
  dom.workCount.textContent = String(work.length);
  dom.workEmpty.hidden = work.length !== 0;
  STATUSES.forEach((status) => {
    const items = work.filter((item) => item.status === status);
    byID(`lane-${status}-count`).textContent = String(items.length);
    byID(`lane-${status}-list`).replaceChildren(...items.map(workCard));
    byID(`lane-${status}-empty`).hidden = items.length !== 0;
  });
}

function renderDiagnostics(diagnostics) {
  dom.diagnosticCount.textContent = String(diagnostics.length);
  dom.diagnosticList.replaceChildren(
    ...diagnostics.map((diagnostic) =>
      make("li", "", `${diagnostic.directory}: ${diagnostic.message}`),
    ),
  );
  dom.diagnostics.hidden = diagnostics.length === 0;
}

function render(snapshot) {
  const focusedKey = document.activeElement?.dataset.focusKey || "";
  const scrollX = window.scrollX;
  const scrollY = window.scrollY;
  renderPrograms(snapshot.programs || []);
  renderWork(snapshot.work || []);
  renderDiagnostics(snapshot.diagnostics || []);
  const focusTarget = Array.from(document.querySelectorAll("[data-focus-key]")).find(
    (node) => node.dataset.focusKey === focusedKey,
  );
  if (focusTarget) {
    focusTarget.focus({ preventScroll: true });
  }
  window.scrollTo(scrollX, scrollY);
  const refreshed = new Date(snapshot.generated_at).toLocaleTimeString();
  if (snapshot.refresh.status === "failed") {
    dom.connectionStatus.textContent = `Showing the last local snapshot. ${snapshot.refresh.error}`;
    return;
  }
  dom.connectionStatus.textContent = `Updated ${refreshed}`;
}

async function refresh() {
  try {
    const response = await fetch("api/overview", {
      cache: "no-store",
      headers: { Accept: "application/json" },
    });
    if (!response.ok) {
      throw new Error(`Overview request failed with ${response.status}`);
    }
    const snapshot = await response.json();
    if (snapshot.schema !== "relay.program.overview.v1") {
      throw new Error(`Unsupported overview schema ${snapshot.schema}`);
    }
    render(snapshot);
  } catch (error) {
    dom.connectionStatus.textContent = `Unable to refresh. ${error.message}`;
  }
}

refresh();
window.setInterval(refresh, 3000);
