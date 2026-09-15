"use strict";

(() => {
  const initial = document.getElementById("initial-program");
  const reconnect = document.getElementById("reconnect");
  const graphNodes = document.getElementById("graph-nodes");
  const graph = document.getElementById("graph");
  const snapshot = initial ? JSON.parse(initial.textContent) : null;

  const setText = (id, value) => {
    const node = document.getElementById(id);
    if (node) {
      node.textContent = String(value || "");
    }
  };
  const values = (value) => Array.isArray(value) ? value : [];
  const count = (value) => Number.isFinite(Number(value)) ? Number(value) : 0;
  const plural = (value, word) => `${value} ${word}${value === 1 ? "" : "s"}`;

  if (snapshot) {
    const program = snapshot.program || {};
    const progress = snapshot.progress || {};
    const plan = snapshot.plan || {};
    const graphData = snapshot.graph || {};
    const nodes = values(graphData.nodes);
    const edges = values(graphData.edges);
    setText("program-title", program.display_title || program.title || "Relay Program");
    setText("program-summary", program.summary);
    setText("program-slug", program.slug);
    setText("task-total", count(progress.total));
    setText(
      "progress-counts",
      `${count(progress.merged)} of ${count(progress.total)} merged`,
    );
    setText(
      "roadmap-note",
      `${values(plan.ready).length} ready · ${values(plan.in_flight).length} in flight · ` +
        `${values(plan.blocked).length} blocked`,
    );
    graph.setAttribute(
      "aria-label",
      `Dependency flow: ${plural(nodes.length, "task")}, ${plural(edges.length, "dependency link")}.`,
    );

    const stage = document.createElement("div");
    stage.className = "stage";
    stage.dataset.stage = "0";
    nodes.slice(0, 2).forEach((node, index) => {
      const card = document.createElement("button");
      card.className = "card";
      card.type = "button";
      card.dataset.item = node.id;
      card.dataset.focusKey = `card:${node.id}`;
      card.dataset.stage = "0";
      card.dataset.lane = node.lane || "pending";
      card.tabIndex = index === 0 ? 0 : -1;
      card.textContent = `${node.title || "Untitled task"}\n${node.id}`;
      stage.append(card);
    });
    graphNodes.replaceChildren(stage);
  }

  const cards = () => Array.from(graphNodes.querySelectorAll(".card"));
  const select = (card, persist) => {
    if (!card) {
      return;
    }
    cards().forEach((candidate) => {
      const active = candidate === card;
      candidate.dataset.selected = active ? "true" : "false";
      candidate.tabIndex = active ? 0 : -1;
    });
    card.focus({ preventScroll: true });
    if (persist) {
      window.history.replaceState(null, "", `#task=${encodeURIComponent(card.dataset.item)}`);
    }
  };
  const onClick = (event) => select(event.target.closest(".card"), true);
  const onKeyDown = (event) => {
    const card = event.target.closest(".card");
    if (!card || !["ArrowRight", "ArrowLeft", "Home", "End"].includes(event.key)) {
      return;
    }
    event.preventDefault();
    const current = cards();
    const index = current.indexOf(card);
    const target = event.key === "Home"
      ? current[0]
      : event.key === "End"
        ? current[current.length - 1]
        : current[Math.max(0, Math.min(current.length - 1,
          index + (event.key === "ArrowRight" ? 1 : -1)))];
    select(target, false);
  };
  graphNodes.addEventListener("click", onClick);
  graphNodes.addEventListener("keydown", onKeyDown);

  const script = document.createElement("script");
  script.src = "/app.js";
  script.onload = () => {
    graphNodes.removeEventListener("click", onClick);
    graphNodes.removeEventListener("keydown", onKeyDown);
  };
  script.onerror = () => {
    reconnect.hidden = false;
    reconnect.textContent = "The Program UI bundle failed to load. Reload the page to retry.";
  };
  document.body.append(script);
})();
