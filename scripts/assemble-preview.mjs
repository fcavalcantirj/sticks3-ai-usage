import { readFile } from 'node:fs/promises';

const root = new URL('../', import.meta.url);
export async function assemble({ preview = true } = {}) {
  const read = name => readFile(new URL(name, root), 'utf8');
  const cssFiles = ['base.css', 'views.css'];
  const jsFiles = ['core.js', 'settings.js', 'device.js', 'render.js', 'app.js'];
  const [html, styles, scripts] = await Promise.all([
    read('internal/web/index.html'),
    Promise.all(cssFiles.map(f => read('internal/web/' + f))),
    Promise.all(jsFiles.map(f => read('internal/web/' + f))),
  ]);
  let adapter = '';
  if (preview) {
    const fixture = JSON.parse(await read('testdata/scenarios/stats-demo/stats.json'));
    const data = await read('preview/data.js');
    adapter = 'window.__PREVIEW_DATA__ = ' + JSON.stringify(fixture).replace(/</g, '\\u003c') + ';\n' + data + '\n' + await read('preview/adapter.js') + '\n';
  }
  return html.replace('/* {{styles}} */', styles.join('\n')).replace('/* {{scripts}} */', () => adapter + scripts.join('\n'));
}
