/* ARFL Private-Connection Client — screens. Loaded after components.jsx. */

/* ---- node constellation — decentralised network motif (shared) ---- */
function Globe({ size = 220, color = 'var(--cyan-500)', opacity = 0.55, spin = false }) {
  const nodes = [
    [100, 30], [56, 52], [148, 48], [30, 96], [172, 90], [100, 78],
    [70, 110], [134, 118], [44, 150], [160, 152], [100, 134], [100, 172],
    [86, 100], [120, 88], [62, 80], [142, 78],
  ];
  const links = [
    [0, 1], [0, 2], [0, 13], [1, 14], [1, 5], [2, 15], [2, 5],
    [3, 6], [3, 1], [4, 7], [4, 15], [5, 12], [5, 13], [6, 12],
    [7, 13], [6, 10], [7, 10], [8, 6], [9, 7], [8, 10], [9, 10],
    [10, 11], [12, 14], [13, 15], [11, 8], [11, 9],
  ];
  const live = new Set([0, 5, 10, 4, 8]);
  return (
    <svg width={size} height={size} viewBox="0 0 200 200" aria-hidden
      style={{ opacity, filter: 'drop-shadow(0 0 10px rgba(0,229,255,.3))', animation: spin ? 'arflSpin 60s linear infinite' : 'none', transformOrigin: '50% 50%' }}>
      <g stroke={color} strokeWidth="0.5" opacity="0.55">
        {links.map(([a, b], i) =>
          <line key={i} x1={nodes[a][0]} y1={nodes[a][1]} x2={nodes[b][0]} y2={nodes[b][1]} />)}
      </g>
      {nodes.map(([x, y], i) => {
        const on = live.has(i);
        return <circle key={i} cx={x} cy={y} r={on ? 3 : 1.8}
          fill={on ? color : 'var(--lav-500)'}
          style={on ? { filter: `drop-shadow(0 0 4px ${color})` } : undefined} />;
      })}
    </svg>
  );
}

const NODES = [
  { id: 'tyo-07', city: 'Tokyo', cc: 'JP', ping: 12, load: 0.34, sats: 18, kana: 'トウキョウ' },
  { id: 'rkv-02', city: 'Reykjavík', cc: 'IS', ping: 41, load: 0.18, sats: 21, kana: 'アイスランド' },
  { id: 'zrh-11', city: 'Zürich', cc: 'CH', ping: 28, load: 0.52, sats: 16, kana: 'スイス' },
  { id: 'sgp-04', city: 'Singapore', cc: 'SG', ping: 22, load: 0.61, sats: 14, kana: 'シンガポール' },
  { id: 'mtl-09', city: 'Montréal', cc: 'CA', ping: 35, load: 0.27, sats: 15, kana: 'カナダ' },
];

