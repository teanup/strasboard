// API base URL, configurable via config.js (window.STRASBOARD_API_URL).
// Empty string means same origin as the page.
const API_BASE = (window.STRASBOARD_API_URL ?? '').replace(/\/$/, '');

function apiURL(path) {
  return API_BASE + path;
}

// Tariff helpers
function toSegments(entry) {
  return Object.entries(entry)
    .filter(([k]) => k !== 'date')
    .map(([tariff, value]) => ({ tariff, value }));
}

function entryTotal(entry) {
  return toSegments(entry).reduce((sum, s) => sum + s.value, 0);
}

// Response parsing
//
// The API returns each resource as { items: [{id, data, updated_at}], updated_at, error }.
// These helpers unwrap that shape into the flat structures the widgets expect.

// resourceItems returns the data payloads of a resource's items.
function resourceItems(resource) {
  return (resource?.items ?? []).map((item) => item.data);
}

// resourceError returns the resource error, or a fallback when it has no items.
function resourceError(resource, hasData) {
  return resource?.error || (!hasData ? 'No data' : null);
}

const parseWeather = (all) => {
  const resource = all.weather;
  const data = resourceItems(resource)[0];
  if (!data) return { data: null, error: resourceError(resource, false) };
  return {
    data: formatWeather(data.current ?? null, data.hourly ?? [], data.daily ?? []),
    error: resourceError(resource, true),
  };
};

const SKY_VARS = Object.fromEntries([
  ...[0, 1].map(c => [c, 'transparent']),
  ...[2, 3].map(c => [c, 'var(--sky-cloudy)']),
  ...[45, 48].map(c => [c, 'var(--sky-fog)']),
  ...[51, 53, 55, 61, 63, 65, 80, 81, 82].map(c => [c, 'var(--sky-rain)']),
  ...[56, 57, 66, 67].map(c => [c, 'var(--sky-freezing)']),
  ...[71, 73, 75, 77, 85, 86].map(c => [c, 'var(--sky-snow)']),
  ...[95, 96, 99].map(c => [c, 'var(--sky-thunder)']),
]);

function formatWeather(current, hourly, daily) {
  let graph = null;
  if (hourly.length > 1) {
    const temps = hourly.map(h => h.temperature);
    const dataMin = Math.min(...temps);
    const dataMax = Math.max(...temps);

    const ceilMax = Math.ceil(dataMax + 0.5);
    const floorMin = Math.floor(dataMin - 0.5);
    const r = ceilMax - floorMin;
    const gap = r <= 6 ? 1 : r <= 14 ? 2 : r <= 24 ? 4 : 5;
    const top = Math.round(ceilMax / gap) * gap;
    const bottom = -Math.round(-floorMin / gap) * gap;
    const graphMax = Math.max(top, dataMax + gap / 2);
    const graphMin = Math.min(bottom, dataMin - gap / 2);
    const graphRange = graphMax - graphMin;

    const yLines = [];
    for (let v = bottom; v <= top; v += gap) {
      yLines.push({ y: (v - graphMin) / graphRange * 100, label: `${v}\u00b0` });
    }

    const n = hourly.length;
    const numSeg = n - 1;
    const toY = (t) => (100 - (t - graphMin) / graphRange * 100).toFixed(2);
    const curve = hourly.map((h, i) =>
      `${(i / numSeg * 100).toFixed(2)}% ${toY(h.temperature)}%`
    ).join(',');
    const fill = `clip-path:polygon(0% 100%,${curve},100% 100%)`;

    // Sky gradient (merge consecutive same-category stops, blur transitions)
    const skyStops = [];
    let prevSky = null;
    for (let i = 0; i < numSeg; i++) {
      const sky = SKY_VARS[hourly[i].code] ?? 'transparent';
      const pct = i / numSeg * 100;
      if (sky !== prevSky) {
        if (prevSky !== null) skyStops.push(`${prevSky} ${Math.max(0, pct - 1).toFixed(2)}%`);
        skyStops.push(`${sky} ${Math.min(100, pct + 1).toFixed(2)}%`);
        prevSky = sky;
      }
    }
    if (prevSky !== null) skyStops.push(`${prevSky} 100%`);
    const skyStyle = `clip-path:polygon(0% 0%,${curve},100% 0%);` +
      `background:linear-gradient(to right,${skyStops.join(',')})`;

    const segments = hourly.slice(0, -1).map((h, i) => {
      const d = new Date(h.time);
      const hour = d.getHours();
      const hh = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
      const seg = {
        wmo: h.code,
        night: !h.is_day,
        label: `${hh} \u00b7 ${Math.round(h.temperature * 10) / 10}\u00b0C`,
      };
      if (hour === 0) {
        seg.tick = d.toLocaleDateString(undefined, { weekday: 'short' });
        seg.main = true;
      } else if (hour === 8 || hour === 14 || hour === 20) {
        seg.tick = `${hour}h`;
      }
      return seg;
    });

    // Current time position as percentage
    const t0 = new Date(hourly[0].time).getTime();
    const tN = new Date(hourly[n - 1].time).getTime();
    const nowMs = Date.now();
    const nowPct = Math.max(0, Math.min(100, (nowMs - t0) / (tN - t0) * 100));

    graph = { fill, sky: skyStyle, segments, yLines, nowPct };
  }

  const forecast = daily.map(d => {
    const date = new Date(d.date + 'T12:00:00');
    return {
      day: date.toLocaleDateString(undefined, { weekday: 'short' }),
      code: d.code,
      min: Math.round(d.temp_min),
      max: Math.round(d.temp_max),
    };
  });

  return { current, graph, forecast };
}

