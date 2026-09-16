"use strict";

(() => {
  const graphNodes = document.getElementById("graph-nodes");
  const graph = document.getElementById("graph");
  const graphEdges = document.getElementById("graph-edges");
  const roadmapScroll = document.getElementById("roadmap-scroll");
  const roadmapContent = document.getElementById("roadmap-content");
  const reconnect = document.getElementById("reconnect");
  const refresh = document.getElementById("refresh");
  const themeToggle = document.getElementById("theme-toggle");
  const themeGlyph = document.getElementById("theme-glyph");
  const themeText = document.getElementById("theme-text");
  const initial = document.getElementById("initial-program");
  const snapshot = initial ? JSON.parse(initial.textContent) : null;
  const compactStages = snapshot && Array.isArray(snapshot.stages) ? snapshot.stages : [];
  const graphData = {
    nodes: [],
    edges: snapshot && Array.isArray(snapshot.edges)
      ? snapshot.edges.map((edge) => ({ from: edge[0], to: edge[1] }))
      : [],
    layers: [],
    cyclic: Boolean(snapshot && snapshot.cyclic),
  };
  compactStages.forEach((stage, layer) => {
    const ids = [];
    stage.forEach((entry) => {
      ids.push(entry.i);
      graphData.nodes.push({
        id: entry.i,
        title: entry.t,
        lane: entry.l,
        layer,
        priority: entry.p || "",
        dependencies: Array.isArray(entry.d) ? entry.d : [],
        dependency_count: Array.isArray(entry.d) ? entry.d.length : 0,
        pr_number: Number(entry.r) || 0,
        ready: Boolean(entry.y),
        orphaned: Boolean(entry.o),
      });
    });
    graphData.layers.push(ids);
  });
  if (snapshot) {
    snapshot.graph = graphData;
  }
  const initialConnectors = snapshot && snapshot.initial_connectors
    ? snapshot.initial_connectors
    : null;
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
    roadmapScroll.removeEventListener("scroll", onRoadmapScroll);
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
  const renderConnectors = (boxes, coordinateWidth, height, stretch) => {
    const edges = Array.isArray(graphData.edges) ? graphData.edges : [];
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
    graph.setAttribute("width", stretch ? "100%" : String(coordinateWidth));
    graph.setAttribute("height", String(height));
    graph.setAttribute("viewBox", `0 0 ${coordinateWidth} ${height}`);
    if (stretch) {
      graph.setAttribute("preserveAspectRatio", "none");
    } else {
      graph.removeAttribute("preserveAspectRatio");
    }
    graphEdges.replaceChildren(fragment);
    window.__relayRoadmapConnectorPaths = paths;
  };
  const drawAccurateConnectors = () => {
    const edges = Array.isArray(graphData.edges) ? graphData.edges : [];
    const base = roadmapContent.getBoundingClientRect();
    if (base.width === 0 || edges.length === 0) {
      return;
    }
    const boxes = new Map(cards().map((card) => {
      const box = card.getBoundingClientRect();
      return [card.dataset.item, {
        center: box.left - base.left + box.width / 2,
        top: box.top - base.top,
        bottom: box.bottom - base.top,
      }];
    }));
    renderConnectors(boxes, Math.ceil(base.width), Math.ceil(base.height), false);
  };
  const drawInitialConnectors = () => {
    if (initialConnectors) {
      const fragment = new DocumentFragment();
      [
        [initialConnectors.normal_path, initialConnectors.normal_count, "edge"],
        [initialConnectors.back_path, initialConnectors.back_count, "edge edge--back"],
      ].forEach(([pathData, count, className]) => {
        if (!pathData || !count) {
          return;
        }
        const path = document.createElementNS(graph.namespaceURI, "path");
        path.setAttribute("class", className);
        path.setAttribute("d", pathData);
        path.setAttribute("marker-end", "url(#flow-arrow)");
        path.dataset.edgeCount = String(count);
        fragment.append(path);
      });
      graph.setAttribute("width", "100%");
      graph.setAttribute("height", String(initialConnectors.height));
      graph.setAttribute(
        "viewBox",
        `0 0 ${initialConnectors.width} ${initialConnectors.height}`,
      );
      graph.setAttribute("preserveAspectRatio", "none");
      graphEdges.replaceChildren(fragment);
      return;
    }
    const stages = Array.from(graphNodes.children);
    const allSingle = stages.length > 0 &&
      stages.every((stage) => stage.classList.contains("stage--single"));
    if (!allSingle) {
      drawAccurateConnectors();
      return;
    }
    const width = 1000;
    const stageHeight = 132;
    const cardTop = 34;
    const cardHeight = 98;
    const gap = window.innerWidth <= 560 ? 20 : 26;
    const boxes = new Map(stages.map((stage, index) => {
      const card = stage.querySelector(".card");
      const top = index * (stageHeight + gap) + cardTop;
      return [card.dataset.item, {
        center: width / 2,
        top,
        bottom: top + cardHeight,
      }];
    }));
    const height = stages.length * stageHeight + Math.max(0, stages.length - 1) * gap;
    renderConnectors(boxes, width, height, true);
  };
  const onRoadmapScroll = () => drawAccurateConnectors();
  roadmapScroll.addEventListener("scroll", onRoadmapScroll, { once: true });

  const loadFullApp = () => {
    const script = document.createElement("script");
    script.src = "/app.js";
    script.onerror = () => {
      reconnect.hidden = false;
      reconnect.textContent = "The complete Program UI bundle failed to load. Reload the page to retry.";
    };
    document.body.append(script);
  };

  drawInitialConnectors();
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", loadFullApp, { once: true });
  } else {
    loadFullApp();
  }
})();
