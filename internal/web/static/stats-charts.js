// Charts for the statistics page. The report is embedded in the page rather
// than fetched, so the charts and the tables above them can never disagree.
(function () {
  const holder = document.getElementById('report-data');
  if (!holder || typeof Chart === 'undefined') return;

  let report;
  try {
    report = JSON.parse(holder.textContent);
  } catch (e) {
    return;
  }
  if (!report || !report.segments) return;

  // A qualitative palette that stays distinguishable in both themes and for
  // the common forms of colour blindness.
  const palette = ['#2f6feb', '#1f8a4c', '#b7791f', '#8e44ad', '#c0392b', '#0e7c86'];
  const dark = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
  const grid = dark ? 'rgba(255,255,255,.10)' : 'rgba(0,0,0,.08)';
  const tick = dark ? '#98a3b0' : '#616b78';

  Chart.defaults.color = tick;
  Chart.defaults.font.family =
    '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif';

  const labelFor = {};
  report.segments.forEach(function (s) { labelFor[s.seq] = s.label; });

  function minutes(seconds) { return Math.round((seconds / 60) * 100) / 100; }

  function durationAxis(title) {
    return {
      title: { display: true, text: title },
      ticks: {
        callback: function (v) {
          const m = Math.floor(v);
          const s = Math.round((v - m) * 60);
          return m + 'm' + (s ? ' ' + s + 's' : '');
        },
      },
      grid: { color: grid },
    };
  }

  const weeksCanvas = document.getElementById('weeks-chart');
  if (weeksCanvas && report.weeks && report.weeks.length) {
    const weeks = [];
    report.weeks.forEach(function (w) {
      if (weeks.indexOf(w.week) === -1) weeks.push(w.week);
    });
    weeks.sort();

    const datasets = report.segments.map(function (seg, i) {
      const bySeq = {};
      report.weeks.forEach(function (w) {
        if (w.seq === seg.seq) bySeq[w.week] = minutes(w.median_s);
      });
      return {
        label: seg.label,
        data: weeks.map(function (w) { return bySeq[w] === undefined ? null : bySeq[w]; }),
        borderColor: palette[i % palette.length],
        backgroundColor: palette[i % palette.length],
        tension: 0.25,
        spanGaps: true,
        pointRadius: 3,
      };
    });

    new Chart(weeksCanvas, {
      type: 'line',
      data: { labels: weeks, datasets: datasets },
      options: {
        responsive: true,
        interaction: { mode: 'index', intersect: false },
        scales: {
          y: durationAxis('Median time'),
          x: { grid: { color: grid } },
        },
        plugins: { legend: { position: 'bottom' } },
      },
    });
  }

  const tripsCanvas = document.getElementById('trips-chart');
  if (tripsCanvas && report.trips && report.trips.length) {
    const datasets = report.segments.map(function (seg, i) {
      const colour = palette[i % palette.length];
      return {
        label: seg.label,
        data: report.trips
          .filter(function (t) { return t.seq === seg.seq; })
          .map(function (t) {
            return { x: t.date, y: minutes(t.duration_s), outlier: t.outlier, dir: t.direction };
          }),
        backgroundColor: colour,
        borderColor: colour,
        // Unusual journeys are drawn larger and hollow rather than dropped.
        pointRadius: function (ctx) { return ctx.raw && ctx.raw.outlier ? 7 : 3.5; },
        pointStyle: function (ctx) { return ctx.raw && ctx.raw.outlier ? 'triangle' : 'circle'; },
      };
    });

    new Chart(tripsCanvas, {
      type: 'scatter',
      data: { datasets: datasets },
      options: {
        responsive: true,
        scales: {
          y: durationAxis('Time taken'),
          x: { type: 'category', grid: { color: grid } },
        },
        plugins: {
          legend: { position: 'bottom' },
          tooltip: {
            callbacks: {
              label: function (ctx) {
                const p = ctx.raw;
                const mins = Math.floor(p.y);
                const secs = Math.round((p.y - mins) * 60);
                return ctx.dataset.label + ': ' + mins + 'm ' + secs + 's' +
                  ' (' + p.dir + (p.outlier ? ', unusual' : '') + ')';
              },
            },
          },
        },
      },
    });
  }
})();
