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
    statusElement.textContent = `${bootstrap.version} · Login required · ${exposure}`;
  })
  .catch(() => {
    statusElement.textContent = "Unavailable";
  });
