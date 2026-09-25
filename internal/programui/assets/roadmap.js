"use strict";

(() => {
  const graphNodes = document.getElementById("graph-nodes");
  const graph = document.getElementById("graph");
  const graphEdges = document.getElementById("graph-edges");
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
      stage.className = "stage";
      stage.dataset.stage = String(stageIndex);
      stage.dataset.label = `Stage ${stageIndex + 1} · ${ids.length} task${ids.length === 1 ? "" : "s"}`;
      ids.forEach((id) => {
        const node = nodesByID.get(id);
        const meta = status[node.lane] || ["·", "Unknown"];
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

        const top = document.createElement("div");
        top.className = "card__top";
        const taskID = document.createElement("span");
        taskID.className = "card__id";
        taskID.textContent = node.id;
        const state = document.createElement("span");
        state.className = "status";
        const glyph = document.createElement("span");
        glyph.className = "status__glyph";
        glyph.textContent = meta[0];
        const word = document.createElement("span");
        word.className = "status__word";
        word.textContent = meta[1];
        state.append(glyph, word);
        top.append(taskID, state);

        const title = document.createElement("p");
        title.className = "card__title";
        title.textContent = node.title;

        const foot = document.createElement("div");
        foot.className = "card__foot";
        const priority = document.createElement("span");
        priority.textContent = node.priority || "P?";
        foot.append(priority);
        if (node.dependency_count > 0) {
          const dependencyCount = document.createElement("span");
          dependencyCount.textContent =
            `${node.dependency_count} dep${node.dependency_count === 1 ? "" : "s"}`;
          foot.append(dependencyCount);
        }
        if (node.pr_number > 0) {
          const pullRequest = document.createElement("span");
          pullRequest.className = "card__pr";
          pullRequest.textContent = `PR #${node.pr_number}`;
          foot.append(pullRequest);
        }
        if (node.orphaned || node.ready) {
          const flag = document.createElement("span");
          flag.className = node.orphaned ? "flag flag--orphan" : "flag flag--ready";
          flag.textContent = node.orphaned ? "orphan" : "ready";
          foot.append(flag);
        }
        card.append(top, title, foot);
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

  const drawInitialConnectors = () => {
    if (!initialConnectors) {
      return;
    }
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
  };

  const loadFullApp = () => {
    const script = document.createElement("script");
    script.src = "app.js";
    script.onerror = () => {
      reconnect.hidden = false;
      reconnect.textContent = "The complete Program UI bundle failed to load. Reload the page to retry.";
    };
    document.body.append(script);
  };
  const loadFullAppAfterPaint = () => {
    requestAnimationFrame(() => setTimeout(loadFullApp, 0));
  };

  drawInitialConnectors();
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", loadFullAppAfterPaint, { once: true });
  } else {
    loadFullAppAfterPaint();
  }
})();
