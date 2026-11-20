#!/usr/bin/env node
// presenter-check.mjs — the reveal choreography as a regression test.
//
// Drives the real presenter page in headless Chromium over CDP: paints an
// award from synthetic data, calls revealWinner(), and samples the winner
// block's height and opacity all the way through the animation. The page
// makes timing promises no server-side test can see — the name fades in only
// once the title has stopped moving, the block grows monotonically, the name
// never exceeds two lines, the container stays at 80% of the window and
// inside it — and every one of them is checkable from the outside, which is
// what this script does.
//
// The geometry pass runs once per color-theme variant (a gradient theme, a
// solid one, and no theme at all) because colors ride on the same paint as
// the choreography: a theme that broke the layout would otherwise only be
// found by the venue that picked the colors. Each variant also gets a color
// pass, reading the computed styles back out to check that applyTheme put
// them where present.css reads them.
//
// Usage:
//   node scripts/presenter-check.mjs [--url URL] [--width N] [--height N]
//                                    [--name TEXT] [--chromium PATH]
//
//   --url       presenter page, default http://127.0.0.1:1323/public/present
//   --width     emulated viewport width, default 1920
//   --height    emulated viewport height, default 1080
//   --name      recipient name to reveal; defaults to a 53-character name,
//               the worst case the two-line fit has ever been asked to hold
//   --chromium  browser binary, default $CHROMIUM or /usr/bin/chromium
//
// The award comes from synthetic data rendered through the page's own
// Mustache template, so the server needs no seeded rows — only to be running,
// because the page's stylesheet, scripts, and WebSocket all come from it.
//
// Exits 0 when every assertion holds, 1 on the first failure, 2 when the
// page never became inspectable (server down, browser missing).

import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

function arg(name, fallback) {
  const i = process.argv.indexOf('--' + name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : fallback;
}

const URL_ = arg('url', 'http://127.0.0.1:1323/public/present');
const WIDTH = parseInt(arg('width', '1920'), 10);
const HEIGHT = parseInt(arg('height', '1080'), 10);
const NAME = arg('name', 'Dr Bartholomew Fitzgerald-Montgomery-Constantinople');
const CHROMIUM = process.env.CHROMIUM || arg('chromium', '/usr/bin/chromium');
const DEBUG_PORT = 9400 + Math.floor(Math.random() * 60);

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function waitForTarget(port, deadlineMs) {
  const until = Date.now() + deadlineMs;
  while (Date.now() < until) {
    try {
      const resp = await fetch(`http://127.0.0.1:${port}/json/list`);
      const targets = await resp.json();
      const page = targets.find((t) => t.type === 'page');
      if (page) return page;
    } catch {
      // not up yet
    }
    await sleep(150);
  }
  return null;
}

class CDP {
  constructor(ws) {
    this.ws = ws;
    this.next = 1;
    this.pending = new Map();
    ws.addEventListener('message', (ev) => {
      const msg = JSON.parse(ev.data);
      const p = this.pending.get(msg.id);
      if (!p) return;
      this.pending.delete(msg.id);
      if (msg.error) p.reject(new Error(msg.error.message));
      else p.resolve(msg.result);
    });
  }

  send(method, params = {}) {
    const id = this.next++;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.ws.send(JSON.stringify({ id, method, params }));
    });
  }

  async evaluate(expression) {
    const r = await this.send('Runtime.evaluate', {
      expression,
      awaitPromise: true,
      returnByValue: true,
    });
    if (r.exceptionDetails) {
      throw new Error('page exception: ' + JSON.stringify(r.exceptionDetails));
    }
    return r.result.value;
  }
}

