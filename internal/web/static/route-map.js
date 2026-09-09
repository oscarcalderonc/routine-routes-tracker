// The waypoint editor. Clicking the map fills in the coordinates for a new
// waypoint, and a past trip can be drawn underneath so that waypoints land on
// a road that was actually driven rather than one guessed from the tiles.
(function () {
  const el = document.getElementById('route-map');
  if (!el || typeof L === 'undefined') return;

  const map = L.map(el);
  L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', {
    maxZoom: 19,
    attribution: '&copy; OpenStreetMap contributors',
  }).addTo(map);

  const waypoints = JSON.parse(el.dataset.waypoints || '[]') || [];
  const bounds = [];

  waypoints.forEach(function (w, i) {
    L.circle([w.Lat, w.Lon], {
      radius: w.RadiusM,
      color: '#2f6feb',
      weight: 1,
      fillOpacity: 0.15,
    }).addTo(map).bindTooltip((i + 1) + '. ' + w.Label, { permanent: false });
    bounds.push([w.Lat, w.Lon]);
  });

  if (bounds.length) map.fitBounds(bounds, { padding: [40, 40] });
  else map.setView([52.2297, 21.0122], 13);

  // A provisional marker showing where the next waypoint would go.
  let pending = null;
  map.on('click', function (e) {
    const lat = e.latlng.lat.toFixed(6);
    const lon = e.latlng.lng.toFixed(6);
    const latInput = document.getElementById('new-lat');
    const lonInput = document.getElementById('new-lon');
    if (latInput) latInput.value = lat;
    if (lonInput) lonInput.value = lon;

    if (pending) map.removeLayer(pending);
    pending = L.circle(e.latlng, {
      radius: 25, color: '#1f8a4c', weight: 2, dashArray: '4 3', fillOpacity: 0.1,
    }).addTo(map);
  });

  let backdrop = null;
  const picker = document.getElementById('backdrop');
  if (picker) {
    picker.addEventListener('change', function () {
      if (backdrop) { map.removeLayer(backdrop); backdrop = null; }
      if (!picker.value) return;

      fetch('/api/trips/' + picker.value + '/track.json')
        .then(function (r) { return r.ok ? r.json() : []; })
        .then(function (points) {
          const line = points.map(function (p) { return [p[0], p[1]]; });
          if (!line.length) return;
          backdrop = L.polyline(line, { color: '#c0392b', weight: 3, opacity: 0.6 }).addTo(map);
          map.fitBounds(backdrop.getBounds(), { padding: [30, 30] });
        });
    });
  }
})();