/* ---------- CONNECT (home) ---------- */
function ConnectScreen({ status, node, sessUp, sessDown, sessSats, onToggle, onPickNode }) {
  const connected = status === 'connected';
  const connecting = status === 'connecting';
  const ringColor = connected ? 'var(--cyan-500)' : connecting ? 'var(--gold-500)' : 'var(--char-300)';
  const label = connected ? 'Connected' : connecting ? 'Establishing tunnel…' : 'Not connected';
  return (
    <Screen>
      {/* header */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: 6 }}>
        <div style={{ display:'flex', alignItems:'center', gap:8 }}>
          <BrandFrame padding={5} arm={7} stroke={1.5}>
            <span style={{ fontFamily: 'var(--font-wordmark)', fontWeight: 700, fontSize: 22, color: 'var(--lav-300)', textTransform: 'uppercase', letterSpacing: '.04em' }}>ARFL</span>
          </BrandFrame>
        </div>
        <span style={{ display: 'flex', alignItems: 'center', gap: 7, fontFamily: 'var(--font-mono)', fontSize: 11, letterSpacing: '.08em', color: connected ? 'var(--cyan-400)' : 'var(--fg3)' }}>
          <span style={{ width: 7, height: 7, borderRadius: '50%', background: connected ? 'var(--cyan-500)' : 'var(--char-300)', boxShadow: connected ? '0 0 6px var(--cyan-500)' : 'none' }}></span>
          {connected ? 'SECURE' : 'EXPOSED'}
        </span>
      </div>

      {/* connect orb */}
      <div style={{ position: 'relative', display: 'grid', placeItems: 'center', height: 320, marginTop: 14 }}>
        <div style={{ position: 'absolute', display: 'grid', placeItems: 'center', animation: connected ? 'arflPulse 3.2s var(--ease-in-out) infinite' : 'none' }}>
          <Globe size={250} opacity={connected ? 0.6 : 0.22} color={connected ? 'var(--cyan-500)' : 'var(--lav-600)'} spin={connected} />
        </div>
        <button onClick={onToggle} style={{
          position: 'relative', width: 150, height: 150, borderRadius: '50%', cursor: 'pointer',
          background: connected ? 'radial-gradient(circle at 50% 38%, #0a2a30, var(--black-700))' : 'var(--surface)',
          border: `2px solid ${ringColor}`, color: connected ? 'var(--cyan-400)' : 'var(--fg2)',
          display: 'grid', placeItems: 'center', gap: 0,
          boxShadow: connected ? 'var(--glow-cyan)' : connecting ? 'var(--glow-gold)' : 'none',
          transition: 'all .3s var(--ease-out)',
        }}>
          <Icon name="power" size={44} color={connected ? 'var(--cyan-400)' : connecting ? 'var(--gold-500)' : 'var(--fg2)'} />
          <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, letterSpacing: '.12em', marginTop: 6, textTransform: 'uppercase' }}>
            {connected ? 'Tap to stop' : connecting ? '· · ·' : 'Tap to connect'}
          </span>
        </button>
      </div>

      <div style={{ textAlign: 'center', fontFamily: 'var(--font-ui)', fontWeight: 600, fontSize: 18, color: connected ? 'var(--lav-300)' : 'var(--fg2)' }}>{label}</div>

      {/* node selector */}
      <button onClick={onPickNode} style={{
        width: '100%', marginTop: 18, display: 'flex', alignItems: 'center', gap: 12,
        background: 'var(--surface)', border: '1px solid var(--border)', clipPath: appCC(10), padding: '14px 16px', cursor: 'pointer',
      }}>
        <Icon name="globe" size={20} color="var(--lav-400)" />
        <div style={{ flex: 1, textAlign: 'left' }}>
          <div style={{ fontFamily: 'var(--font-ui)', fontWeight: 600, fontSize: 15, color: 'var(--lav-300)' }}>{node.city} <span style={{ color: 'var(--fg3)', fontFamily: 'var(--font-mono)', fontSize: 12 }}>{node.cc} · {node.id}</span></div>
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--fg3)' }}>{node.ping} ms · {node.sats} sats/GB</div>
        </div>
        <Icon name="chevron-right" size={18} color="var(--fg3)" />
      </button>

      {/* live readouts */}
      <div style={{ display: 'flex', gap: 10, marginTop: 12 }}>
        <Readout icon="arrow-down-left" label="Down" value={connected ? sessDown.toFixed(1) + ' MB' : '—'} tint="var(--cyan-400)" />
        <Readout icon="arrow-up-right" label="Up" value={connected ? sessUp.toFixed(1) + ' MB' : '—'} tint="var(--lav-400)" />
        <Readout icon="zap" label="Spent" value={connected ? sessSats + ' sat' : '—'} tint="var(--gold-500)" />
      </div>

      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 7, marginTop: 18, color: 'var(--fg3)' }}>
        <Icon name="eye-off" size={13} color="var(--fg3)" />
        <span style={{ fontFamily: 'var(--font-body)', fontSize: 12 }}>No logs. No account. Paid per bundle over Lightning.</span>
      </div>
    </Screen>
  );
}

function Readout({ icon, label, value, tint }) {
  return (
    <div style={{ flex: 1, clipPath: appCC(8), background: 'var(--border)', padding: 1, textAlign: 'center' }}>
      <div style={{ clipPath: appCC(8), background: 'var(--surface)', padding: '12px 10px' }}>
        <Icon name={icon} size={16} color={tint} />
        <div style={{ fontFamily: 'var(--font-mono)', fontSize: 15, color: 'var(--lav-300)', marginTop: 5 }}>{value}</div>
        <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9, letterSpacing: '.14em', textTransform: 'uppercase', color: 'var(--fg3)', marginTop: 2 }}>{label}</div>
      </div>
    </div>
  );
}

