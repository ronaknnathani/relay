"use strict";

(() => {
  const graphNodes = document.getElementById("graph-nodes");
  const graph = document.getElementById("graph");
  const graphEdges = document.getElementById("graph-edges");
  const roadmapContent = document.getElementById("roadmap-content");
  const reconnect = document.getElementById("reconnect");
  const refresh = document.getElementById("refresh");
  const themeToggle = document.getElementById("theme-toggle");
  const themeGlyph = document.getElementById("theme-glyph");
  const themeText = document.getElementById("theme-text");
  const initial = document.getElementById("initial-program");
  const snapshot = initial ? JSON.parse(initial.textContent) : null;
  const graphData = snapshot && snapshot.graph ? snapshot.graph : {};
  const status = {
    pending: ["○", "Pending"],
    dispatched: ["▶", "Dispatched"],
    "in-review": ["◆", "In review"],
    blocked: ["✕", "Blocked"],
    merged: ["●", "Merged"],
    cancelled: ["⊘", "Cancelled"],
  };
  const buildRoadmap = (snapshot) => {
    const graph = snapshot && snapshot.graph ? snapshot.graph : {};
    const nodes = Array.isArray(graph.nodes) ? graph.nodes : [];
    const nodesByID = new Map(nodes.map((node) => [node.id, node]));
    const layers = Array.isArray(graph.layers) && graph.layers.length > 0
      ? graph.layers
      : nodes.reduce((grouped, node) => {
        while (grouped.length <= Number(node.layer || 0)) {
          grouped.push([]);
        }
        grouped[Number(node.layer || 0)].push(node.id);
        return grouped;
      }, []);
    const fragment = new DocumentFragment();
    let position = 0;
    layers.forEach((layer, stageIndex) => {
      const ids = layer.filter((id) => nodesByID.has(id));
      if (ids.length === 0) {
        return;
      }
      const stage = document.createElement("div");
      stage.className = ids.length === 1 ? "stage stage--single" : "stage";
      stage.dataset.stage = String(stageIndex);
      stage.dataset.label = `Stage ${stageIndex + 1} · ${ids.length} task${ids.length === 1 ? "" : "s"}`;
      ids.forEach((id) => {
        const node = nodesByID.get(id);
        const meta = status[node.lane] || ["·", "Unknown"];
        let facts = node.priority || "";
        if (node.dependency_count > 0) {
          facts += ` · ${node.dependency_count} dep${node.dependency_count === 1 ? "" : "s"}`;
        }
        if (node.pr_number > 0) {
          facts += ` · PR #${node.pr_number}`;
        }
        if (node.orphaned) {
          facts += " · orphan";
        } else if (node.ready) {
          facts += " · ready";
        }
        const dependencies = Array.isArray(node.dependencies) ? node.dependencies : [];
        const dependencyLabel = dependencies.length > 0
          ? `Dependencies: ${dependencies.join(", ")}`
          : "No dependencies";
        const card = document.createElement("button");
        card.className = "card";
        card.type = "button";
        card.dataset.item = node.id;
        card.dataset.focusKey = `card:${node.id}`;
        card.dataset.stage = String(stageIndex);
        card.dataset.lane = node.lane;
        card.dataset.selected = "false";
        card.tabIndex = position === 0 ? 0 : -1;
        card.setAttribute(
          "aria-label",
          `Task ${node.id}: ${node.title}. Status ${meta[1]}. ` +
            `Priority ${node.priority}. ${dependencyLabel}.`,
        );
        card.textContent = `${node.title}\n${node.id} · ${meta[0]} ${meta[1]}\n${facts}`;
        stage.append(card);
        position += 1;
      });
      fragment.append(stage);
    });
    graphNodes.replaceChildren(fragment);
  };
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
  const renderThemeToggle = () => {
    const dark = document.documentElement.dataset.theme === "dark";
    themeGlyph.textContent = dark ? "☀" : "☾";
    themeText.textContent = dark ? "Light" : "Dark";
    themeToggle.setAttribute("aria-label", dark ? "Switch to light theme" : "Switch to dark theme");
  };
  const onThemeToggle = () => {
    const next = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
    document.documentElement.dataset.theme = next;
    try {
      window.localStorage.setItem("relay.program.theme", next);
    } catch (_) {
      // Theme switching remains available when local storage is unavailable.
    }
    renderThemeToggle();
  };
  const onRefresh = () => window.location.reload();

  buildRoadmap(snapshot);
  graphNodes.addEventListener("click", onClick);
  graphNodes.addEventListener("keydown", onKeyDown);
  themeToggle.addEventListener("click", onThemeToggle);
  refresh.addEventListener("click", onRefresh);
  window.__relayRoadmapCoreCleanup = () => {
    graphNodes.removeEventListener("click", onClick);
    graphNodes.removeEventListener("keydown", onKeyDown);
    themeToggle.removeEventListener("click", onThemeToggle);
    refresh.removeEventListener("click", onRefresh);
  };
  renderThemeToggle();
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
    const edges = Array.isArray(graphData.edges) ? graphData.edges : [];
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

  const loadFullApp = () => {
    const script = document.createElement("script");
    script.src = "/app.js";
    script.onerror = () => {
      reconnect.hidden = false;
      reconnect.textContent = "The complete Program UI bundle failed to load. Reload the page to retry.";
    };
    document.body.append(script);
  };

  drawConnectors();
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", loadFullApp, { once: true });
  } else {
    loadFullApp();
  }
})();
