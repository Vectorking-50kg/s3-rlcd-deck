"use strict";

const statusElement = document.querySelector("#runtime-status");

fetch("/api/v1/status", { cache: "no-store" })
  .then((response) => {
    if (!response.ok) {
      throw new Error(`status request failed: ${response.status}`);
    }
    return response.json();
  })
  .then((status) => {
    statusElement.textContent = `${status.state} · ${status.version}`;
  })
  .catch(() => {
    statusElement.textContent = "Unavailable";
  });
