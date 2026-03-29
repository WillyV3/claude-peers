// ========== PAGE 5: PEER NETWORK ==========
// Terminal-style network table. No canvas, pure DOM, pure information.

let peerMachines = {};
let peerList = [];
let lastPeerHTML = '';

function updatePeersPage(machines, peers) {
  peerMachines = machines || peerMachines || {};
  peerList = peers || peerList || [];
  renderPeerTable();
}

function renderPeerTable() {
  const el = document.getElementById('peer-layout');
  if (!el) return;

  const peers = peerList;
  const machines = peerMachines;

  // Group peers by normalized machine name
  const byMachine = {};
  for (const p of peers) {
    const mid = normalizeMachine(p.machine);
    if (!byMachine[mid]) byMachine[mid] = [];
    byMachine[mid].push(p);
  }

  // Build machine list: machines with peers first, then online without peers, then offline
  const allMachineIds = new Set();
  for (const id of Object.keys(machines)) allMachineIds.add(id);
  for (const id of Object.keys(byMachine)) allMachineIds.add(id);

  const machineList = Array.from(allMachineIds).sort((a, b) => {
    const aPeers = (byMachine[a] || []).length;
    const bPeers = (byMachine[b] || []).length;
    if (aPeers !== bPeers) return bPeers - aPeers; // most peers first
    const aOnline = machines[a] && machines[a].status === 'online';
    const bOnline = machines[b] && machines[b].status === 'online';
    if (aOnline !== bOnline) return aOnline ? -1 : 1;
    return a.localeCompare(b);
  });

  // Stats
  const totalPeers = peers.length;
  const activePeers = peers.filter(p => !p.last_seen || (Date.now() - new Date(p.last_seen).getTime()) <= 60000).length;
  const onlineMachines = Object.values(machines).filter(m => m.status === 'online').length;
  const machinesWithPeers = Object.keys(byMachine).length;

  // Header
  let html = `<div class="peer-header">
    <span class="peer-title">PEER NETWORK</span>
    <span class="peer-stats">
      <span class="peer-stat"><span class="peer-stat-n">${activePeers}</span> agents</span>
      <span class="peer-sep">/</span>
      <span class="peer-stat"><span class="peer-stat-n">${machinesWithPeers}</span> active</span>
      <span class="peer-sep">/</span>
      <span class="peer-stat"><span class="peer-stat-n">${onlineMachines}</span> online</span>
    </span>
    <span class="peer-nats">${natsConnected ? 'NATS LIVE' : 'NATS OFF'}</span>
  </div>`;

  // Machine sections
  html += '<div class="peer-table">';

  for (const mid of machineList) {
    const m = machines[mid];
    const online = m && m.status === 'online';
    const mPeers = byMachine[mid] || [];
    const displayName = (mid || '').toUpperCase().replace('UBUNTU-HOMELAB', 'U-HOMELAB').replace('-OMARCHY', '');

    // Machine role tag
    let role = '';
    if (mid === 'ubuntu-homelab') role = 'HUB';
    else if (mid === 'willyv4') role = 'DECK';
    else if (mid === 'raspdeck') role = 'KIOSK';
    else if (mid === 'sonias-mbp') role = 'LLM';

    // Machine header
    const statusClass = !online ? 'offline' : mPeers.length > 0 ? 'active' : 'idle';
    html += `<div class="peer-machine ${statusClass}">`;
    html += `<div class="peer-machine-hdr">`;
    html += `<span class="peer-machine-dot ${statusClass}"></span>`;
    html += `<span class="peer-machine-name">${esc(displayName)}</span>`;
    html += `<span class="peer-machine-line"></span>`;
    if (role) html += `<span class="peer-machine-role">${role}</span>`;
    if (mPeers.length > 0) {
      html += `<span class="peer-machine-count">${mPeers.length} session${mPeers.length > 1 ? 's' : ''}</span>`;
    } else if (online) {
      html += `<span class="peer-machine-count dim">no sessions</span>`;
    } else {
      html += `<span class="peer-machine-count off">offline</span>`;
    }
    html += `</div>`;

    // Peer rows
    if (mPeers.length > 0) {
      for (const p of mPeers) {
        const stale = p.last_seen && (Date.now() - new Date(p.last_seen).getTime()) > 60000;
        const tty = p.tty ? p.tty.replace('/dev/', '').replace('pts/', 'pts/') : '';
        const project = p.project || (p.git_root ? p.git_root.split('/').pop() : '') || '';
        const branch = p.branch || '';
        const cwd = shortenCwd(p.cwd || '');
        const summary = p.summary || '';
        const upSec = p.registered_at ? Math.floor((Date.now() - new Date(p.registered_at).getTime()) / 1000) : 0;
        const upStr = upSec > 86400 ? Math.floor(upSec/86400) + 'd'
          : upSec > 3600 ? Math.floor(upSec/3600) + 'h'
          : upSec > 60 ? Math.floor(upSec/60) + 'm'
          : upSec + 's';

        const rowClass = stale ? 'peer-row stale' : 'peer-row';

        html += `<div class="${rowClass}">`;
        html += `<span class="peer-dot ${stale ? 'stale' : 'live'}"></span>`;
        html += `<span class="peer-tty">${esc(tty || '·')}</span>`;

        // Project + branch
        if (project) {
          html += `<span class="peer-project">${esc(project)}</span>`;
          if (branch) html += `<span class="peer-branch">[${esc(branch)}]</span>`;
        } else {
          html += `<span class="peer-project dim">${esc(cwd || '~')}</span>`;
        }

        html += `<span class="peer-uptime">${upStr}</span>`;

        // Summary -- the main content
        if (summary) {
          html += `<span class="peer-summary">${esc(summary)}</span>`;
        }

        html += `</div>`;
      }
    } else if (mid === 'willyv4') {
      // Special willyv4 row -- show awareness data if available
      html += `<div class="peer-row willyv4-row">`;
      html += `<span class="peer-dot live"></span>`;
      html += `<span class="peer-tty">·</span>`;
      html += `<span class="peer-project">ghostbox</span>`;
      html += `<span class="peer-uptime">always</span>`;
      // Summary comes from willyv4 peer if registered
      const v4peer = peers.find(p => normalizeMachine(p.machine) === 'willyv4');
      if (v4peer && v4peer.summary) {
        html += `<span class="peer-summary">${esc(v4peer.summary)}</span>`;
      }
      html += `</div>`;
    }

    html += `</div>`;
  }

  html += '</div>';

  if (html !== lastPeerHTML) {
    el.innerHTML = html;
    lastPeerHTML = html;
  }
}

function shortenCwd(cwd) {
  return cwd
    .replace(/^\/home\/\w+\//, '~/')
    .replace(/^\/Users\/\w+\//, '~/')
    .replace(/^\/home\/\w+$/, '~')
    .replace(/^\/Users\/\w+$/, '~')
    .replace(/^\/root$/, '~')
    .replace(/^\/root\//, '~/');
}
