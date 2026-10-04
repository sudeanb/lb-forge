"use strict";
async function poll() {
  try {
    const s = await (await fetch("/lb/status")).json();
    document.querySelector("#strategy").textContent = "strategy: " + s.strategy;
    document.querySelector("#uptime").textContent = "up " + s.uptime_sec + "s";
    document.querySelector("#backends tbody").innerHTML = s.backends.map(b =>
      `<tr><td>${b.addr}</td>` +
      `<td class="${b.healthy ? "ok" : "bad"}">${b.state}</td>` +
      `<td>${b.conns}</td><td>${b.reqs}</td><td>${b.fails}</td></tr>`).join("");
    document.querySelector("#totals").textContent =
      `${s.total_requests} requests · ${s.backend_failures} backend failures · ${s.retries} retries`;
  } catch {}
}
setInterval(poll, 1000);
poll();
