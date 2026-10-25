// Demo data for StrasBoard.
//
// Enabled with ?demo in the URL. Generates plausible data for every resource,
// with all dates computed from the current time so the dashboard always looks
// fresh. No personal data is involved.

// item wraps a payload in the API's item shape.
function demoItem(id, data) {
  return { id, data, updated_at: new Date().toISOString() };
}

// demoResource returns a resource payload matching the API's list response.
function demoResource(name) {
  const now = new Date().toISOString();
  const items = (DEMO_BUILDERS[name] ?? (() => []))();
  return { items, updated_at: now, error: '' };
}

// dateOnly formats a Date as YYYY-MM-DD in local time.
function dateOnly(d) {
  return d.toLocaleDateString('en-CA');
}

// parisDate formats a Date as YYYY-MM-DD in Europe/Paris, matching how the
// tempo parser looks up today and tomorrow.
function parisDate(d) {
  return d.toLocaleDateString('en-CA', { timeZone: 'Europe/Paris' });
}

// hourStamp formats a Date as YYYY-MM-DDTHH:00 (local), matching Open-Meteo.
function hourStamp(d) {
  const pad = (n) => String(n).padStart(2, '0');
  return `${dateOnly(d)}T${pad(d.getHours())}:00`;
}

// pseudoRandom returns a stable pseudo-random number in [0, 1) for a seed.
function pseudoRandom(seed) {
  const x = Math.sin(seed * 12.9898) * 43758.5453;
  return x - Math.floor(x);
}

const DEMO_BUILDERS = {
  weather() {
    const now = new Date();
    const hour = now.getHours();

    // A smooth daily temperature curve peaking mid-afternoon.
    const tempAt = (h) => 12 + 6 * Math.sin(((h - 9) / 24) * 2 * Math.PI);

    const current = {
      time: hourStamp(now),
      temperature: Math.round(tempAt(hour) * 10) / 10,
      feels_like: Math.round((tempAt(hour) - 1.5) * 10) / 10,
      is_day: hour >= 7 && hour < 20,
      code: 2,
    };

    const hourly = [];
    for (let i = -4; i <= 96; i++) {
      const t = new Date(now.getTime() + i * 3600_000);
      const h = t.getHours();
      hourly.push({
        time: hourStamp(t),
        temperature: Math.round(tempAt(h) * 10) / 10,
        feels_like: Math.round((tempAt(h) - 1.5) * 10) / 10,
        is_day: h >= 7 && h < 20,
        code: [0, 1, 2, 3, 61][Math.floor(pseudoRandom(i + 100) * 5)],
      });
    }

    const daily = [];
    for (let i = 4; i <= 7; i++) {
      const t = new Date(now.getTime() + i * 86400_000);
      const base = 12 + 4 * Math.sin(i);
      daily.push({
        date: dateOnly(t),
        temp_max: Math.round((base + 5) * 10) / 10,
        temp_min: Math.round((base - 4) * 10) / 10,
        code: [0, 2, 3, 61][Math.floor(pseudoRandom(i + 200) * 4)],
      });
    }

    return [
      demoItem('current', current),
      demoItem('hourly', hourly),
      demoItem('daily', daily),
    ];
  },

  temperature() {
    const now = Date.now();
    const items = [];
    for (let i = 4; i >= 0; i--) {
      const t = new Date(now - i * 60_000);
      items.push(demoItem(String(t.getTime()), {
        time: t.toISOString(),
        temperature: Math.round((21 + pseudoRandom(i + 1) * 1.5) * 10) / 10,
        humidity: Math.round(44 + pseudoRandom(i + 50) * 6),
        location: 'Living Room',
      }));
    }
    return items;
  },

  transport() {
    const now = Date.now();
    const stops = [
      { id: 0, name: 'Gare Centrale', line: 'C', color: '#F29400', color_text: '#FFFFFF' },
      { id: 1, name: 'Observatoire', line: 'E', color: '#009EE0', color_text: '#FFFFFF' },
    ];
    const destinations = ['Neuhof Rodolphe Reuss', 'Baggersee', 'Wacken', "Robertsau L'Escale"];

    return stops.map((stop, si) => {
      const dests = destinations.slice(si * 2, si * 2 + 2).map((name, di) => {
        const departures = [];
        for (let i = 0; i < 4; i++) {
          const mins = 2 + i * 7 + Math.floor(pseudoRandom(si * 10 + di * 5 + i) * 4);
          departures.push({
            time: new Date(now + mins * 60_000).toISOString(),
            realtime: i % 2 === 0,
          });
        }
        return { name, departures };
      });
      return demoItem(String(stop.id), { ...stop, destinations: dests });
    });
  },

  electricity() {
    const now = new Date();
    const tariffs = ['BCHC', 'BCHP', 'BUHC', 'BUHP', 'RHC', 'RHP'];

    const days = [];
    for (let i = 13; i >= 0; i--) {
      const t = new Date(now.getTime() - i * 86400_000);
      const entry = { date: dateOnly(t) };
      tariffs.forEach((k, ki) => {
        entry[k] = Math.round(3 + pseudoRandom(i * 10 + ki) * 12);
      });
      days.push(entry);
    }

    const months = [];
    for (let i = 1; i >= 0; i--) {
      const t = new Date(now.getFullYear(), now.getMonth() - i, 1);
      const entry = { date: `${t.getFullYear()}-${String(t.getMonth() + 1).padStart(2, '0')}` };
      ['HC', 'HP'].forEach((k, ki) => {
        entry[k] = Math.round(120 + pseudoRandom(i * 10 + ki + 300) * 80);
      });
      months.push(entry);
    }

    return [demoItem('days', days), demoItem('months', months)];
  },

  tempo() {
    const now = new Date();
    const colors = ['blue', 'white', 'red'];
    const items = [];
    for (let i = 0; i <= 1; i++) {
      const t = new Date(now.getTime() + i * 86400_000);
      const date = parisDate(t);
      items.push(demoItem(date, {
        date,
        color: colors[Math.floor(pseudoRandom(i + 400) * 3)],
      }));
    }
    return items;
  },
};
