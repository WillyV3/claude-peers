// ========== PAGE 6: SECURITY ==========
// Per-machine security status from Wazuh EDR + broker health scoring.

const SEC_MACHINES = ['omarchy', 'ubuntu-homelab', 'raspdeck', 'thinkbook', 'willyv4', 'macbook1'];

const SEC_DISPLAY = {
  'omarchy': 'OMARCHY',
  'ubuntu-homelab': 'U-HOMELAB',
  'raspdeck': 'RASPDECK',
  'thinkbook': 'THINKBOOK',
  'thinkbook-omarchy': 'THINKBOOK',
  'willyv4': 'WILLYV4',
  'macbook1': 'MACBOOK',
};

function updateSecurityPage(healthMap) {
  const el = document.getElementById('sec-layout');
  if (!el) return;

  if (!healthMap || Object.keys(healthMap).length === 0) {
    // No security events -- show all machines as secure
    el.innerHTML = `
      <div class="sec-header">
        <span class="sec-title">FLEET SECURITY</span>
        <span class="sec-summary sec-ok">ALL CLEAR</span>
      </div>
      <div class="sec-grid">${SEC_MACHINES.map(id => {
        const name = SEC_DISPLAY[id] || id;
        return `<div class="sec-card ok">
          <div class="sec-card-hdr">
            <span class="sec-dot ok"></span>
            <span class="sec-name">${name}</span>
            <span class="sec-status-label ok">SECURE</span>
          </div>
          <div class="sec-card-body">
            <div class="sec-metric"><span class="sec-lbl">SCORE</span><span class="sec-val">0</span></div>
            <div class="sec-metric"><span class="sec-lbl">STATUS</span><span class="sec-val ok">healthy</span></div>
            <div class="sec-metric"><span class="sec-lbl">EVENTS</span><span class="sec-val">none</span></div>
          </div>
        </div>`;
      }).join('')}</div>`;
    return;
  }

  // Count statuses
  const statuses = { healthy: 0, degraded: 0, quarantined: 0 };
  for (const id of SEC_MACHINES) {
    const h = healthMap[id] || healthMap[normalizeMachine(id)];
    const status = h ? h.status : 'healthy';
    statuses[status] = (statuses[status] || 0) + 1;
  }

  const summaryClass = statuses.quarantined > 0 ? 'sec-quarantined'
    : statuses.degraded > 0 ? 'sec-degraded' : 'sec-ok';
  const summaryText = statuses.quarantined > 0 ? `${statuses.quarantined} QUARANTINED`
    : statuses.degraded > 0 ? `${statuses.degraded} DEGRADED`
    : 'ALL CLEAR';

  let html = `
    <div class="sec-header">
      <span class="sec-title">FLEET SECURITY</span>
      <span class="sec-summary ${summaryClass}">${summaryText}</span>
      <span class="sec-counts">
        <span class="sec-count-ok">${statuses.healthy}</span> ok
        <span class="sec-count-warn">${statuses.degraded}</span> degraded
        <span class="sec-count-crit">${statuses.quarantined}</span> quarantined
      </span>
    </div>
    <div class="sec-grid">`;

  for (const id of SEC_MACHINES) {
    const h = healthMap[id] || healthMap[normalizeMachine(id)];
    const name = SEC_DISPLAY[id] || id;
    const score = h ? h.score : 0;
    const status = h ? h.status : 'healthy';
    const lastEvent = h ? h.last_event_desc : '';
    const events = h ? (h.events || []) : [];
    const statusClass = status === 'quarantined' ? 'quarantined'
      : status === 'degraded' ? 'degraded' : 'ok';

    html += `<div class="sec-card ${statusClass}">
      <div class="sec-card-hdr">
        <span class="sec-dot ${statusClass}"></span>
        <span class="sec-name">${name}</span>
        <span class="sec-status-label ${statusClass}">${status === 'healthy' ? 'SECURE' : status.toUpperCase()}</span>
      </div>
      <div class="sec-card-body">
        <div class="sec-metric"><span class="sec-lbl">SCORE</span><span class="sec-val ${score > 0 ? 'warn' : ''}">${score}</span></div>
        <div class="sec-metric"><span class="sec-lbl">STATUS</span><span class="sec-val ${statusClass}">${status}</span></div>
        <div class="sec-metric"><span class="sec-lbl">LAST</span><span class="sec-val">${lastEvent ? esc(lastEvent).substring(0, 40) : 'none'}</span></div>
      </div>
      ${events.length > 0 ? `<div class="sec-events">${events.slice(0, 4).map(e =>
        `<div class="sec-event">${esc(e)}</div>`
      ).join('')}</div>` : ''}
    </div>`;
  }

  html += '</div>';
  el.innerHTML = html;
}
