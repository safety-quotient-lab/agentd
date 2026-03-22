// ═══ Agent Chrome — Station Switching + SSE + Startup ═══════
// Per-agent LCARS dashboard controller. Manages station tabs,
// SSE subscription for real-time updates, and catalog init.

(function () {
    "use strict";

    var activeStation = "vitals";
    var eventSource = null;
    var refreshInterval = null;

    // ── Station switching ────────────────────────────────────
    window.switchStation = function (station) {
        activeStation = station;

        // Update sidebar buttons
        var buttons = document.querySelectorAll(".lcars-sidebar-btn");
        for (var i = 0; i < buttons.length; i++) {
            var btn = buttons[i];
            if (btn.dataset.tab === station) {
                btn.classList.add("active");
            } else {
                btn.classList.remove("active");
            }
        }

        // Update tab panes
        var panes = document.querySelectorAll(".tab-pane");
        for (var j = 0; j < panes.length; j++) {
            var pane = panes[j];
            if (pane.id === "pane-" + station) {
                pane.classList.add("active");
            } else {
                pane.classList.remove("active");
            }
        }

        // Update URL without navigation
        var url = new URL(location);
        url.searchParams.set("station", station);
        history.replaceState(null, "", url);

        // Fetch data for the active station
        refreshStation(station);
    };

    // ── Station data refresh ────────────────────────────────
    function refreshStation(station) {
        switch (station) {
            case "vitals":
                if (window.refreshVitals) window.refreshVitals();
                break;
            case "knowledge":
                if (window.refreshKnowledge) window.refreshKnowledge();
                break;
            case "architecture":
                if (window.refreshArchitecture) window.refreshArchitecture();
                break;
            case "transport":
                if (window.refreshTransport) window.refreshTransport();
                break;
            case "governance":
                if (window.refreshGovernance) window.refreshGovernance();
                break;
            case "integrity":
                if (window.refreshIntegrity) window.refreshIntegrity();
                break;
        }
    }

    // ── Real-time connection (WebSocket preferred, SSE fallback) ──
    var ws = null;
    function connectRealtime() {
        // Prefer WebSocket for Cloudflare Tunnel compatibility
        var wsProto = location.protocol === "https:" ? "wss:" : "ws:";
        var wsUrl = wsProto + "//" + location.host + "/ws";

        try {
            ws = new WebSocket(wsUrl);
            ws.onmessage = function (evt) {
                try {
                    var data = JSON.parse(evt.data);
                    if (data.event === "refresh") {
                        refreshStation(activeStation);
                    }
                } catch (e) {
                    // Non-JSON message (ping) — ignore
                }
            };
            ws.onclose = function () {
                console.warn("[ws] Connection closed, reconnecting in 5s...");
                ws = null;
                setTimeout(connectRealtime, 5000);
            };
            ws.onerror = function () {
                // WebSocket failed — fall back to SSE
                console.warn("[ws] WebSocket failed, falling back to SSE");
                ws.close();
                ws = null;
                connectSSE();
            };
        } catch (e) {
            // WebSocket not available — use SSE
            connectSSE();
        }
    }

    function connectSSE() {
        var sseUrl = "/events";
        if (eventSource) eventSource.close();

        eventSource = new EventSource(sseUrl);
        eventSource.onmessage = function () {
            refreshStation(activeStation);
        };
        eventSource.onerror = function () {
            console.warn("[sse] Connection lost, reconnecting...");
        };
    }

    // ── Agent header update ─────────────────────────────────
    function updateHeader(agentData) {
        var titleEl = document.getElementById("agent-title");
        if (titleEl && agentData.agent_id) {
            titleEl.textContent = agentData.agent_id.toUpperCase().replace(/-/g, " ");
        }
        var badgeEl = document.getElementById("agent-coherence-badge");
        if (badgeEl && agentData.coherence != null) {
            badgeEl.textContent = "COH " + agentData.coherence.toFixed(2);
        }
    }

    // ── Periodic refresh (fallback when SSE unavailable) ────
    function startPeriodicRefresh() {
        if (refreshInterval) clearInterval(refreshInterval);
        refreshInterval = setInterval(function () {
            refreshStation(activeStation);
        }, 15000); // 15s fallback
    }

    // ── Startup ─────────────────────────────────────────────
    function init() {
        // Load catalog
        lcars.catalog.load("").then(function () {
            // Restore station from URL
            var params = new URLSearchParams(location.search);
            var station = params.get("station");
            if (station && document.getElementById("pane-" + station)) {
                switchStation(station);
            } else {
                refreshStation(activeStation);
            }

            // Fetch agent root for header
            lcars.catalog.fetch("Agent Identity").then(function (data) {
                updateHeader(data);
                // Alert palette check from coherence
                if (data.coherence != null) {
                    lcars.patterns.alertCheck(data.coherence);
                }
            }).catch(function () {
                // Agent identity unavailable — use defaults
            });

            // Connect real-time (WebSocket preferred, SSE fallback)
            connectRealtime();

            // Fallback periodic refresh
            startPeriodicRefresh();
        });
    }

    // Run on DOM ready
    if (document.readyState === "loading") {
        document.addEventListener("DOMContentLoaded", init);
    } else {
        init();
    }
})();