// The whole choreography runs in one page-side evaluation: paint through the
// page's own template, start the reveal, sample every animation frame, and
// hand the trace back for assertions. Frame-rate sampling rather than a fixed
// interval because the failure being hunted is a one-frame jump.
const TRACE = (name, theme) => `
  (async () => {
    if (typeof revealWinner !== 'function' || typeof Mustache === 'undefined') {
      return { fatal: 'presenter globals missing: the page did not load fully' };
    }
    const data = {
      Award: {
        DispTitle: 'Presenting Check',
        DispSub: 'regression run',
        ColorTheme: ${JSON.stringify(theme)},
      },
      Recipient: { Name: ${JSON.stringify(name)} },
    };
    container.innerHTML = Mustache.render(awardTemplate, { d: data });
    applyTheme(data.Award.ColorTheme);
    void container.offsetWidth;
    container.classList.add('award-container-shown');

    const box = () => {
      const win = document.getElementById('winner');
      const con = document.getElementById('award-container');
      if (!win || !con) return null;
      const wr = win.getBoundingClientRect();
      const cr = con.getBoundingClientRect();
      return {
        h: wr.height,
        o: parseFloat(getComputedStyle(win).opacity),
        left: cr.left,
        right: cr.right,
        width: cr.width,
        lines: countLines(win.querySelector('h1')),
      };
  };

    await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
    const painted = box();
    if (!painted) return { fatal: 'no award on the board after painting' };

    revealWinner();

    const samples = [];
    const t0 = performance.now();
    for (;;) {
      await new Promise((r) => requestAnimationFrame(r));
      const s = box();
      if (s) samples.push({ t: performance.now() - t0, ...s });
      const done = document.getElementById('winner')
        .classList.contains('award-winner-stage3');
      if (done && s && s.o > 0.995) break;
      if (performance.now() - t0 > 15000) {
        return { fatal: 'reveal never completed: 15s with no final opacity',
                 samples: samples };
      }
    }
    return { painted: painted, samples: samples,
             innerWidth: window.innerWidth };
  })()
`;

// COLOR_CHECK reads back what applyTheme painted: the custom properties the
// script set, and the computed styles present.css derived from them. The wait
// covers the background-color transition, whose interpolated middle would
// otherwise be what gets read.
const COLOR_CHECK = () => `
  (async () => {
    await new Promise((r) => setTimeout(r, 1300));
    const cs = getComputedStyle(document.body);
    const decl = document.body.style;
    const props = {};
    for (const p of ['--award-bg', '--award-bg-image', '--award-text',
                     '--award-winner']) {
      props[p] = decl.getPropertyValue(p).trim();
    }
    return {
      bgColor: cs.backgroundColor,
      bgImage: cs.backgroundImage,
      awardColor: getComputedStyle(document.querySelector('#award h1')).color,
      winnerColor: getComputedStyle(document.querySelector('#winner h1')).color,
      props: props,
    };
  })()
`;

function rgb(hex) {
  const n = parseInt(hex.slice(1), 16);
  return `rgb(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255})`;
}

// The states a paint can arrive in: a two-color gradient, a five-color one,
// a solid background, and no theme at all. The colors are ones a venue would
// plausibly type: every pairing in them clears the contrast bar the server
// enforces, so the checks below are about plumbing and not about taste.
const VARIANTS = [
  {
    name: 'gradient',
    theme: { Bg: '#1a1a2e', BgTo: '#16213e', BgDir: 'to bottom right',
             Text: '#ffffff', Winner: '#ffd700' },
  },
  {
    name: 'five',
    theme: { Bg: '#1a1a2e', BgTo: '#16213e', Bg3: '#0f3460', Bg4: '#533483',
             Bg5: '#1b1464', BgDir: 'to right',
             Text: '#ffffff', Winner: '#ffd700' },
  },
  {
    name: 'solid',
    theme: { Bg: '#123456', BgTo: '', BgDir: '',
             Text: '#ffffff', Winner: '#ffd700' },
  },
  { name: 'none', theme: null },
];

