// The map: every building with records, clustered until street level.
// Settings come from data- attributes on #map; pins come from /map.json.
'use strict';
(function () {
  const el = document.getElementById('map');
  if (!el || !window.L) return;
  const status = document.getElementById('map-status');
  const still = matchMedia('(prefers-reduced-motion: reduce)').matches;
  const building = (id) => id.slice(0, id.lastIndexOf('|') + 1);
  const here = el.dataset.place ? building(el.dataset.place) : null;

  const map = L.map(el, {
    center: [+el.dataset.lat, +el.dataset.lon],
    zoom: +el.dataset.zoom,
    minZoom: 9,
    maxZoom: 19,
    maxBounds: [[34.5, -116.6], [37.6, -113.0]], // Clark County and a margin
    scrollWheelZoom: el.dataset.wheel !== 'off',
    zoomAnimation: !still,
    fadeAnimation: !still,
    markerZoomAnimation: !still,
  });
  L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', {
    maxZoom: 19,
    attribution: '© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors',
  }).addTo(map);
  const layer = L.layerGroup().addTo(map);

  function icon(cls, size, text) {
    const dot = document.createElement('span');
    if (text) dot.textContent = text;
    return L.divIcon({ className: 'pin ' + cls, html: dot, iconSize: [size, size] });
  }

  function count(n) {
    return n < 1000 ? String(n) : (n / 1000).toFixed(n < 10000 ? 1 : 0) + 'k';
  }

  function popup(p) {
    const box = document.createElement('div');
    box.className = 'popup';
    const title = document.createElement('strong');
    title.textContent = p.address;
    box.append(title);
    if (p.places > 1) {
      const units = document.createElement('span');
      units.textContent = p.places + ' addresses in this building';
      box.append(units);
    }
    const a = document.createElement('a');
    a.href = '/place/' + encodeURIComponent(p.id);
    a.textContent = 'Its history';
    box.append(a);
    return box;
  }

  function draw(pins) {
    layer.clearLayers();
    for (const p of pins || []) {
      if (p.id) {
        const mine = here !== null && building(p.id) === here;
        L.marker([p.lat, p.lon], {
          icon: icon(mine ? 'pin--here' : 'pin--one', mine ? 18 : 12),
          title: p.address,
          zIndexOffset: mine ? 1000 : 0,
        }).bindPopup(popup(p), { closeButton: false }).addTo(layer);
      } else {
        const size = Math.round(14 + 7 * Math.log10(p.n));
        L.marker([p.lat, p.lon], {
          icon: icon('pin--many', size, p.n > 1 ? count(p.n) : ''),
          title: p.n === 1 ? '1 building, zoom in' : p.n + ' buildings, zoom in',
        }).on('click', () => map.setView([p.lat, p.lon], Math.min(map.getZoom() + 2, 19))).addTo(layer);
      }
    }
  }

  // ponytail: one request per settled view, padded so small pans reuse it;
  // tile-snapped requests would let a CDN cache them if traffic ever needs it.
  let loaded = null, loadedZoom = -1, inflight = null;
  function load() {
    const z = map.getZoom();
    if (z === loadedZoom && loaded && loaded.contains(map.getBounds())) return;
    const b = map.getBounds().pad(0.5);
    if (inflight) inflight.abort();
    const mine = inflight = new AbortController();
    const q = new URLSearchParams({
      z, s: b.getSouth().toFixed(5), w: b.getWest().toFixed(5), n: b.getNorth().toFixed(5), e: b.getEast().toFixed(5),
    });
    el.classList.add('is-loading');
    fetch('/map.json?' + q, { signal: mine.signal })
      .then((r) => { if (!r.ok) throw new Error(r.status); return r.json(); })
      .then((pins) => {
        draw(pins);
        loaded = b; loadedZoom = z;
        if (status) status.textContent = '';
      })
      .catch((err) => {
        if (err.name === 'AbortError') return;
        if (status) status.textContent = 'The places did not load. Move the map to try again.';
      })
      .finally(() => { if (inflight === mine) el.classList.remove('is-loading'); });
  }
  map.on('moveend', load);
  load();
})();
