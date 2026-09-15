"use strict";

(() => {
  const initial = document.getElementById("initial-program");
  const appScript = document.getElementById("app-script");
  const reconnect = document.getElementById("reconnect");
  const graphNodes = document.getElementById("graph-nodes");
  const graphEdges = document.getElementById("graph-edges");
  const roadmapContent = document.getElementById("roadmap-content");
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
  const round = (value) => Math.round(value * 10) / 10;
  const downwardPath = (from, to) => {
    const y1 = from.bottom + 1;
    const y2 = to.top - 7;
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
  };

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

    const visibleNodes = nodes.length <= 128 ? nodes : nodes.slice(0, 2);
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
    graphNodes.replaceChildren(fragment);

    if (visibleNodes.length === nodes.length) {
      const base = roadmapContent.getBoundingClientRect();
      const boxes = new Map();
      Array.from(graphNodes.querySelectorAll(".card")).forEach((card) => {
        const box = card.getBoundingClientRect();
        boxes.set(card.dataset.item, {
          center: box.left - base.left + box.width / 2,
          top: box.top - base.top,
          bottom: box.bottom - base.top,
        });
      });
      const edgeFragment = new DocumentFragment();
      edges.forEach((edge) => {
        const from = boxes.get(edge.from);
        const to = boxes.get(edge.to);
        if (!from || !to) {
          return;
        }
        const downward = to.top > from.bottom + 4;
        const path = document.createElementNS(graph.namespaceURI, "path");
        path.setAttribute("class", downward ? "edge" : "edge edge--back");
        path.setAttribute("d", downward
          ? downwardPath(from, to)
          : `M ${round(from.center)} ${round(from.bottom + 1)} L ${round(to.center)} ${round(to.top - 7)}`);
        path.setAttribute("marker-end", "url(#flow-arrow)");
        path.dataset.from = edge.from;
        path.dataset.to = edge.to;
        path.dataset.downward = downward ? "true" : "false";
        edgeFragment.append(path);
      });
      const width = Math.ceil(base.width);
      const height = Math.ceil(base.height);
      graph.setAttribute("width", String(width));
      graph.setAttribute("height", String(height));
      graph.setAttribute("viewBox", `0 0 ${width} ${height}`);
      graphEdges.replaceChildren(edgeFragment);
    }
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
  appScript.addEventListener("error", () => {
    reconnect.hidden = false;
    reconnect.textContent = "The Program UI bundle failed to load. Reload the page to retry.";
  });
})();
