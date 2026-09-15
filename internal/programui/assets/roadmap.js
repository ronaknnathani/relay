"use strict";

(() => {
  const graphNodes = document.getElementById("graph-nodes");
  const graph = document.getElementById("graph");
  const graphEdges = document.getElementById("graph-edges");
  const roadmapContent = document.getElementById("roadmap-content");
  const reconnect = document.getElementById("reconnect");
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
    window.__relayRoadmapSelection = card.dataset.item;
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
  window.__relayRoadmapCoreCleanup = () => {
    graphNodes.removeEventListener("click", onClick);
    graphNodes.removeEventListener("keydown", onKeyDown);
  };
  window.__relayCoreReady = true;
  window.__relayRoadmapSelection =
    document.querySelector('.card[data-selected="true"]')?.dataset.item || "";
  document.documentElement.dataset.relayCoreReady = "true";

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
  const drawConnectors = () => {
    const initial = document.getElementById("initial-program");
    const snapshot = initial ? JSON.parse(initial.textContent) : null;
    const edges = snapshot && snapshot.graph ? snapshot.graph.edges || [] : [];
    const width = roadmapContent.clientWidth;
    if (width === 0 || edges.length === 0) {
      return;
    }
    const boxes = new Map(cards().map((card) => {
      const stage = card.parentElement;
      const left = stage.offsetLeft + card.offsetLeft;
      const top = stage.offsetTop + card.offsetTop;
      return [card.dataset.item, {
        center: left + card.offsetWidth / 2,
        top,
        bottom: top + card.offsetHeight,
      }];
    }));
    const paths = [];
    edges.forEach((edge) => {
      const from = boxes.get(edge.from);
      const to = boxes.get(edge.to);
      if (!from || !to) {
        return;
      }
      const downward = to.top > from.bottom + 4;
      paths.push({
        from: edge.from,
        to: edge.to,
        downward,
        path: downward
          ? downwardPath(from, to)
          : `M ${round(from.center)} ${round(from.bottom + 1)} L ${round(to.center)} ${round(to.top - 7)}`,
      });
    });
    const grouped = {
      normal: paths.filter((edge) => edge.downward),
      back: paths.filter((edge) => !edge.downward),
    };
    const fragment = new DocumentFragment();
    [["normal", "edge"], ["back", "edge edge--back"]].forEach(([name, className]) => {
      if (grouped[name].length === 0) {
        return;
      }
      const path = document.createElementNS(graph.namespaceURI, "path");
      path.setAttribute("class", className);
      path.setAttribute("d", grouped[name].map((edge) => edge.path).join(" "));
      path.setAttribute("marker-end", "url(#flow-arrow)");
      path.dataset.edgeCount = String(grouped[name].length);
      fragment.append(path);
    });
    const height = roadmapContent.scrollHeight;
    graph.setAttribute("width", String(width));
    graph.setAttribute("height", String(height));
    graph.setAttribute("viewBox", `0 0 ${width} ${height}`);
    graphEdges.replaceChildren(fragment);
    window.__relayRoadmapConnectorPaths = paths;
  };

  drawConnectors();
  const script = document.createElement("script");
  script.src = "/app.js";
  script.onerror = () => {
    reconnect.hidden = false;
    reconnect.textContent = "The complete Program UI bundle failed to load. Reload the page to retry.";
  };
  document.body.append(script);
})();