/* ---------- NODES ---------- */
function NodesScreen({ node, onSelect }) {
  return (
    <Screen>
      <ScreenTitle title="Exit nodes" sub="Discovered over Nostr · settled via Fedimint" />
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10, marginTop: 16 }}>
        {NODES.map(n => {
          const sel = n.id === node.id;
          return (
            <button key={n.id} onClick={() => onSelect(n)} style={{
              display: 'flex', alignItems: 'center', gap: 13, textAlign: 'left', cursor: 'pointer',
              background: sel ? 'var(--black-700)' : 'var(--surface)',
              border: `1px solid ${sel ? 'var(--border-cyan)' : 'var(--border)'}`,
              clipPath: appCC(10), padding: '14px 15px',
              boxShadow: sel ? '0 0 18px rgba(0,229,255,.12)' : 'none',
            }}>
              <div style={{ width: 38, height: 38, clipPath: appCC(6), background: 'var(--black-800)', display: 'grid', placeItems: 'center', fontFamily: 'var(--font-mono)', fontSize: 12, color: 'var(--lav-400)' }}>{n.cc}</div>
              <div style={{ flex: 1 }}>
                <div style={{ fontFamily: 'var(--font-ui)', fontWeight: 600, fontSize: 15, color: 'var(--lav-300)' }}>{n.city}</div>
                <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--fg3)' }}>{n.id} · {n.ping} ms · {n.sats} sats/GB</div>
              </div>
              <div style={{ width: 54, textAlign: 'right' }}>
                <Load v={n.load} />
              </div>
              {sel && <Icon name="check" size={18} color="var(--cyan-400)" />}
            </button>
          );
        })}
      </div>
    </Screen>
  );
}
function Load({ v }) {
  const c = v < 0.4 ? 'var(--success)' : v < 0.7 ? 'var(--gold-500)' : 'var(--red-500)';
  return (
    <div>
      <div style={{ height: 5, borderRadius: 3, background: 'var(--char-400)', overflow: 'hidden' }}>
        <div style={{ height: '100%', width: (v * 100) + '%', background: c }}></div>
      </div>
      <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--fg3)', marginTop: 4 }}>{Math.round(v * 100)}% load</div>
    </div>
  );
}

/* ---------- WALLET ---------- */
function WalletScreen({ sats }) {
  const history = [
    ['Tokyo · tyo-07', '−18', '1.0 GB · now'],
    ['Zürich · zrh-11', '−32', '1.9 GB · 2h'],
    ['Channel top-up', '+50,000', 'Lightning · 1d'],
    ['Singapore · sgp-04', '−14', '0.8 GB · 2d'],
  ];
  return (
    <Screen>
      <ScreenTitle title="Wallet" sub="Lightning balance · pay-per-bundle" />
      <div className="grain-over" style={{ marginTop: 16, clipPath: appCC(14), padding: 1, position: 'relative' }}>
        <div style={{ clipPath: appCC(14), padding: '22px 20px', position: 'relative', overflow: 'hidden', background: 'linear-gradient(150deg,#221a10,var(--surface) 60%)' }}>
        <div className="halftone" style={{ position: 'absolute', inset: 0, opacity: .07, '--dot': 'rgba(230,184,0,.9)' }}></div>
        <div style={{ position: 'relative', fontFamily: 'var(--font-mono)', fontSize: 11, letterSpacing: '.16em', textTransform: 'uppercase', color: 'var(--fg3)' }}>Channel balance</div>
        <div style={{ position: 'relative', fontFamily: 'var(--font-mono)', fontSize: 38, color: 'var(--gold-500)', marginTop: 8 }}>{sats.toLocaleString()} <span style={{ fontSize: 16, color: 'var(--fg2)' }}>sats</span></div>
        <div style={{ position: 'relative', display: 'flex', gap: 10, marginTop: 18 }}>
          <AppButton variant="gold" full icon="plus">Top up</AppButton>
          <AppButton variant="ghost" full icon="arrow-up-right">Withdraw</AppButton>
        </div>
        </div>
      </div>
      <div style={{ fontFamily: 'var(--font-ui)', fontWeight: 600, fontSize: 15, color: 'var(--lav-300)', margin: '24px 0 4px' }}>Recent</div>
      {history.map(([t, a, m], i) => {
        const pos = a.startsWith('+');
        return (
          <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '12px 0', borderBottom: '1px solid var(--border)' }}>
            <div style={{ width: 36, height: 36, clipPath: appCC(6), background: 'var(--black-700)', display: 'grid', placeItems: 'center', color: pos ? 'var(--gold-500)' : 'var(--lav-400)' }}>
              <Icon name={pos ? 'plus' : 'zap'} size={16} color={pos ? 'var(--gold-500)' : 'var(--lav-400)'} />
            </div>
            <div style={{ flex: 1 }}>
              <div style={{ fontFamily: 'var(--font-ui)', fontWeight: 500, fontSize: 14, color: 'var(--lav-300)' }}>{t}</div>
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--fg3)' }}>{m}</div>
            </div>
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 14, color: pos ? 'var(--gold-500)' : 'var(--lav-300)' }}>{a}</div>
          </div>
        );
      })}
    </Screen>
  );
}

