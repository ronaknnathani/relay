"use strict";

(() => {
  const initial = document.getElementById("initial-program");
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

  const renderSnapshot = (current) => {
    if (!current || window.__relayCoreReady) {
      return;
    }
    const snapshot = current;
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

    const visibleNodes = graphNodes.querySelectorAll(".card").length > 0
      ? []
      : nodes.slice(0, 2);
    const visibleIDs = new Set(visibleNodes.map((node) => node.id));
    const byID = new Map(visibleNodes.map((node) => [node.id, node]));
    let stages = values(graphData.layers)
      .map((layer) => values(layer).filter((id) => visibleIDs.has(id)))
      .filter((layer) => layer.length > 0);
    if (stages.length === 0) {
      const grouped = [];
      visibleNodes.forEach((node) => {
        const layer = Math.max(0, count(node.layer));
        while (grouped.length <= layer) {
          grouped.push([]);
        }
        grouped[layer].push(node.id);
      });
      stages = grouped.filter((layer) => layer.length > 0);
    }
    const fragment = new DocumentFragment();
    let position = 0;
    stages.forEach((ids, stageIndex) => {
      const stage = document.createElement("div");
      stage.className = "stage";
      stage.dataset.stage = String(stageIndex);
      stage.dataset.label = `Stage ${stageIndex + 1} · ${plural(ids.length, "task")}`;
      ids.forEach((id) => {
        const node = byID.get(id);
        const card = document.createElement("button");
        card.className = "card";
        card.type = "button";
        card.dataset.item = node.id;
        card.dataset.focusKey = `card:${node.id}`;
        card.dataset.stage = String(stageIndex);
        card.dataset.lane = node.lane || "pending";
        card.tabIndex = position === 0 ? 0 : -1;
        card.textContent = `${node.title || "Untitled task"}\n${node.id}`;
        stage.append(card);
        position += 1;
      });
      fragment.append(stage);
    });
    if (visibleNodes.length > 0) {
      graphNodes.replaceChildren(fragment);
    }
  };
  if (snapshot) {
    renderSnapshot(snapshot);
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

  window.__relayBootstrapCleanup = () => {
    graphNodes.removeEventListener("click", onClick);
    graphNodes.removeEventListener("keydown", onKeyDown);
  };
})();
