// Render docs/overview/overview.html into the README's light and dark images.
import { chromium } from 'playwright';

const page = new URL('../docs/overview/overview.html', import.meta.url);
const browser = await chromium.launch();
try {
  for (const colorScheme of ['light', 'dark']) {
    const tab = await browser.newPage({ viewport: { width: 1280, height: 900 }, deviceScaleFactor: 2, colorScheme, reducedMotion: 'reduce' });
    await tab.goto(page.href);
    await tab.waitForSelector('body[data-painted]', { state: 'attached' });
    await tab.screenshot({ path: new URL(`${colorScheme}.png`, page).pathname, fullPage: true });
  }
} finally {
  await browser.close();
}
