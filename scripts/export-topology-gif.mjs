/**
 * export-topology-gif.mjs
 *
 * Renders the "runtime topology" SVG diagram from
 * docs/public/agent-orchestration.html into a looping GIF.
 *
 * The diagram has one continuous CSS animation (`dashFlow`, 2.6s) plus
 * one-shot `fadeUp` entrance animations. This script drives every
 * animation deterministically by stepping `Animation.currentTime`, so
 * the captured frames form an exact, seamless 2.6s loop.
 *
 * Output: docs/public/agent-orchestration-topology-<theme>.gif
 *
 * Usage:  node scripts/export-topology-gif.mjs
 * Deps:   playwright (npm devDependency) + ffmpeg on PATH.
 */
import { chromium } from 'playwright';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const htmlPath = join(repoRoot, 'docs/public/agent-orchestration.html');
const outDir = join(repoRoot, 'docs/public');

const PERIOD_MS = 2600; // dashFlow keyframe duration
const FPS = 25;
const FRAMES = (PERIOD_MS / 1000) * FPS; // 65 -> exact loop
const SCALE = 2; // supersample, downscaled by ffmpeg for clean edges
const THEMES = ['dark', 'light'];

async function captureTheme(browser, theme, tmp) {
  const page = await browser.newPage({
    viewport: { width: 1400, height: 1000 },
    deviceScaleFactor: SCALE,
    reducedMotion: 'no-preference',
  });

  await page.addInitScript((t) => {
    try { localStorage.setItem('zf-theme', t); } catch {}
  }, theme);
  await page.goto(pathToFileURL(htmlPath).href);
  await page.evaluate((t) => document.documentElement.setAttribute('data-theme', t), theme);

  const svg = page.locator('svg.diagram');
  await svg.waitFor({ state: 'visible' });
  // Let fonts load and entrance animations settle so layout is final
  // before measuring; an early measurement reports a collapsed box.
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(1500);
  const box = await svg.boundingBox();
  // Scroll the diagram into view so the element screenshot captures it
  // whole; a viewport-relative `clip` would crop off-screen rows.
  await svg.scrollIntoViewIfNeeded();

  for (let i = 0; i < FRAMES; i++) {
    const phase = (i / FRAMES) * PERIOD_MS;
    await page.evaluate(async (p) => {
      for (const a of document.getAnimations()) {
        a.pause();
        const name = a.animationName || '';
        if (name === 'dashFlow') {
          a.currentTime = p; // step the looping edge animation
        } else {
          // settle every one-shot entrance animation to its final frame
          const dur = a.effect?.getTiming?.().duration;
          a.currentTime = typeof dur === 'number' ? dur : 100000;
        }
      }
      await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
    }, phase);

    const file = join(tmp, `${theme}-${String(i).padStart(4, '0')}.png`);
    await svg.screenshot({ path: file });
  }

  await page.close();
  return box;
}

function buildGif(theme, tmp, box) {
  const pattern = join(tmp, `${theme}-%04d.png`);
  const palette = join(tmp, `${theme}-palette.png`);
  const out = join(outDir, `agent-orchestration-topology-${theme}.gif`);
  const w = Math.round(box.width); // downscale from SCALE-x supersample
  const vf = `fps=${FPS},scale=${w}:-1:flags=lanczos`;

  execFileSync('ffmpeg', [
    '-y', '-framerate', String(FPS), '-i', pattern,
    '-vf', `${vf},palettegen=stats_mode=full`, palette,
  ], { stdio: 'inherit' });

  execFileSync('ffmpeg', [
    '-y', '-framerate', String(FPS), '-i', pattern, '-i', palette,
    '-lavfi', `${vf} [x]; [x][1:v] paletteuse=dither=bayer:bayer_scale=3`,
    '-loop', '0', out,
  ], { stdio: 'inherit' });

  return out;
}

const tmp = mkdtempSync(join(tmpdir(), 'zf-topology-'));
const browser = await chromium.launch();
try {
  for (const theme of THEMES) {
    const box = await captureTheme(browser, theme, tmp);
    const out = buildGif(theme, tmp, box);
    console.log(`wrote ${out}`);
  }
} finally {
  await browser.close();
  rmSync(tmp, { recursive: true, force: true });
}
