// Draws one recorded trip: the path it took and the waypoints it was measured
// against. Display only — nothing here feeds back into the measurements.
(function () {
  const el = document.getElementById('map');
  if (!el || typeof L === 'undefined') return;

  const map = L.map(el);
  L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', {
    maxZoom: 19,
    attribution: '&copy; OpenStreetMap contributors',
  }).addTo(map);

  const waypoints = JSON.parse(el.dataset.waypoints || '[]') || [];
  const bounds = [];

  waypoints.forEach(function (w, i) {
    // The circle is drawn at the waypoint's true tolerance, so it is obvious
    // when a radius is too small for the road to pass through it.
    L.circle([w.Lat, w.Lon], {
      radius: w.RadiusM,
      color: '#2f6feb',
      weight: 1,
      fillOpacity: 0.15,
    }).addTo(map).bindTooltip((i + 1) + '. ' + w.Label);
    bounds.push([w.Lat, w.Lon]);
  });

  fetch(el.dataset.track)
    .then(function (r) { return r.ok ? r.json() : []; })
    .then(function (points) {
      const line = points.map(function (p) { return [p[0], p[1]]; });
      if (line.length) {
        L.polyline(line, { color: '#c0392b', weight: 4, opacity: 0.85 }).addTo(map);
        line.forEach(function (p) { bounds.push(p); });
      }
      if (bounds.length) map.fitBounds(bounds, { padding: [30, 30] });
      else map.setView([0, 0], 2);
    })
    .catch(function () {
      if (bounds.length) map.fitBounds(bounds, { padding: [30, 30] });
    });
})();
