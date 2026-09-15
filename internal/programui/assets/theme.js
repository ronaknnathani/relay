"use strict";

(() => {
  const key = "relay.program.theme";
  let theme = "light";
  try {
    const stored = window.localStorage.getItem(key);
    if (stored === "light" || stored === "dark") {
      theme = stored;
    }
  } catch (error) {
    /* Private browsing can refuse storage; the default remains usable. */
  }
  document.documentElement.dataset.theme = theme;
})();
