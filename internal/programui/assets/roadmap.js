"use strict";

(() => {
  const graphNodes = document.getElementById("graph-nodes");
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
  document.documentElement.dataset.relayCoreReady = "true";

  requestAnimationFrame(() => setTimeout(() => {
    const script = document.createElement("script");
    script.src = "/app.js";
    script.onerror = () => {
      reconnect.hidden = false;
      reconnect.textContent = "The complete Program UI bundle failed to load. Reload the page to retry.";
    };
    document.body.append(script);
  }, 0));
})();