/* ---------- EARN ---------- */
function EarnScreen({ running, onToggle, earned, served }) {
  return (
    <Screen>
      <ScreenTitle title="Earn" sub="System operator · sell bandwidth for Bitcoin" />
      <div style={{ position: 'relative', display: 'grid', placeItems: 'center', height: 188, marginTop: 8 }}>
        <div style={{ position: 'absolute' }}><Globe size={170} opacity={running ? 0.6 : 0.2} color={running ? 'var(--gold-500)' : 'var(--lav-600)'} spin={running} /></div>
        <div style={{ position: 'relative', textAlign: 'center' }}>
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, letterSpacing: '.16em', textTransform: 'uppercase', color: 'var(--fg3)' }}>Earned · 30d</div>
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 32, color: 'var(--gold-500)', marginTop: 4 }}>{earned.toLocaleString()}</div>
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: 'var(--fg2)' }}>sats · {served} TB routed</div>
        </div>
      </div>
      <div style={{ display: 'flex', gap: 10, marginTop: 8 }}>
        <Readout icon="radio-tower" label="Status" value={running ? 'Live' : 'Idle'} tint={running ? 'var(--cyan-400)' : 'var(--fg3)'} />
        <Readout icon="users" label="Peers" value={running ? '38' : '0'} tint="var(--lav-400)" />
        <Readout icon="gauge" label="Up" value={running ? '94 Mb/s' : '—'} tint="var(--cyan-400)" />
      </div>
      <div style={{ marginTop: 18 }}>
        <AppButton variant={running ? 'red' : 'gold'} full icon={running ? 'square' : 'server'} onClick={onToggle}>
          {running ? 'Stop node' : 'Start node'}
        </AppButton>
      </div>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 7, marginTop: 16, color: 'var(--fg3)' }}>
        <Icon name="git-branch" size={13} color="var(--fg3)" />
        <span style={{ fontFamily: 'var(--font-body)', fontSize: 12 }}>Advertised over Nostr · settled via Fedimint.</span>
      </div>
    </Screen>
  );
}

function ScreenTitle({ title, sub }) {
  return (
    <div style={{ marginTop: 8 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <h2 style={{ fontFamily: 'var(--font-display)', fontWeight: 700, fontSize: 28, color: 'var(--lav-300)', textTransform: 'uppercase', margin: 0, letterSpacing: '.02em' }}>{title}</h2>
        <span className="kana" style={{ fontFamily: 'var(--font-ui)', fontWeight: 600, color: 'var(--blood-red)', fontSize: 12 }}>עֲרָפֶל</span>
      </div>
      <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, letterSpacing: '.08em', color: 'var(--fg3)', marginTop: 4 }}>{sub}</div>
    </div>
  );
}

Object.assign(window, { Globe, NODES, ConnectScreen, NodesScreen, WalletScreen, EarnScreen, ScreenTitle, Readout });
