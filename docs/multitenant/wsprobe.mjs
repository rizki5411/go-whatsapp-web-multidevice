// Klien WebSocket untuk verifikasi isolasi fase 06.
//
// Menghubungkan beberapa identitas sekaligus, memicu sebuah aksi, lalu mencatat
// event apa yang diterima masing-masing koneksi.
//
// Autentikasi lewat ?authorization= karena itulah jalur yang dipakai browser
// (WebSocket API tidak bisa mengirim header sendiri). device_id wajib karena
// /ws terdaftar di headerDeviceGroup, sehingga DeviceMiddleware menuntut device
// yang bisa diresolve sebelum upgrade.
//
// Pemakaian:
//   node wsprobe.mjs <ws-base> "<label=user:pass|deviceId,...>" [aksi]
// Aksi:
//   fetch:<label>                     kirim FETCH_DEVICES dari satu koneksi
//   http:<METHOD>|<url>|<user:pass>   picu event lewat REST

const [, , baseUrl, spec, action] = process.argv;

const peers = spec.split(',').map((entry) => {
  const [label, rest] = entry.split('=');
  const [cred, deviceId] = rest.split('|');
  return { label, cred, deviceId };
});

const received = new Map(peers.map((p) => [p.label, []]));
const sockets = [];

function connect(peer) {
  return new Promise((resolve, reject) => {
    const auth = Buffer.from(peer.cred).toString('base64');
    const suffix = peer.deviceId ? '&device_id=' + encodeURIComponent(peer.deviceId) : '';
    const ws = new WebSocket(baseUrl + '/ws?authorization=' + auth + suffix);
    ws.addEventListener('open', () => resolve(ws));
    ws.addEventListener('error', () => reject(new Error('gagal konek (non-101)')));
    ws.addEventListener('message', (event) => {
      try {
        received.get(peer.label).push(JSON.parse(event.data));
      } catch {
        received.get(peer.label).push({ raw: String(event.data) });
      }
    });
    sockets.push(ws);
  });
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

(async () => {
  for (const peer of peers) {
    try {
      peer.ws = await connect(peer);
    } catch (err) {
      console.log('KONEK GAGAL ' + peer.label + ': ' + err.message);
    }
  }
  await sleep(400);

  if (action && action.startsWith('http:')) {
    const parts = action.slice('http:'.length).split('|');
    const method = parts[0];
    const url = parts[1];
    const cred = parts[2];
    const res = await fetch(url, {
      method,
      headers: { Authorization: 'Basic ' + Buffer.from(cred).toString('base64') },
    });
    console.log('pemicu ' + method + ' ' + url + ' -> HTTP ' + res.status);
  }

  if (action && action.startsWith('fetch:')) {
    const who = action.slice('fetch:'.length);
    const peer = peers.find((p) => p.label === who);
    if (peer && peer.ws) peer.ws.send(JSON.stringify({ code: 'FETCH_DEVICES' }));
  }

  await sleep(1500);

  for (const peer of peers) {
    const events = received.get(peer.label);
    const summary = events.map((e) => e.code + (e.device_id ? '(' + e.device_id + ')' : ''));
    console.log(peer.label + ': ' + events.length + ' event -> ' + JSON.stringify(summary));
    for (const e of events) {
      if (e.code === 'LIST_DEVICES') {
        console.log('   LIST_DEVICES isi: ' + JSON.stringify((e.result || []).map((d) => d.device)));
      }
      if (e.code === 'DEVICE_LOGGED_OUT') {
        console.log('   DEVICE_LOGGED_OUT result keys: ' + JSON.stringify(Object.keys(e.result || {})));
      }
    }
  }

  for (const ws of sockets) ws.close();
  process.exit(0);
})();