const parseTemperature = (all) => {
  const resource = all.temperature;
  const data = resourceItems(resource)[0];
  if (!data) return { data: null, error: resourceError(resource, false) };
  return {
    data: {
      temperature: data.temperature,
      humidity: data.humidity,
      location: data.location ?? null,
    },
    error: resourceError(resource, true),
  };
};

const parseTransport = (all) => {
  const resource = all.transport;
  const stops = resourceItems(resource);
  if (!stops.length) return { data: null, error: resourceError(resource, false) };
  return {
    data: {
      stops: stops.flatMap((stop) =>
        (stop.destinations?.length ? stop.destinations : [{ name: '', departures: [] }])
          .map((dest) => ({
            id: stop.id,
            stop: stop.name,
            line: stop.line,
            color: stop.color,
            colorText: stop.color_text,
            destination: dest.name,
            departures: dest.departures ?? [],
          }))
      ),
    },
    error: resourceError(resource, true),
  };
};

function parseElectricity(all) {
  const resource = all.electricity;
  const data = resourceItems(resource)[0];
  if (!data) return { data: null, error: resourceError(resource, false) };

  const normalize = (entry) => ({
    date: entry.date,
    hp: entry.HP ?? 0, hc: entry.HC ?? 0,
    rhp: entry.RHP ?? 0, rhc: entry.RHC ?? 0,
    bchp: entry.BCHP ?? 0, bchc: entry.BCHC ?? 0,
    buhp: entry.BUHP ?? 0, buhc: entry.BUHC ?? 0,
  });

  const days = (data.days ?? []).slice(-14).map(normalize);
  const months = (data.months ?? []).slice(-2).map(normalize);
  return {
    data: formatElectricity(months, days),
    error: resourceError(resource, true),
  };
}

function formatElectricity(months, days) {
  const monthBars = months.map((m, i) => {
    const d = new Date(m.date + '-15');
    const title = d.toLocaleDateString(undefined, { month: 'long' });
    const bar = { title, segments: toSegments(m) };

    if (i === months.length - 1 && days.length > 0) {
      const daysInMonth = new Date(d.getFullYear(), d.getMonth() + 1, 0).getDate();
      const lastDay = new Date(days[days.length - 1].date);
      const daysWithData = lastDay.getMonth() === d.getMonth() ? lastDay.getDate() : daysInMonth;
      if (daysWithData > 0 && daysWithData < daysInMonth) {
        bar.ext = Math.round(entryTotal(m) * daysInMonth / daysWithData) - entryTotal(m);
      }
    }
    return bar;
  });

  const monthMax = monthBars.reduce((mx, b) => {
    const total = b.segments.reduce((s, seg) => s + seg.value, 0);
    return Math.max(mx, total + (b.ext ?? 0));
  }, 0);

  const current7 = days.slice(-7);
  const prev7 = days.slice(-14, -7);
  const dayGroups = current7.map((day, i) => {
    const d = new Date(day.date + 'T12:00:00');
    const title = ' ' + d.toLocaleDateString(undefined, { weekday: 'short' }) + ' ' + d.getDate();
    const bars = [];
    if (prev7[i]) bars.push({ segments: toSegments(prev7[i]), dim: true });
    bars.push({ segments: toSegments(day) });
    return { title, bars };
  });

  const dayMax = days.slice(-14).reduce((mx, d) => Math.max(mx, entryTotal(d)), 0);
  const allEntries = [...months, ...days];
  const legend = Object.keys(allEntries[0] ?? {})
    .filter(k => k !== 'date' && allEntries.some(e => e[k] > 0));

  return { monthBars, monthMax, dayGroups, dayMax, legend };
}

