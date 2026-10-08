import { chromium } from 'playwright';
import { createServer } from 'node:http';
import { readFile, readdir, stat } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '../../docs/diagrams');
const modules = path.join(here, 'node_modules');
const watching = process.argv.includes('--watch');
const server = createServer(async (req, res) => {
  try {
    if (req.url === '/') {
      res.setHeader('Content-Type', 'text/html; charset=utf-8');
      res.end('<!doctype html><style>body{margin:0;background:white}#diagram{display:inline-block;padding:24px}</style><div id="diagram"></div><script type="module">import mermaid from "/mermaid/dist/mermaid.esm.min.mjs"; mermaid.initialize({startOnLoad:false,securityLevel:"strict",theme:"default",look:"classic",layout:"dagre",flowchart:{useMaxWidth:false},sequence:{useMaxWidth:false},er:{useMaxWidth:false},state:{useMaxWidth:false},usecase:{useMaxWidth:false}}); window.draw=async(source)=>{await mermaid.parse(source);const {svg}=await mermaid.render("diagramSvg",source);document.getElementById("diagram").innerHTML=svg;await document.fonts.ready;};</script>');
      return;
    }
    const filename = path.resolve(modules, '.' + decodeURIComponent(req.url.split('?')[0]));
    if (!filename.startsWith(modules + path.sep)) { res.writeHead(403).end(); return; }
    res.setHeader('Content-Type', filename.endsWith('.css') ? 'text/css' : 'text/javascript');
    res.end(await readFile(filename));
  } catch { res.writeHead(404).end(); }
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
let browser;
let stopping = false;
async function stop() {
  stopping = true;
  await browser?.close();
  server.close();
}
process.on('SIGINT', stop);
process.on('SIGTERM', stop);
try {
  browser = await chromium.launch({channel:'chrome',headless:true});
  const page = await browser.newPage({viewport:{width:1600,height:1200},deviceScaleFactor:2});
  await page.goto(`http://127.0.0.1:${server.address().port}/`);
  await page.waitForFunction(() => typeof window.draw === 'function');
  const timestamps = new Map();
  do {
    for (const entry of await readdir(root, {withFileTypes:true})) {
      if (!entry.isDirectory()) continue;
      const folder = path.join(root, entry.name);
      for (const filename of await readdir(folder)) {
        if (!filename.endsWith('.mmd')) continue;
        const source = path.join(folder, filename);
        const modified = (await stat(source)).mtimeMs;
        if (timestamps.get(source) === modified) continue;
        try {
          await page.evaluate(text => window.draw(text), await readFile(source, 'utf8'));
          const output = source.replace(/\.mmd$/, '.png');
          await page.locator('#diagram').screenshot({path:output});
          console.log(`PNG: ${path.relative(root, output)}`);
        } catch (error) {
          console.error(`${source}: ${error.message}`);
          if (!watching) throw error;
        }
        timestamps.set(source, modified);
      }
    }
    if (watching && !stopping) await new Promise(resolve => setTimeout(resolve, 500));
  } while (watching && !stopping);
} finally { await stop(); }
