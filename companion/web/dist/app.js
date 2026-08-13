"use strict";

const statusElement = document.querySelector("#runtime-status");

fetch("/api/v1/bootstrap", { cache: "no-store" })
  .then((response) => {
    if (!response.ok) {
      throw new Error(`status request failed: ${response.status}`);
    }
    return response.json();
  })
  .then((bootstrap) => {
    const exposure = bootstrap.lan_management_enabled ? "LAN management enabled" : "Loopback only";
    return fetch("/api/v1/status", { cache: "no-store" })
      .then((response) => {
        if (response.status === 401) {
          statusElement.textContent = `${bootstrap.version} · Login required · ${exposure}`;
          return null;
        }
        if (!response.ok) {
          throw new Error(`runtime status request failed: ${response.status}`);
        }
        return response.json();
      })
      .then((runtime) => {
        if (runtime !== null) {
          const decks = runtime.connected_decks === 1 ? "1 Deck connected" : `${runtime.connected_decks} Decks connected`;
          statusElement.textContent = `${runtime.version} · ${runtime.state} · ${decks} · ${exposure}`;
        }
      });
  })
  .catch(() => {
    statusElement.textContent = "Unavailable";
  });