const parseTempo = (all) => {
  const resource = all.tempo;
  const days = resourceItems(resource);
  if (!days.length) return { data: null, error: resourceError(resource, false) };

  const today = new Date().toLocaleDateString('en-CA', { timeZone: 'Europe/Paris' });
  const tomorrow = new Date(Date.now() + 86400000).toLocaleDateString('en-CA', { timeZone: 'Europe/Paris' });
  const find = (date) => days.find((t) => t.date === date)?.color ?? null;
  return {
    data: { today: find(today), tomorrow: find(tomorrow) },
    error: resourceError(resource, true),
  };
};


document.addEventListener('alpine:init', () => {
  Alpine.data('dashboard', () => ({
    timestamp: null,
    now: Date.now(),
    weather: { data: null, error: null },
    temperature: { data: null, error: null },
    transport: { data: null, error: null },
    electricity: { data: null, error: null },
    tempo: { data: null, error: null },

    init() {
      document.documentElement.lang = navigator.language || 'en';
      this.refresh();
      setInterval(() => this.refresh(), 60_000);
      setInterval(() => { this.now = Date.now(); }, 1_000);
    },

    async refresh() {
      try {
        const all = await fetch(apiURL('/api/v1/all')).then(r => r.json());
        this.timestamp = all.timestamp;
        this.weather = parseWeather(all);
        this.temperature = parseTemperature(all);
        this.transport = parseTransport(all);
        this.electricity = parseElectricity(all);
        this.tempo = parseTempo(all);
      } catch (e) {
        console.error('Fetch failed:', e);
      }
    },

    async refreshStop(stopId) {
      try {
        const stop = await fetch(apiURL(`/api/v1/transport/live?id=${stopId}`)).then(r => r.json());
        if (stop?.destinations) {
          this.transport.data?.stops?.forEach((row) => {
            if (row.id !== stopId) return;
            const match = stop.destinations.find((d) => d.name === row.destination);
            if (match) row.departures = match.departures ?? [];
          });
        }
      } catch (e) {
        console.error('Live transport fetch failed:', e);
      }

      document.querySelectorAll(`.transport-row[data-stop-id="${stopId}"]`).forEach((el) => {
        el.classList.remove('refreshed');
        void el.offsetWidth;
        el.classList.add('refreshed');
      });
    },

    getActiveDepartures(departures, _now) {
      return departures
        .filter((dep) => new Date(dep.time).getTime() - _now > -20_000)
        .slice(0, 3);
    },

    formatDeparture(isoTime, _now) {
      const secs = Math.round((new Date(isoTime).getTime() - _now) / 1000);
      if (secs <= 20) return '';
      if (secs < 60) return `${secs} s`;
      const mins = Math.floor(secs / 60);
      if (mins < 60) return `${mins} min`;
      const hours = Math.floor(mins / 60);
      return `${hours}h${String(mins % 60).padStart(2, '0')}`;
    },

    barSegments(segments) {
      return segments.filter(s => s.value > 0);
    },

    segStyle(seg, max) {
      return max <= 0 ? 'display:none' : `flex-basis:${seg.value / max * 100}%`;
    },

    extStyle(bar, max) {
      return !bar.ext || max <= 0 ? 'display:none' : `flex-basis:${bar.ext / max * 100}%`;
    },

    barTotal(bar) {
      return bar.segments.reduce((sum, s) => sum + s.value, 0);
    },

    showExtLabel(bar, max) {
      return bar.ext > 0 && bar.ext / max > 0.05;
    },
  }));
});
