// The helix is the link between the recipient and the owner's machine, drawn as a
// double strand of particles on the page's #helix canvas. Elements marked
// data-helix lay out its path in DOM order: a link, the core (the owner's machine),
// or a gap (a control the strands plug into). The view they sit in sets what it
// shows, through #gate's data-state and data-reason or the task bench's data-stage,
// data-fault, data-beat, and --progress: strands pair as the link connects, carry
// input into the machine, zip toward the result as a run progresses, and break
// where a run fails. It reads no field values. The canvas blends by difference, so
// the white it draws reads black on the page and white inside the machine.
(() => {
  // Without a 2D context there is no helix; setup never throws into the loader.
  const canvas = document.getElementById('helix');
  const ctx = canvas?.getContext('2d');
  if (!ctx) return;
  const calm = matchMedia('(prefers-reduced-motion: reduce)');
  const TAU = 2 * Math.PI;
  const clamp = (v, lo = 0, hi = 1) => Math.min(hi, Math.max(lo, v));
  const ramp = (a, b, v) => { const t = clamp((v - a) / (b - a)); return t * t * (3 - 2 * t); };
  const rand = (a, b) => a + Math.random() * (b - a);
  const flip = () => Math.random() < 0.5 ? 0 : 1;

  function sprite(size, stops) {
    const image = document.createElement('canvas');
    image.width = image.height = size;
    const draw = image.getContext('2d');
    if (!draw) return null;
    const fill = draw.createRadialGradient(size / 2, size / 2, 0, size / 2, size / 2, size / 2);
    for (const [at, alpha] of stops) fill.addColorStop(at, `rgba(255,255,255,${alpha})`);
    draw.fillStyle = fill;
    draw.fillRect(0, 0, size, size);
    return image;
  }
  const DOT = sprite(24, [[0, 1], [0.72, 1], [1, 0]]);
  const GLOW = sprite(64, [[0, 0.7], [0.22, 0.28], [1, 0]]);
  if (!DOT || !GLOW) return;

  // Rotation speed in radians per second for each gate state and run stage.
  const SPIN = { opening: 0.8, secret: 0.45, connecting: 2.6, unavailable: 0.25,
    ready: 0.7, sending: 1.8, accepted: 1.2, running: 3.4, done: 0.9, failed: 0.3 };
  // The most backing pixels the canvas may hold; a larger area draws at a lower resolution.
  const MAX_PIXELS = 8e6;
  // How fast the dust drifts along the links, relative to its own pace.
  const DRIFT = { opening: 2, connecting: 7, unavailable: 0.3, sending: 4, running: 5, failed: 0.3 };
  // Reasons the gate shows when the two sides never paired, rather than a far end that failed.
  const UNPAIRED = ['rejected', 'incomplete', 'nolink'];

  // Per rung: whether the strands coil together or drift apart (zip), whether its
  // base pair holds (bond), brightness (lit), visibility (vis), and the core's share
  // (core). Per strand point, two per rung: its offset from the helix and that
  // offset's velocity, the spring that carries assembly, layout changes, and the
  // pointer's push, then the drawn position and depth.
  let n = 0, zip, bond, lit, vis, boost, rise, core, face, ox, oy, vx, vy, px, py, pz;
  let geo = null, key = '', since = 0, born = 0, beat = '', phase = 0, speed = 0.7, then = 0;
  let filled = 0, broken = 0, area = '', drawn = null, painted = '';
  let riders = [], flyers = [], bits = [], rings = [], pulses = [], motes = [], dust = [], pointer = null;
  let qx = 0, qy = 0, qz = 0, qv = 0, tz = 1, tb = 1, tl = 0.45, tv = 1;

  // The path runs along one axis through every marked element, from the first one's
  // leading edge to the last one's trailing edge, in page coordinates.
  function measure() {
    const marks = [...document.querySelectorAll('[data-helix]')].filter(el => el.getClientRects().length);
    if (!marks.length) return null;
    const box = el => {
      const r = el.getBoundingClientRect();
      return { l: r.left + scrollX, t: r.top + scrollY, r: r.right + scrollX, b: r.bottom + scrollY };
    };
    const boxes = marks.map(box);
    const first = boxes[0], end = boxes.at(-1);
    const across = end.r - first.l >= end.b - first.t;
    const lead = b => across ? b.l : b.t, trail = b => across ? b.r : b.b;
    const from = lead(first), L = trail(end) - from;
    if (L < 60) return null;
    const host = marks[0].closest('[data-stage], [data-state]');
    const data = host?.dataset ?? {};
    const task = 'stage' in data;
    const fraction = parseFloat(host?.style.getPropertyValue('--progress'));
    const g = {
      across, from, L, cross: across ? (first.t + first.b) / 2 : (first.l + first.r) / 2, host, task,
      mode: data.stage ?? data.state ?? '', why: data.fault ?? data.reason ?? '', beat: data.beat ?? '',
      progress: data.progress !== 'unknown' && Number.isFinite(fraction) ? clamp(fraction) : null,
      core: null, gap: null, room: null,
    };
    const at = v => (v - from) / L;
    marks.forEach((el, i) => {
      if (el.dataset.helix === 'core') {
        g.core = [at(lead(boxes[i])), at(trail(boxes[i]))];
        g.room = boxes[i];
      } else if (el.dataset.helix === 'gap') {
        const shown = [...el.children].filter(child => child.getClientRects().length).map(box);
        const on = across ? boxes[i].t <= g.cross && boxes[i].b >= g.cross : boxes[i].l <= g.cross && boxes[i].r >= g.cross;
        if (shown.length && on) g.gap = [at(Math.min(...shown.map(lead)) - 16), at(Math.max(...shown.map(trail)) + 16)];
      }
    });
    g.mid = g.core ?? g.gap ?? [0.5, 0.5];
    // The gate's helix is the hero; a task's runs slimmer, and slimmer still down a narrow rail.
    g.scale = (task ? 1 : 1.2) * (across ? 1 : 0.85);
    g.amp = (task ? 15 : 22) * (across ? 1 : 0.68);
    g.coreAmp = g.room ? Math.min(across ? 30 : 14, (across ? g.room.b - g.room.t : g.room.r - g.room.l) * 0.28) : g.amp;
    g.pitch = across ? (task ? 150 : 180) : 120;
    g.cut = cutAt(g);
    return g;
  }

  function cutAt(g) {
    const [m0, m1] = g.mid;
    if (g.mode === 'unavailable') return UNPAIRED.includes(g.why) ? null : m1 + 0.4 * (1 - m1);
    if (g.mode !== 'failed' || g.why === 'invalid') return null;
    return g.why === 'disconnected' ? m0 * 0.55 : m1 + 0.05 * (1 - m1);
  }

  // What a rung at s aims for, t seconds into the current state: zip, bond, lit, and
  // vis. A bond follows zip unless the state sets it: the gate's strands coil before
  // they pair.
  function aim(g, s, t) {
    const [m0, m1] = g.mid;
    const before = s < m0, after = s > m1, inside = !before && !after;
    const out = after ? (s - m1) / (1 - m1) : 0;
    tz = 1;
    tb = -1;
    tl = inside && g.core ? 0.75 : 0.45;
    tv = g.gap && inside ? 0 : 1;
    switch (g.mode) {
      case 'secret': tb = 0; tl = 0.4; break;
      case 'connecting': tb = clamp((t / 1.4 - s) / 0.06); tl = 0.42 + 0.5 * tb; break;
      case 'unavailable': if (g.cut == null) { tb = 0; tl = 0.3; } else tl = 0.4; break;
      case 'sending': tl = before ? 1 : inside ? 0.7 : 0.3; tz = after ? 0 : 1; break;
      case 'accepted': tl = before ? 0.55 : inside ? 0.85 : 0.3; tz = after ? 0 : 1; break;
      case 'running': {
        if (!after) { tl = inside ? 1 : 0.5; break; }
        // Known progress zips the strand up to it; unknown progress sends a zipped band along.
        tz = g.progress == null ? 1 - clamp(Math.abs(out - (t * 0.5 % 1.4 - 0.15)) / 0.14) : out < g.progress ? 1 : 0;
        tl = 0.3 + 0.65 * tz;
        break;
      }
      case 'done': tl = inside ? 0.85 : 0.62; break;
      case 'failed':
        if (g.why !== 'invalid') tl = inside ? 0.55 : 0.42;
        else if (after) { tz = 0; tl = 0.28; tv = 0.7; } else tl = 0.45;
        break;
    }
    if (g.cut != null && s > g.cut) { tz = 0; tb = 0; tl = 0.32; tv = Math.min(tv, 0.32); }
    if (tb < 0) tb = tz;
  }

  // A rider travels along strand k from s to end at px per second.
  function ride(g, k, s, end, pace, size = 1) {
    if (riders.length < 400) riders.push({ k, s, end, v: Math.sign(end - s) * pace / g.L, size });
  }

  // A flyer leaves a point on the page for strand k at s, then rides on to end. It
  // curves in along the path's axis, so a stream of flyers reads as one current.
  function fly(g, x, y, k, s, end, delay) {
    const [tx, ty] = point(g, s * g.L);
    flyers.push({ x, y, cx: g.across ? tx + rand(-30, 10) : x + rand(-12, 12), cy: g.across ? y + rand(-12, 12) : ty + rand(-30, 10),
      k, s, end, t: -delay, dur: rand(0.6, 0.95), trail: [] });
  }

  // Locate strand k at s from the drawn rungs, into qx, qy, qz (depth), and qv (visibility).
  function spot(s, k) {
    const f = clamp(s * n - 0.5, 0, n - 1);
    const i = Math.floor(f), w = f - i, j = Math.min(i + 1, n - 1);
    const a = 2 * i + k, b = 2 * j + k;
    qx = px[a] + (px[b] - px[a]) * w;
    qy = py[a] + (py[b] - py[a]) * w;
    qz = pz[a] + (pz[b] - pz[a]) * w;
    qv = vis[i] + (vis[j] - vis[i]) * w;
  }

  function point(g, d) {
    return g.across ? [g.from + d, g.cross] : [g.cross, g.from + d];
  }

  function resize(count, fresh) {
    const keep = (old, stride) => {
      const out = new Float32Array(count * stride);
      if (!old) return out;
      for (let i = 0; i < count; i++) {
        const f = clamp((i + 0.5) / count * n - 0.5, 0, n - 1);
        const a = Math.floor(f), w = f - a, b = Math.min(a + 1, n - 1);
        for (let k = 0; k < stride; k++) out[i * stride + k] = old[a * stride + k] * (1 - w) + old[b * stride + k] * w;
      }
      return out;
    };
    [zip, bond, lit, vis, core, face] = [zip, bond, lit, vis, core, face].map(old => keep(fresh ? null : old, 1));
    boost = new Float32Array(count);
    rise = new Float32Array(count);
    [ox, oy, vx, vy, px, py, pz] = [ox, oy, vx, vy, px, py, pz].map(old => keep(fresh ? null : old, 2));
    n = count;
  }

  // One-off effects as a state begins.
  function enter(g) {
    const [m0, m1] = g.mid;
    if (g.mode === 'sending') {
      const slots = [...g.host.querySelectorAll('.slot')].filter(el => el.getClientRects().length);
      const sources = slots.length ? slots : [g.host.querySelector('form')].filter(Boolean);
      sources.forEach((el, i) => {
        const r = el.getBoundingClientRect();
        for (let j = 0; j < 7; j++) {
          fly(g, r.right + scrollX - rand(10, Math.min(120, r.width * 0.5)), r.top + scrollY + rand(0.25, 0.75) * r.height, flip(), 0,
            m0 + rand(0.2, 0.9) * (m1 - m0), i * 0.09 + j * 0.035);
        }
      });
    }
    if (g.mode === 'done') {
      const [x, y] = point(g, g.L);
      burst(x, y, 44, 220);
      rings.push({ x, y, t: 0 });
      for (let i = 0; i < 34; i++) ride(g, flip(), m1 - rand(0, 0.05), 1, rand(700, 1200), rand(0.8, 1.3));
      pulses.push({ u: m0, v: 1100 / g.L });
      speed = 7;
    }
    if (g.mode === 'failed' && g.why === 'invalid') {
      for (let i = 0; i < 26; i++) ride(g, flip(), m0 + rand(0, 0.6) * (m1 - m0), 0, rand(300, 520));
      shake(160);
    }
    if (g.cut != null) {
      for (let i = 0; i < n; i++) {
        if ((i + 0.5) / n <= g.cut || vis[i] < 0.3) continue;
        for (let k = 0; k < 2; k++) {
          if (Math.random() < 0.55) bits.push({ x: px[2 * i + k], y: py[2 * i + k], vx: rand(-40, 40), vy: rand(-50, 30),
            life: rand(1, 2.2), max: 2.2, r: rand(0.8, 1.9) * g.scale, fall: 80 });
        }
      }
    }
    if (g.mode === 'unavailable' && g.cut == null) shake(240);
  }

  function burst(x, y, count, pace) {
    for (let i = 0; i < count; i++) {
      const angle = rand(0, TAU), v = rand(0.25, 1) * pace;
      bits.push({ x, y, vx: Math.cos(angle) * v, vy: Math.sin(angle) * v, life: rand(0.7, 1.4), max: 1.4, r: rand(0.7, 1.8), fall: 0 });
    }
  }

  function shake(force) {
    for (let j = 0; j < 2 * n; j++) { vx[j] += rand(-force, force) * 0.3; vy[j] += rand(-force, force); }
  }

  // Continuous traffic: what moves along the strands while a state lasts.
  function flow(g, dt, t) {
    const [m0, m1] = g.mid;
    const every = rate => Math.random() < rate * dt;
    switch (g.mode) {
      case 'opening': if (every(2)) ride(g, flip(), 0, 1, 300); break;
      case 'secret':
        if (every(1.2)) {
          if (Math.random() < 0.5) ride(g, flip(), 0, m0, rand(90, 150), 0.8);
          else ride(g, flip(), 1, m1, rand(90, 150), 0.8);
        }
        break;
      case 'connecting': if (every(14)) ride(g, flip(), 0, 1, rand(480, 640)); break;
      case 'ready':
      case 'done':
        if (every(1.4)) {
          if (Math.random() < 0.5) ride(g, flip(), 0, m0, 200, 0.9);
          else ride(g, flip(), m1, 1, 200, 0.9);
        }
        if (g.core && every(2.5)) ride(g, flip(), m0, m1, 240, 0.9);
        break;
      case 'sending': if (every(24)) ride(g, flip(), 0, m0 + rand(0.2, 0.9) * (m1 - m0), rand(440, 600)); break;
      case 'accepted': if (every(6)) ride(g, flip(), m0, m1, 260); break;
      case 'running': {
        if (every(18)) ride(g, flip(), m0, m1, rand(480, 720));
        const front = g.progress ?? clamp(t * 0.5 % 1.4 - 0.15);
        if (every(8) && front > 0.02) ride(g, flip(), m1, m1 + front * (1 - m1), 380);
        break;
      }
    }
  }

  addEventListener('pointermove', event => {
    pointer = event.pointerType === 'mouse' ? { x: event.pageX, y: event.pageY } : null;
  }, { passive: true });
  document.documentElement.addEventListener('pointerleave', () => { pointer = null; });
  // A tap or click near the strands sets them rippling.
  addEventListener('pointerdown', event => {
    if (!geo || calm.matches) return;
    const x = event.pageX, y = event.pageY;
    let near = false;
    for (let j = 0; j < 2 * n; j++) {
      const dx = px[j] - x, dy = py[j] - y, d = Math.hypot(dx, dy);
      if (d > 150 || d < 0.01) continue;
      near ||= d < 60;
      vx[j] += dx / d * (1 - d / 150) * 900;
      vy[j] += dy / d * (1 - d / 150) * 900;
    }
    if (near) rings.push({ x, y, t: 0 });
  }, { passive: true });
  // Each keystroke sends a spark from the field into the strands; a paste sends a handful.
  document.addEventListener('input', event => {
    const g = geo, target = event.target;
    if (!g || calm.matches || !(target instanceof Element) || target.closest('[data-stage], [data-state]') !== g.host) return;
    const r = target.getBoundingClientRect();
    if (!r.width) return;
    const [m0, m1] = g.mid;
    const count = event.inputType === 'insertFromPaste' ? 14 : event.inputType?.startsWith('delete') ? 0 : 1;
    for (let i = 0; i < count; i++) {
      const x = r.left + scrollX + rand(r.width * 0.3, r.width - 10), y = r.top + scrollY + rand(6, r.height - 6);
      if (g.task) fly(g, x, y, flip(), 0, m0 * 0.85, i * 0.02);
      else if (Math.random() < 0.5) fly(g, x, y, flip(), m0, 0, i * 0.02);
      else fly(g, x, y, flip(), m1, 1, i * 0.02);
    }
  }, true);

  function frame(now) {
    requestAnimationFrame(frame);
    const dt = Math.min(0.05, Math.max(0, (now - then) / 1000));
    then = now;
    const g = measure();
    if (!g) {
      if (!canvas.hidden) canvas.hidden = true;
      geo = null;
      n = 0;
      return;
    }
    const still = calm.matches;
    // Reduced motion keeps nothing in flight.
    if (still) for (const list of [riders, flyers, bits, rings, pulses]) list.length = 0;
    const state = `${g.task}:${g.mode}:${g.why}`;
    const entered = state !== key || g.host !== geo?.host;
    if (entered) { key = state; since = now; }
    const t = still ? 1e3 : (now - since) / 1000;
    speed += ((SPIN[g.mode] ?? 0.7) - speed) * (1 - Math.exp(-2.5 * dt));
    phase = still ? 0.9 : phase + speed * dt;

    const fresh = n === 0;
    const count = Math.round(clamp(g.L / 9, 24, 260));
    if (fresh || count !== n) resize(count, fresh);
    if (fresh) born = now;
    // A new helix unrolls from its start, led by a bright front.
    const front = still ? 2 : (now - born) / 1100;
    const moved = !fresh && (geo.across !== g.across || Math.abs(geo.from - g.from) > 0.5 ||
      Math.abs(geo.L - g.L) > 0.5 || Math.abs(geo.cross - g.cross) > 0.5);
    const nx = g.across ? 0 : 1, ny = g.across ? 1 : 0;
    const [c0, c1] = g.core ? [g.core[0] * g.L, g.core[1] * g.L] : [0, 0];
    const [g0, g1] = g.gap ? [g.gap[0] * g.L, g.gap[1] * g.L] : [0, 0];
    const ease = rate => still || fresh ? 1 : 1 - Math.exp(-rate * dt);
    const ez = ease(5), el = ease(4), ev = ease(3);
    boost.fill(0);
    for (const pulse of pulses) pulse.u += pulse.v * dt;
    pulses = pulses.filter(pulse => pulse.u < 1.05);
    let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;

    for (let i = 0; i < n; i++) {
      const s = (i + 0.5) / n, d = s * g.L;
      aim(g, s, t);
      zip[i] += (tz - zip[i]) * ez;
      bond[i] += (tb - bond[i]) * ez;
      lit[i] += (tl - lit[i]) * el;
      vis[i] += (tv - vis[i]) * ev;
      core[i] = g.core ? ramp(c0 - 30, c0 + 30, d) * (1 - ramp(c1 - 30, c1 + 30, d)) : 0;
      rise[i] = clamp((front - s) / 0.12);
      if (rise[i] > 0 && rise[i] < 1) boost[i] += 0.8 * (1 - rise[i]);
      for (const pulse of pulses) boost[i] += 0.9 * Math.max(0, 1 - Math.abs(s - pulse.u) / 0.035);
      let amp = (g.amp + (g.coreAmp - g.amp) * core[i]) * ramp(0, 40, d) * ramp(0, 40, g.L - d) * (0.1 + 0.9 * rise[i]);
      if (g.gap) amp *= ramp(0, 36, Math.max(g0 - d, d - g1));
      const th = d / g.pitch * TAU - phase;
      const sn = Math.sin(th), cs = Math.cos(th), open = 1 - zip[i];
      face[i] = Math.abs(sn);
      // Paired strands coil around each other; open ones drift apart, each still coiling.
      const sway = still ? 0 : open * amp * 0.3 * Math.sin(now / 850 + i * 0.37);
      const offA = amp * (zip[i] * sn + open * (1.3 + 0.3 * sn)) + sway, offB = -amp * (zip[i] * sn + open * (1.3 - 0.3 * sn)) + sway;
      const [ax, ay] = point(g, d);
      for (let k = 0; k < 2; k++) {
        const j = 2 * i + k, off = k ? offB : offA, tx = ax + nx * off, ty = ay + ny * off;
        if (moved) {
          // A new layout leaves each point where it was drawn; the spring carries it over.
          ox[j] = px[j] - tx;
          oy[j] = py[j] - ty;
        }
        if (still) {
          ox[j] = oy[j] = vx[j] = vy[j] = 0;
        } else {
          if (pointer) {
            const dx = tx + ox[j] - pointer.x, dy = ty + oy[j] - pointer.y, dist = Math.hypot(dx, dy);
            if (dist < 90 && dist > 0.01) {
              const push = 1 - dist / 90;
              vx[j] += dx / dist * push * 1500 * dt;
              vy[j] += dy / dist * push * 1500 * dt;
              boost[i] = Math.max(boost[i], 0.45 * push * push);
            }
          }
          vx[j] += (-70 * ox[j] - 13 * vx[j]) * dt;
          vy[j] += (-70 * oy[j] - 13 * vy[j]) * dt;
          ox[j] += vx[j] * dt;
          oy[j] += vy[j] * dt;
        }
        px[j] = tx + ox[j];
        py[j] = ty + oy[j];
        pz[j] = k ? -cs : cs;
        if (px[j] < minX) minX = px[j];
        if (px[j] > maxX) maxX = px[j];
        if (py[j] < minY) minY = py[j];
        if (py[j] > maxY) maxY = py[j];
      }
    }
    if (entered && !fresh && !still) enter(g);
    if (!still) {
      if (g.beat && g.beat !== beat && g.mode === 'running') {
        pulses.push({ u: (g.mid[0] + g.mid[1]) / 2, v: 900 / g.L });
        for (let i = 0; i < 6; i++) ride(g, flip(), g.mid[0], g.mid[1], rand(500, 800), 1.2);
      }
      flow(g, dt, t);
    }
    beat = g.beat;
    geo = g;

    // The particles each move on; their extent joins the strands' in the canvas area.
    for (const rider of riders) rider.s += rider.v * dt;
    riders = riders.filter(rider => rider.v > 0 ? rider.s < rider.end : rider.s > rider.end);
    for (const flyer of flyers) flyer.t += dt / flyer.dur;
    for (const flyer of flyers.filter(flyer => flyer.t >= 1)) ride(g, flyer.k, flyer.s, flyer.end, rand(420, 560));
    flyers = flyers.filter(flyer => flyer.t < 1);
    for (const bit of bits) {
      bit.vy += bit.fall * dt;
      bit.vx *= 1 - 1.6 * dt;
      bit.vy *= 1 - (bit.fall ? 0.2 : 1.6) * dt;
      bit.x += bit.vx * dt;
      bit.y += bit.vy * dt;
      bit.life -= dt;
    }
    bits = bits.filter(bit => bit.life > 0);
    for (const ring of rings) ring.t += dt / 0.9;
    rings = rings.filter(ring => ring.t < 1);
    const reach = (x, y) => {
      minX = Math.min(minX, x);
      maxX = Math.max(maxX, x);
      minY = Math.min(minY, y);
      maxY = Math.max(maxY, y);
    };
    for (const flyer of flyers) { reach(flyer.x, flyer.y); reach(flyer.cx, flyer.cy); }
    for (const bit of bits) reach(bit.x, bit.y);

    // The canvas covers the strands, everything in flight, and room for a bloom, within
    // the page and half a screen around the view, so it moves only every 256px of
    // scrolling and its backing store stays bounded.
    const pad = 150;
    const band = g.across
      ? [g.from - 40, g.cross - pad, g.from + g.L + 40, g.cross + pad]
      : [g.cross - pad, g.from - 40, g.cross + pad, g.from + g.L + 40];
    const last = drawn ?? band;
    const page = [document.documentElement.clientWidth, document.body.getBoundingClientRect().bottom + scrollY];
    const view = [Math.floor((scrollY - innerHeight / 2) / 256) * 256, Math.ceil((scrollY + innerHeight * 1.5) / 256) * 256];
    const snap = (v, up) => (up ? Math.ceil : Math.floor)(v / 32) * 32;
    const l = Math.max(0, snap(Math.min(band[0], last[0], minX - 12)));
    const top = Math.max(0, view[0], snap(Math.min(band[1], last[1], minY - 12)));
    const r = Math.min(page[0], snap(Math.max(band[2], last[2], maxX + 12), true));
    const b = Math.min(page[1], view[1], snap(Math.max(band[3], last[3], maxY + 12), true));
    drawn = [minX - 12, minY - 12, maxX + 12, maxY + 12];
    if (r - l < 1 || b - top < 1) {
      if (!canvas.hidden) canvas.hidden = true;
      area = '';
      return;
    }
    if (canvas.hidden) canvas.hidden = false;
    const dpr = Math.min(2, devicePixelRatio || 1, Math.sqrt(MAX_PIXELS / ((r - l) * (b - top))));
    const next = `${l},${top},${r},${b},${dpr}`;
    if (next !== area) {
      area = next;
      canvas.width = Math.round((r - l) * dpr);
      canvas.height = Math.round((b - top) * dpr);
      canvas.style.width = `${r - l}px`;
      canvas.style.height = `${b - top}px`;
      canvas.style.transform = `translate(${l}px, ${top}px)`;
      painted = '';
    }
    // Nothing is drawn while the helix is scrolled out of view, and without motion the
    // picture changes only with the layout or the state.
    if (b < scrollY || top > scrollY + innerHeight) {
      painted = '';
      return;
    }
    const picture = `${still}|${area}|${state}|${g.progress}|${g.from}|${g.L}|${g.cross}|${g.gap}|${g.core}`;
    if (still && picture === painted) return;
    painted = picture;
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.clearRect(0, 0, canvas.width, canvas.height);
    ctx.setTransform(dpr, 0, 0, dpr, -l * dpr, -top * dpr);
    draw(g, now, dt, still);
  }

  function stamp(image, x, y, r, a) {
    if (a < 0.005 || r < 0.05) return;
    ctx.globalAlpha = Math.min(1, a);
    ctx.drawImage(image, x - r, y - r, 2 * r, 2 * r);
  }

  function draw(g, now, dt, still) {
    const S = g.scale;
    ctx.strokeStyle = '#fff';
    ctx.fillStyle = '#fff';
    ctx.lineCap = 'round';
    // Base pairs: each rung in two shades, brightest where it faces the reader.
    ctx.lineWidth = 1.1 * S;
    for (let i = 0; i < n; i++) {
      const a = bond[i] * vis[i] * rise[i] * Math.min(1.2, lit[i] + boost[i]) * (0.16 + 0.42 * face[i]);
      if (a < 0.01) continue;
      const j = 2 * i, mx = (px[j] + px[j + 1]) / 2, my = (py[j] + py[j + 1]) / 2;
      ctx.globalAlpha = Math.min(1, a);
      ctx.beginPath();
      ctx.moveTo(px[j], py[j]);
      ctx.lineTo(mx, my);
      ctx.stroke();
      ctx.globalAlpha = Math.min(1, a * 0.55);
      ctx.beginPath();
      ctx.moveTo(mx, my);
      ctx.lineTo(px[j + 1], py[j + 1]);
      ctx.stroke();
    }
    // Strands: a bead at every rung and two between, sized and shaded by depth.
    for (let j = 0; j < 2 * n; j++) {
      const i = j >> 1, v = vis[i] * Math.min(1, rise[i] * 3);
      if (v < 0.01) continue;
      const light = Math.min(1.25, lit[i] + boost[i]) * v;
      const depth = (pz[j] + 1) / 2;
      const r = S * (0.7 + 1.5 * depth), a = (0.22 + 0.78 * depth) * light;
      stamp(DOT, px[j], py[j], r, a);
      if (core[i] > 0.05 && depth > 0.55) stamp(GLOW, px[j], py[j], r * 4.5, a * 0.16 * core[i]);
      if (i < n - 1) {
        const k = j + 2, depth2 = (pz[k] + 1) / 2;
        for (const f of [1 / 3, 2 / 3]) {
          const dd = depth + (depth2 - depth) * f;
          stamp(DOT, px[j] + (px[k] - px[j]) * f, py[j] + (py[k] - py[j]) * f, S * (0.55 + 1.2 * dd), (0.2 + 0.7 * dd) * light * 0.9);
        }
      }
    }
    // The machine's air: motes drifting through the core, quicker while it works.
    if (g.room && !still) {
      const w = g.room.r - g.room.l, h = g.room.b - g.room.t;
      const want = Math.round(clamp(w * h / 1800, 8, 48));
      while (motes.length < want) motes.push({ u: Math.random(), v: Math.random(), du: rand(-0.02, 0.02), dv: rand(-0.02, 0.02), r: rand(0.5, 1.3), a: rand(0.12, 0.4), p: rand(0, TAU) });
      motes.length = want;
      const energy = g.mode === 'running' ? 4 : g.mode === 'accepted' || g.mode === 'sending' ? 2 : 1;
      for (const mote of motes) {
        mote.du = clamp(mote.du + rand(-0.012, 0.012) * dt, -0.03, 0.03);
        mote.dv = clamp(mote.dv + rand(-0.012, 0.012) * dt, -0.03, 0.03);
        mote.u = (mote.u + mote.du * energy * dt + 1) % 1;
        mote.v = (mote.v + mote.dv * energy * dt + 1) % 1;
        stamp(DOT, g.room.l + mote.u * w, g.room.t + mote.v * h, mote.r, mote.a * (0.55 + 0.45 * Math.sin(now / 600 + mote.p)));
      }
    }
    // Dust: a halo drifting along the links with the flow, quick while it carries something.
    if (!still) {
      const want = Math.round(g.L / (g.task ? 30 : 14));
      while (dust.length < want) dust.push({ s: Math.random(), side: flip() ? 1 : -1, off: rand(1.4, 3.4), v: rand(14, 42), p: rand(0, TAU), r: rand(0.5, 1.3), a: rand(0.18, 0.5) });
      dust.length = want;
      const pace = DRIFT[g.mode] ?? 1;
      for (const mote of dust) {
        mote.s = (mote.s + mote.v * pace * dt / g.L) % 1;
        const s = mote.s, d = s * g.L;
        if (g.gap && s > g.gap[0] - 0.02 && s < g.gap[1] + 0.02) continue;
        if (g.core && s > g.core[0] - 0.02 && s < g.core[1] + 0.02) continue;
        if (g.cut != null && s > g.cut) continue;
        const [x, y] = point(g, d), off = mote.side * (mote.off * g.amp + 5 * Math.sin(now / 1100 + mote.p));
        const fade = ramp(0, 0.06, s) * ramp(0, 0.06, 1 - s) * (0.55 + 0.45 * Math.sin(now / 700 + mote.p));
        stamp(DOT, x + (g.across ? 0 : off), y + (g.across ? off : 0), mote.r * S, mote.a * fade);
      }
    }
    // Riders: bright beads carried along a strand.
    for (const rider of riders) {
      spot(rider.s, rider.k);
      const depth = (qz + 1) / 2, r = S * (1.2 + 1.3 * depth) * rider.size, a = (0.6 + 0.4 * depth) * qv;
      stamp(DOT, qx, qy, r, a);
      const s = rider.s;
      if (g.core && s > g.core[0] && s < g.core[1]) stamp(GLOW, qx, qy, r * 5, a * 0.3);
    }
    // Flyers: input leaving its field for the strand, with a short trail.
    for (const flyer of flyers) {
      if (flyer.t < 0) continue;
      spot(flyer.s, flyer.k);
      const t = flyer.t < 0.5 ? 4 * flyer.t ** 3 : 1 - (-2 * flyer.t + 2) ** 3 / 2, u = 1 - t;
      const x = u * u * flyer.x + 2 * u * t * flyer.cx + t * t * qx, y = u * u * flyer.y + 2 * u * t * flyer.cy + t * t * qy;
      flyer.trail.unshift(x, y);
      flyer.trail.length = Math.min(flyer.trail.length, 16);
      for (let i = 2; i < flyer.trail.length; i += 2) stamp(DOT, flyer.trail[i], flyer.trail[i + 1], S * (1.5 - i * 0.06), 0.55 - i * 0.032);
      stamp(DOT, x, y, S * 1.7, 0.95);
    }
    for (const bit of bits) stamp(DOT, bit.x, bit.y, bit.r, 0.85 * Math.min(1, bit.life / bit.max * 1.6));
    ctx.lineWidth = 1.4;
    for (const ring of rings) {
      ctx.globalAlpha = 0.55 * (1 - ring.t);
      ctx.beginPath();
      ctx.arc(ring.x, ring.y, 8 + 64 * (1 - (1 - ring.t) ** 3), 0, TAU);
      ctx.stroke();
    }
    // The result's node, where the strands end: open, filled once the result is in, broken by a failure.
    if (g.task) {
      const settle = still ? 1 : 1 - Math.exp(-8 * dt);
      filled += ((g.mode === 'done' ? 1 : 0) - filled) * settle;
      broken += ((g.cut != null ? 1 : 0) - broken) * settle;
      const [x, y] = point(g, g.L), r = 7.5 * S;
      ctx.globalAlpha = 1 - 0.65 * broken;
      ctx.lineWidth = 2.25 * S;
      ctx.setLineDash(broken > 0.5 ? [2.5, 3.6] : []);
      ctx.beginPath();
      ctx.arc(x, y, r, 0, TAU);
      ctx.stroke();
      ctx.setLineDash([]);
      if (filled > 0.01) {
        ctx.beginPath();
        ctx.arc(x, y, r * filled, 0, TAU);
        ctx.fill();
      }
    }
    // While a run works with a known end, a small ring of beads circles the fork where the strand zips.
    if (g.mode === 'running' && g.progress != null && !still) {
      const m1 = g.mid[1], d = (m1 + g.progress * (1 - m1)) * g.L;
      const [x, y] = point(g, d), radius = g.amp * 1.25;
      for (let i = 0; i < 5; i++) {
        const angle = now / 180 + i * TAU / 5, depth = (Math.cos(angle) + 1) / 2;
        const along = Math.cos(angle) * radius * 0.3, side = Math.sin(angle) * radius;
        stamp(DOT, x + (g.across ? along : side), y + (g.across ? side : along), g.scale * (0.9 + 1.2 * depth), 0.4 + 0.6 * depth);
      }
    }
    ctx.globalAlpha = 1;
  }

  requestAnimationFrame(frame);
})();