function colorAssertions(variant, c) {
  const msgs = [];
  const t = variant.theme;

  if (!t) {
    // With no theme every property is cleared and the stylesheet's own
    // fallbacks paint the pre-theme screen: ivory behind black.
    if (c.bgColor !== 'rgb(255, 255, 240)') {
      msgs.push(`body background is ${c.bgColor}, want ivory`);
    }
    for (const [p, v] of Object.entries(c.props)) {
      if (v !== '') msgs.push(`${p} still holds ${v}`);
    }
    if (c.awardColor !== 'rgb(0, 0, 0)') {
      msgs.push(`award text is ${c.awardColor}, want black`);
    }
    if (c.winnerColor !== 'rgb(0, 0, 0)') {
      msgs.push(`winner name is ${c.winnerColor}, want black`);
    }
    return msgs;
  }

  if (c.bgColor !== rgb(t.Bg)) {
    msgs.push(`body background is ${c.bgColor}, want ${rgb(t.Bg)}`);
  }
  const stops = [t.Bg].concat([t.BgTo, t.Bg3, t.Bg4, t.Bg5].filter(Boolean));
  if (stops.length > 1) {
    // A gradient theme: the image the script composed must carry every stop.
    // The inline custom property is checked spelled exactly as applyTheme
    // writes it (the computed style would serialize the direction keyword
    // as an angle, so keywords are only asserted inline).
    if (!c.bgImage.includes('linear-gradient(')) {
      msgs.push(`background-image is ${c.bgImage}, want a linear-gradient`);
    }
    for (const stop of stops) {
      if (!c.bgImage.includes(rgb(stop))) {
        msgs.push(`background-image ${c.bgImage} is missing the stop ${stop}`);
      }
    }
    const want = 'linear-gradient(' + (t.BgDir || 'to bottom') + ', ' + stops.join(', ') + ')';
    if (c.props['--award-bg-image'] !== want) {
      msgs.push(`--award-bg-image is ${c.props['--award-bg-image']}, want ${want}`);
    }
  } else {
    // A solid theme: no image is set at all, so the stylesheet's none keeps
    // the plain color showing, and any image the previous award painted is
    // cleared along with every other property.
    if (c.bgImage !== 'none') {
      msgs.push(`solid theme's background-image ${c.bgImage} is not none`);
    }
    if (c.props['--award-bg-image'] !== '') {
      msgs.push(`--award-bg-image still holds ${c.props['--award-bg-image']}`);
    }
  }

  if (c.awardColor !== rgb(t.Text)) {
    msgs.push(`award text is ${c.awardColor}, want ${rgb(t.Text)}`);
  }
  if (c.winnerColor !== rgb(t.Winner)) {
    msgs.push(`winner name is ${c.winnerColor}, want ${rgb(t.Winner)}`);
  }
  return msgs;
}

function fail(msgs) {
  for (const m of msgs) console.error('FAIL: ' + m);
  process.exitCode = 1;
  throw new Error('assertions failed');
}

// variantPass drives one color-theme variant: the full choreography with the
// variant's theme painted on, then the color readback. Geometry assertions
// run per variant because the theme and the reveal share one paint, and the
// only honest way to know a gradient does not disturb the layout is to run
// the layout with the gradient on.
async function variantPass(cdp, variant) {
  const run = await cdp.evaluate(TRACE(NAME, variant.theme));
  if (run.fatal) {
    fail([variant.name + ': ' + run.fatal]);
  }

  const msgs = [];
  const samples = run.samples;
  if (samples.length < 5) msgs.push('too few samples for the reveal to have animated');

  // The container is 80% of the window and never crosses an edge; the fit
  // measures against this width, so widening it silently rewrites the
  // two-line guarantee.
  const want = 0.8 * run.innerWidth;
  if (Math.abs(run.painted.width - want) > 2) {
    msgs.push(`container is ${run.painted.width}px, want 80% of ${run.innerWidth} (${want}px)`);
  }
  for (const s of samples) {
    if (s.left < -0.5 || s.right > run.innerWidth + 0.5) {
      msgs.push(`container crosses the viewport edge (left ${s.left}, right ${s.right})`);
      break;
    }
  }

  // Height grows monotonically and never jumps: the growing block carries
  // the title, so a jump is the whole board snapping upward.
  let lastH = run.painted.h;
  for (const s of samples) {
    if (s.h < lastH - 0.5) msgs.push(`height shrank mid-reveal: ${lastH} to ${s.h}`);
    if (s.h - lastH > 30) msgs.push(`height jumped ${Math.round(s.h - lastH)}px in one frame`);
    lastH = s.h;
  }

  // The name appears when the award stops moving, not on a timer: opacity
  // must still be zero at the sample where the height settles.
  let settledAt = -1;
  for (let i = 1; i < samples.length; i++) {
    const quiet = samples.slice(Math.max(0, i - 3), i + 1)
      .every((s) => Math.abs(s.h - samples[i].h) < 0.5);
    if (quiet && samples[i].o <= 0.005) { settledAt = i; break; }
  }
  for (let i = 0; i < samples.length && settledAt >= 0; i++) {
    if (samples[i].o > 0.005 && i < settledAt) {
      msgs.push(`name visible before the height settled (t=${Math.round(samples[i].t)}ms)`);
      break;
    }
  }
  if (settledAt < 0) msgs.push('never saw a settled height with the name still hidden');

  for (const s of samples) {
    if (s.lines > 2) { msgs.push(`the name exceeded two lines (${s.lines})`); break; }
  }

  const final = samples[samples.length - 1];
  if (!(final.h > 0)) msgs.push('final height is zero: nothing was revealed');
  if (!(final.o > 0.995)) msgs.push(`final opacity ${final.o}, want 1`);

  // The colors were painted by the same call that painted this award, so
  // the readback belongs to this variant and no other. The wait inside
  // COLOR_CHECK covers the background-color transition.
  msgs.push(...colorAssertions(variant, await cdp.evaluate(COLOR_CHECK())));

  if (msgs.length) fail(msgs.map((m) => variant.name + ': ' + m));
  console.log(`PASS: ${variant.name} theme at ${WIDTH}x${HEIGHT}, ` +
    `${samples.length} frames, ` +
    `final height ${Math.round(final.h)}px, ` +
    `container ${Math.round(run.painted.width)}px of ${run.innerWidth}px`);
}

async function main() {
  const userDataDir = mkdtempSync(join(tmpdir(), 'presenter-check-'));
  const browser = spawn(CHROMIUM, [
    '--headless=new',
    '--no-sandbox',
    '--disable-gpu',
    `--remote-debugging-port=${DEBUG_PORT}`,
    `--user-data-dir=${userDataDir}`,
    'about:blank',
  ], { stdio: 'ignore' });

  let exitCode = 0;
  try {
    const target = await waitForTarget(DEBUG_PORT, 15000);
    if (!target) {
      console.error(`FAIL: no ${CHROMIUM} page on port ${DEBUG_PORT}; is the browser installed?`);
      process.exitCode = 2;
      return;
    }

    const ws = new WebSocket(target.webSocketDebuggerUrl);
    await new Promise((resolve, reject) => {
      ws.addEventListener('open', resolve, { once: true });
      ws.addEventListener('error', reject, { once: true });
    });
    const cdp = new CDP(ws);

    await cdp.send('Page.enable');
    await cdp.send('Runtime.enable');
    await cdp.send('Emulation.setDeviceMetricsOverride', {
      width: WIDTH, height: HEIGHT, deviceScaleFactor: 1, mobile: false,
    });
    await cdp.send('Page.navigate', { url: URL_ });

    let ready = '';
    for (let i = 0; i < 100 && ready !== 'complete'; i++) {
      await sleep(100);
      ready = await cdp.evaluate('document.readyState');
    }
    if (ready !== 'complete') {
      console.error(`FAIL: ${URL_} never reached readyState complete; is the server running?`);
      process.exitCode = 2;
      return;
    }

    for (const variant of VARIANTS) {
      await variantPass(cdp, variant);
    }

    ws.close();
  } catch (e) {
    if (!process.exitCode) {
      console.error('FAIL: ' + e.message);
      exitCode = 1;
    } else {
      exitCode = process.exitCode;
    }
  } finally {
    browser.kill();
    await sleep(300);
    rmSync(userDataDir, { recursive: true, force: true });
    process.exit(exitCode);
  }
}

main();
