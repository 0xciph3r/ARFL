/* ARFL Marketing Website — page composition (decentralised privacy protocol) */

const sx = {
  page: { minHeight: '100vh', background: 'var(--bg)' },
  hero: {
    position: 'relative', minHeight: '100vh', overflow: 'hidden',
    display: 'flex', flexDirection: 'column', alignItems: 'flex-start', justifyContent: 'center',
    padding: '120px 8% 80px',
  },
  heroArt: {
    position: 'absolute', inset: 0, zIndex: 0,
    backgroundImage: "url('" + ((window.__resources && window.__resources.heroBg) || '../../assets/imagery/cyber-portrait.png') + "')",
    backgroundSize: 'cover', backgroundPosition: 'center 18%',
    opacity: 0.4, filter: 'saturate(0.9) contrast(1.05) brightness(0.78)',
  },
  heroVignette: {
    position: 'absolute', inset: 0, zIndex: 1,
    background: 'radial-gradient(ellipse 72% 62% at 50% 46%, rgba(13,13,13,0) 0%, rgba(13,13,13,.5) 58%, rgba(13,13,13,.98) 100%)',
  },
  heroInner: { position: 'relative', zIndex: 3, maxWidth: 760, textAlign: 'left' },
};

/* node constellation — decentralised network motif (procedural, deterministic) */
function Globe({ size = 320, opacity = 0.5 }) {
  // fixed node positions in a 200x200 field — scattered, network-like
  const nodes = [
    [100, 30], [56, 52], [148, 48], [30, 96], [172, 90], [100, 78],
    [70, 110], [134, 118], [44, 150], [160, 152], [100, 134], [100, 172],
    [86, 100], [120, 88], [62, 80], [142, 78],
  ];
  // links between nearby nodes (indices)
  const links = [
    [0, 1], [0, 2], [0, 13], [1, 14], [1, 5], [2, 15], [2, 5],
    [3, 6], [3, 1], [4, 7], [4, 15], [5, 12], [5, 13], [6, 12],
    [7, 13], [6, 10], [7, 10], [8, 6], [9, 7], [8, 10], [9, 10],
    [10, 11], [12, 14], [13, 15], [11, 8], [11, 9],
  ];
  const live = new Set([0, 5, 10, 4, 8]); // brighter "online" nodes
  return (
    <svg width={size} height={size} viewBox="0 0 200 200" style={{ position: 'absolute', zIndex: 2, opacity, filter: 'drop-shadow(0 0 10px rgba(0,229,255,.25))' }} aria-hidden>
      <g stroke="var(--cyan-500)" strokeWidth="0.4" opacity="0.55">
        {links.map(([a, b], i) =>
          <line key={i} x1={nodes[a][0]} y1={nodes[a][1]} x2={nodes[b][0]} y2={nodes[b][1]} />)}
      </g>
      {nodes.map(([x, y], i) => {
        const on = live.has(i);
        return <circle key={i} cx={x} cy={y} r={on ? 2.4 : 1.5}
          fill={on ? 'var(--cyan-400)' : 'var(--lav-500)'}
          style={on ? { filter: 'drop-shadow(0 0 4px var(--cyan-500))' } : undefined} />;
      })}
    </svg>
  );
}

function Hero() {
  return (
    <header style={sx.hero}>
      <div className="halftone" style={{ position: 'absolute', inset: 0, zIndex: 0, opacity: 0.05, '--dot': 'rgba(0,229,255,.7)' }}></div>
      <div style={{
        position: 'absolute', top: '50%', left: '50%', width: 'min(820px, 98vw)', aspectRatio: '1408 / 768',
        transform: 'translate(-50%, -50%)', zIndex: 0,
        backgroundImage: `url('${(window.__resources && window.__resources.networkGlobe) || '../../assets/imagery/network-globe.png'}')`,
        backgroundSize: 'contain', backgroundRepeat: 'no-repeat', backgroundPosition: 'center',
        opacity: 1, animation: 'arflRotate 140s linear infinite', transformOrigin: '50% 50%',
      }}></div>
      <div style={{ position: 'absolute', inset: 0, zIndex: 1, background: 'linear-gradient(90deg, rgba(13,13,13,.92) 0%, rgba(13,13,13,.7) 42%, rgba(13,13,13,.15) 68%, rgba(13,13,13,0) 100%)' }}></div>
      <div style={sx.heroVignette}></div>
      <div style={sx.heroInner}>
        <span className="kana" style={{ fontFamily: 'var(--font-ui)', fontWeight: 600, color: 'var(--blood-red)', fontSize: 16, letterSpacing: '.1em', display: 'block', marginBottom: 20 }}>עֲרָפֶל</span>
        <h1 style={{
          fontFamily: 'var(--font-display)', fontWeight: 700, fontSize: 'clamp(58px, 10vw, 130px)',
          lineHeight: 0.9, margin: '0 0 22px', color: 'var(--lav-300)',
          textTransform: 'uppercase', letterSpacing: '.005em',
          textShadow: '0 2px 24px #000',
        }}>Privacy in<br />the Dark Cloud</h1>
        <div style={{ fontFamily: 'var(--font-mono)', fontSize: 13, letterSpacing: '.18em', textTransform: 'uppercase', color: 'var(--lav-400)', marginBottom: 40 }}>Decentralised VPN protocol</div>
        <Button variant="cyan" glow icon="download">Get ARFL</Button>
      </div>
    </header>
  );
}

/* ---- Protocol layers ---- */
const features = [
  { icon: 'shield', tint: 'var(--cyan-500)', kicker: 'TRANSPORT', title: 'WireGuard tunnels', body: 'Every byte leaves your device inside a modern encrypted tunnel. The node that carries your traffic cannot read it — it only forwards.' },
  { icon: 'radio-tower', tint: 'var(--lav-400)', kicker: 'DISCOVERY', title: 'Nostr node mesh', body: 'Nodes announce themselves over Nostr. There is no directory server to block, poison, or subpoena — discovery is gossip, not infrastructure.' },
  { icon: 'zap', tint: 'var(--gold-500)', kicker: 'PAYMENT', title: 'Lightning per bundle', body: 'You pay for bandwidth in tiny Lightning increments as you use it. No plan, no card, no account. System operators earn Bitcoin for the bytes they carry — split 50/50, settled via Fedimint.' },
];

function FeatureCard({ f }) {
  const [hover, setHover] = React.useState(false);
  return (
    <div onMouseEnter={() => setHover(true)} onMouseLeave={() => setHover(false)}
      style={{ flex: 1, clipPath: cc(14), background: hover ? 'var(--border-cyan)' : 'var(--border)', padding: 1,
        transform: hover ? 'translateY(-4px)' : 'none', transition: 'all .25s var(--ease-out)' }}>
      <div style={{ clipPath: cc(14), background: 'var(--surface)', padding: 28, height: '100%' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
          <div style={{ width: 46, height: 46, clipPath: cc(8), display: 'grid', placeItems: 'center',
            background: 'var(--black-700)', color: f.tint }}>
            <Icon name={f.icon} size={22} color={f.tint} />
          </div>
          <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, letterSpacing: '.18em', color: 'var(--fg3)' }}>{f.kicker}</span>
        </div>
        <h3 style={{ fontFamily: 'var(--font-ui)', fontWeight: 600, fontSize: 21, color: 'var(--lav-300)', margin: '20px 0 10px', letterSpacing: '-.01em' }}>{f.title}</h3>
        <p style={{ fontFamily: 'var(--font-body)', fontSize: 15, lineHeight: 1.6, color: 'var(--fg2)', margin: 0 }}>{f.body}</p>
      </div>
    </div>
  );
}

function Features() {
  return (
    <section style={{ position: 'relative', overflow: 'hidden' }}>
      <div style={{ position: 'relative', zIndex: 2, padding: '110px 40px', maxWidth: 1180, margin: '0 auto' }}>
        <Eyebrow>Three layers. One network.</Eyebrow>
        <h2 className="reveal" style={{ fontFamily: 'var(--font-ui)', fontWeight: 700, fontSize: 44, color: 'var(--lav-300)', margin: '16px 0 0', letterSpacing: '-.02em', maxWidth: 620, lineHeight: 1.08 }}>
          Open protocols, assembled into privacy.</h2>
        <div className="reveal-group" style={{ display: 'flex', gap: 22, marginTop: 54 }}>
          {features.map(f => <FeatureCard key={f.title} f={f} />)}
        </div>
      </div>
    </section>
  );
}

/* ---- Manifesto / negation triplet poster ---- */
function Manifesto() {
  return (
    <section className="grain-over" style={{
      position: 'relative', overflow: 'hidden',
      background: 'var(--blood-red)', padding: '120px 24px', textAlign: 'center',
    }}>
      <div className="halftone" style={{ position: 'absolute', inset: 0, opacity: .14, '--dot': 'rgba(13,13,13,.9)' }}></div>
      <div style={{ position: 'relative', zIndex: 2, maxWidth: 940, margin: '0 auto' }}>
        <h2 className="reveal" style={{
          fontFamily: 'var(--font-display)', fontWeight: 700, textTransform: 'uppercase',
          fontSize: 'clamp(40px, 6.5vw, 82px)', lineHeight: 0.96, color: '#0D0D0D', margin: 0,
        }}>No server to seize.<br />No logs to subpoena.<br />No identity required.</h2>
        <p style={{ fontFamily: 'var(--font-mono)', fontSize: 14, lineHeight: 1.7, color: 'rgba(13,13,13,.78)', maxWidth: 520, margin: '28px auto 0', letterSpacing: '.04em' }}>
          THE PROTOCOL DEFINES THE RULES. MATHEMATICS ENFORCES THEM.</p>
      </div>
    </section>
  );
}

/* ---- Cinematic figure band (image used purely as atmosphere) ---- */
function FigureBand() {
  const fig = (window.__resources && window.__resources.satoshi) || '../../assets/mascot/satoshi.png';
  return (
    <section className="grain-over" style={{ position: 'relative', overflow: 'hidden', minHeight: 600, background: 'var(--black-900)', borderTop: '1px solid var(--border)', borderBottom: '1px solid var(--border)', display: 'flex', alignItems: 'center' }}>
      {/* full-bleed figure, right-anchored, bled into black on every edge */}
      <div style={{ position: 'absolute', inset: 0, zIndex: 0 }}>
        <img src={fig} alt="" style={{ position: 'absolute', top: 0, bottom: 0, right: 0, height: '100%', width: '62%', objectFit: 'cover', objectPosition: 'center 14%', opacity: 'var(--portrait-opacity, 0.55)', transition: 'opacity .4s var(--ease-out)' }} />
        <div style={{ position: 'absolute', inset: 0, background: 'linear-gradient(90deg, var(--black-900) 26%, rgba(8,8,8,.4) 52%, rgba(8,8,8,0) 78%)' }}></div>
        <div style={{ position: 'absolute', inset: 0, background: 'linear-gradient(0deg, var(--black-900), rgba(8,8,8,0) 30%, rgba(8,8,8,0) 72%, var(--black-900))' }}></div>
        <div className="halftone" style={{ position: 'absolute', inset: 0, opacity: 0.06, '--dot': 'rgba(179,15,15,.9)' }}></div>
      </div>
      {/* overlaid statement — no name, no label */}
      <div style={{ position: 'relative', zIndex: 2, maxWidth: 1180, width: '100%', margin: '0 auto', padding: '90px 40px' }}>
        <div style={{ maxWidth: 540 }}>
          <Eyebrow>Built for the watched</Eyebrow>
          <h2 style={{ fontFamily: 'var(--font-display)', fontWeight: 700, fontSize: 'clamp(40px, 6vw, 80px)', color: 'var(--lav-300)', textTransform: 'uppercase', margin: '20px 0 0', lineHeight: 0.88, letterSpacing: '.01em' }}>
            Move through<br /><span style={{ color: 'var(--blood-red)' }}>the dark</span><br />unseen</h2>
          <p style={{ fontFamily: 'var(--font-body)', fontSize: 18, lineHeight: 1.6, color: 'var(--fg1)', marginTop: 24, maxWidth: 420 }}>
            No name on file. No trail to follow. ARFL is built for anyone who refuses to be watched — and pays only for the bytes they use.</p>
          <div style={{ marginTop: 30, fontFamily: 'var(--font-mono)', fontSize: 12, letterSpacing: '.16em', color: 'var(--fg3)', textTransform: 'uppercase' }}>
            No account · No logs · No trace
          </div>
        </div>
      </div>
    </section>
  );
}

/* ---- Araphel — the name section ---- */
function NameSection() {
  return (
    <section className="grain-over" style={{ position:'relative', overflow:'hidden', background:'var(--black-900)', padding:'110px 40px', borderTop:'1px solid var(--border)' }}>
      <div style={{ maxWidth:1100, margin:'0 auto', display:'flex', gap:72, alignItems:'flex-start', flexWrap:'wrap' }}>

        {/* Left — the word */}
        <div className="reveal" style={{ flex:'0 0 auto', minWidth:240 }}>
          <div style={{ fontFamily:'serif', fontSize:96, lineHeight:1, color:'var(--lav-300)', direction:'rtl', letterSpacing:'.04em', opacity:.9 }}>עֲרָפֶל</div>
          <div style={{ fontFamily:'var(--font-mono)', fontSize:12, letterSpacing:'.18em', textTransform:'uppercase', color:'var(--cyan-500)', marginTop:16 }}>Araphel · ah-rah-FEL</div>
          <div style={{ width:36, height:1, background:'var(--gold-500)', marginTop:12 }}></div>
          <div style={{ fontFamily:'var(--font-body)', fontStyle:'italic', fontSize:14, color:'var(--fg2)', marginTop:10, lineHeight:1.6 }}>A thick, dark, protective cloud.<br />Ancient Hebrew.</div>
        </div>

        {/* Right — the story */}
        <div className="reveal" style={{ flex:'1 1 400px', maxWidth:520, paddingTop:10 }}>
          <p style={{ fontFamily:'var(--font-body)', fontSize:18, lineHeight:1.7, color:'var(--fg1)', margin:'0 0 18px' }}>
            We've been conditioned to believe that privacy is only for people with something to hide. ARFL completely rejects that.
          </p>
          <p style={{ fontFamily:'var(--font-body)', fontSize:16, lineHeight:1.7, color:'var(--fg2)', margin:0 }}>
            Araphel is the dark cloud that both conceals and protects — not a hiding place for bad actors, but a sanctuary for personal freedom. If someone doesn't have your explicit permission, they are locked out. Not everyone is worthy of access to your data.
          </p>
        </div>
      </div>
    </section>
  );
}

/* ---- Users + Operators combined ---- */
function Audience() {
  return (
    <section style={{ padding: '110px 40px', maxWidth: 1180, margin: '0 auto' }}>
      <div style={{ display: 'flex', gap: 40, flexWrap: 'wrap' }}>
        {/* Users */}
        <div className="reveal" style={{ flex: '1 1 380px', borderTop: '2px solid var(--cyan-500)', paddingTop: 28 }}>
          <Eyebrow color="var(--cyan-500)">For users</Eyebrow>
          <h2 style={{ fontFamily: 'var(--font-ui)', fontWeight: 700, fontSize: 38, color: 'var(--lav-300)', margin: '16px 0 14px', letterSpacing: '-.02em', lineHeight: 1.08 }}>Buy a bundle.<br />Browse.</h2>
          <p style={{ fontFamily: 'var(--font-body)', fontSize: 16, lineHeight: 1.6, color: 'var(--fg2)', maxWidth: 420 }}>Pay-as-you-go privacy. Buy bandwidth in Bitcoin, connect, and browse — no account, no subscription, no identity. Your device holds the only key. When you're done, there's nothing left behind.</p>
          <div style={{ marginTop: 26 }}><Button variant="cyan" glow icon="download">Get ARFL</Button></div>
        </div>
        {/* Operators */}
        <div className="reveal" style={{ flex: '1 1 380px', borderTop: '2px solid var(--gold-500)', paddingTop: 28 }}>
          <Eyebrow color="var(--gold-500)">For system operators</Eyebrow>
          <h2 style={{ fontFamily: 'var(--font-ui)', fontWeight: 700, fontSize: 38, color: 'var(--lav-300)', margin: '16px 0 14px', letterSpacing: '-.02em', lineHeight: 1.08 }}>Serve bandwidth.<br />Earn sats.</h2>
          <p style={{ fontFamily: 'var(--font-body)', fontSize: 16, lineHeight: 1.6, color: 'var(--fg2)', maxWidth: 420 }}>Run the system software, advertise it over Nostr, and route traffic for the network. You're paid in sats for every bundle you carry — settled automatically via Fedimint, with no contract and no counterparty.</p>
          <div style={{ marginTop: 26 }}><Button variant="gold" glow icon="server">Become an operator</Button></div>
        </div>
      </div>
    </section>
  );
}
/* ---- Footer ---- */
function Footer() {
  const cols = {
    Protocol: ['Architecture', 'WireGuard', 'Nostr discovery', 'Lightning'],
    Build: ['Documentation', 'System operators', 'SDK', 'GitHub'],
    More: ['Manifesto', 'Privacy', 'Status', 'Contact'],
  };
  return (
    <footer style={{ padding: '70px 40px 40px', maxWidth: 1180, margin: '0 auto' }}>
      <div style={{ display: 'flex', gap: 60, flexWrap: 'wrap' }}>
        <div style={{ flex: '1 1 280px' }}>
          <div style={{ display:'flex', alignItems:'center', gap:12 }}>
            <div style={{ width:34, height:34, clipPath:'polygon(0% 0%, calc(100% - 8px) 0%, 100% 8px, 100% 100%, 8px 100%, 0% calc(100% - 8px))', background:'var(--lav-500)', padding:1 }}>
              <div style={{ clipPath:'polygon(0% 0%, calc(100% - 8px) 0%, 100% 8px, 100% 100%, 8px 100%, 0% calc(100% - 8px))', background:'var(--pitch-black)', width:'100%', height:'100%', display:'grid', placeItems:'center' }}>

              </div>
            </div>
            <div style={{ fontFamily:'var(--font-wordmark)', fontWeight:700, fontSize:32, letterSpacing:'.05em', color:'var(--lav-300)', textTransform:'uppercase', lineHeight:1 }}>ARFL</div>
          </div>
          <div style={{ display:'flex', alignItems:'center', gap:10, marginTop:12 }}>
            <span style={{ fontFamily:'var(--font-ui)', fontWeight:600, color:'var(--blood-red)', fontSize:12, letterSpacing:'.1em' }}>עֲרָפֶל</span>
            <span style={{ fontFamily:'var(--font-mono)', fontSize:11, letterSpacing:'.18em', textTransform:'uppercase', color:'var(--cyan-500)' }}>Privacy in the Dark Cloud</span>
          </div>
        </div>
        {Object.entries(cols).map(([h, items]) =>
          <div key={h} style={{ minWidth: 140 }}>
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, letterSpacing: '.16em', textTransform: 'uppercase', color: 'var(--fg3)', marginBottom: 16 }}>{h}</div>
            {items.map(i => <div key={i} style={{ fontFamily: 'var(--font-body)', fontSize: 14, color: 'var(--fg2)', marginBottom: 11, cursor: 'pointer' }}>{i}</div>)}
          </div>)}
      </div>
      <div style={{ borderTop: '1px solid var(--border)', marginTop: 44, paddingTop: 24, display: 'flex', justifyContent: 'space-between', flexWrap: 'wrap', gap: 12 }}>
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: 'var(--fg3)' }}>© 2026 ARFL Protocol · No rights reserved</span>
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: 'var(--fg3)' }}>↗WORLDWIDE/WEB−</span>
      </div>
    </footer>
  );
}

/* ---- How it works (users + operators) ---- */
const flows = [
  {
    eyebrow: 'For users', tint: 'var(--cyan-400)', icon: 'shield',
    title: 'Pay-as-you-go privacy',
    steps: [
      ['key', 'Your device generates a key', 'On first launch — that cryptographic key is your only credential. No account, no email, no identity.'],
      ['bitcoin', 'Buy a bundle of bandwidth', 'Pay in Bitcoin for exactly what you want. No subscription, no card on file.'],
      ['globe', 'Connect and browse', 'Traffic routes through the network, encrypted end to end. Nobody can see what you do.'],
      ['rotate-ccw', 'Top up or walk away', 'Run out? Buy more. Done? There is nothing left behind — no logs, no trail, no record.'],
    ],
  },
  {
    eyebrow: 'For system operators', tint: 'var(--gold-500)', icon: 'server',
    title: 'Serve bandwidth, earn sats',
    steps: [
      ['download', 'Run the system software', 'Download it, connect a Fedimint wallet, and you are most of the way there.'],
      ['lock', 'Post a small deposit', 'Fund a refundable security deposit and go online. No contract, no employer.'],
      ['radio-tower', 'The protocol routes traffic', 'Bundles flow through your node automatically — you do not chase work, it comes to you.'],
      ['zap', 'Earn your 50% share', 'Every bundle splits 50/50 between entry and exit node — settled directly via Fedimint. The protocol takes no fee, ever.'],
    ],
  },
];

function FlowCard({ f }) {
  return (
    <div style={{ flex: '1 1 420px', clipPath: cc(14), background: 'var(--border)', padding: 1 }}>
      <div style={{ clipPath: cc(14), background: 'var(--surface)', padding: 30 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
        <div style={{ width: 42, height: 42, clipPath: cc(8), display: 'grid', placeItems: 'center', background: 'var(--black-700)', color: f.tint }}>
          <Icon name={f.icon} size={20} color={f.tint} />
        </div>
        <div>
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, letterSpacing: '.18em', textTransform: 'uppercase', color: f.tint }}>{f.eyebrow}</div>
          <div style={{ fontFamily: 'var(--font-ui)', fontWeight: 700, fontSize: 22, color: 'var(--lav-300)', letterSpacing: '-.01em' }}>{f.title}</div>
        </div>
      </div>
      <div style={{ marginTop: 22, display: 'flex', flexDirection: 'column', gap: 2 }}>
        {f.steps.map(([ic, t, b], i) =>
          <div key={i} style={{ display: 'flex', gap: 14, padding: '14px 0', borderTop: i ? '1px solid var(--border)' : 'none' }}>
            <div style={{ flexShrink: 0, width: 26, height: 26, borderRadius: 7, display: 'grid', placeItems: 'center', background: 'var(--black-800)', border: '1px solid var(--border)', fontFamily: 'var(--font-mono)', fontSize: 12, color: f.tint }}>{i + 1}</div>
            <div>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <Icon name={ic} size={15} color="var(--fg2)" />
                <span style={{ fontFamily: 'var(--font-ui)', fontWeight: 600, fontSize: 15, color: 'var(--lav-300)' }}>{t}</span>
              </div>
              <p style={{ fontFamily: 'var(--font-body)', fontSize: 13.5, lineHeight: 1.55, color: 'var(--fg2)', margin: '5px 0 0' }}>{b}</p>
            </div>
          </div>)}
      </div>
      </div>
    </div>
  );
}

function HowItWorks() {
  return (
    <section style={{ position: 'relative', padding: '110px 40px', maxWidth: 1180, margin: '0 auto' }}>
      <div className="reveal"><Eyebrow>How it works</Eyebrow></div>
      <h2 style={{ fontFamily: 'var(--font-ui)', fontWeight: 700, fontSize: 44, color: 'var(--lav-300)', margin: '16px 0 0', letterSpacing: '-.02em', maxWidth: 640, lineHeight: 1.08 }}>
        Two sides of one network.</h2>
      <p style={{ fontFamily: 'var(--font-body)', fontSize: 17, lineHeight: 1.6, color: 'var(--fg2)', marginTop: 14, maxWidth: 560 }}>
        Users spend Bitcoin for bandwidth. System operators earn Bitcoin for serving it — split 50/50 between entry and exit node, settled via Fedimint. Nobody has to trust anybody.</p>
      <div style={{ display: 'flex', gap: 22, marginTop: 46, flexWrap: 'wrap' }}>
        {flows.map(f => <FlowCard key={f.eyebrow} f={f} />)}
      </div>
    </section>
  );
}

/* ---- Launch a Hub (for builders) ---- */
function HubStage({ icon, label, sub, tint, highlight }) {
  return (
    <div style={{
      flex: '1 1 150px', minWidth: 150, textAlign: 'center', padding: '22px 16px',
      clipPath: cc(12),
      background: highlight ? 'var(--black-700)' : 'transparent',
      border: `1px solid ${highlight ? 'var(--border-cyan)' : 'var(--border)'}`,
      boxShadow: highlight ? '0 0 28px rgba(0,229,255,.14)' : 'none',
    }}>
      <div style={{ width: 48, height: 48, margin: '0 auto', clipPath: cc(8), display: 'grid', placeItems: 'center', background: 'var(--black-800)', color: tint }}>
        <Icon name={icon} size={24} color={tint} />
      </div>
      <div style={{ fontFamily: 'var(--font-ui)', fontWeight: 600, fontSize: 16, color: 'var(--lav-300)', marginTop: 14 }}>{label}{highlight && <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--cyan-400)', marginLeft: 6, letterSpacing: '.1em' }}>YOU</span>}</div>
      <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--fg3)', marginTop: 5 }}>{sub}</div>
    </div>
  );
}
function HubArrow({ label }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 4, padding: '0 4px', minWidth: 64 }}>
      <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, letterSpacing: '.14em', textTransform: 'uppercase', color: 'var(--gold-500)' }}>{label}</span>
      <Icon name="arrow-right" size={22} color="var(--char-300)" />
    </div>
  );
}

const hubPoints = [
  { icon: 'sparkles', title: 'Compete on experience', body: 'Hubs compete on brand, reliability, and UX — never on infrastructure. The node network is shared by every hub.' },
  { icon: 'scale', title: 'No protocol fee, ever', body: 'Every bundle splits 50/50 between the entry and exit node. The protocol keeps nothing — your margin is whatever you charge above that.' },
];

function HubLaunch() {
  return (
    <section className="grain-over" style={{ position: 'relative', overflow: 'hidden', padding: '110px 40px', background: 'var(--midnight-charcoal)', borderTop: '1px solid var(--border)', borderBottom: '1px solid var(--border)' }}>
      <div style={{ position: 'relative', zIndex: 2, maxWidth: 1100, margin: '0 auto' }}>
        <div style={{ maxWidth: 640 }}>
          <Eyebrow color="var(--gold-500)">For builders</Eyebrow>
          <h2 className="reveal" style={{ fontFamily: 'var(--font-display)', fontWeight: 700, fontSize: 'clamp(38px, 6vw, 68px)', color: 'var(--lav-300)', textTransform: 'uppercase', margin: '16px 0 0', lineHeight: 0.94, letterSpacing: '.01em' }}>Launch a Hub</h2>
          <p style={{ fontFamily: 'var(--font-body)', fontSize: 17, lineHeight: 1.6, color: 'var(--fg2)', marginTop: 18 }}>
            A hub is a product built on ARFL — the payment gateway between users and the node network. Take Bitcoin from users, issue signed bandwidth receipts, and settle earnings to the system operators who carried the traffic. Any developer can run one.</p>
        </div>

        {/* value-flow diagram */}
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', flexWrap: 'wrap', gap: 6, marginTop: 48, padding: '28px 18px', clipPath: cc(14), background: 'var(--black-800)', border: '1px solid var(--border)' }}>
          <HubStage icon="users" label="Users" sub="pay in Bitcoin" tint="var(--lav-400)" />
          <HubArrow label="BTC" />
          <HubStage icon="layout-grid" label="Your hub" sub="issues signed receipts" tint="var(--cyan-400)" highlight />
          <HubArrow label="settle" />
          <HubStage icon="globe" label="Node network" sub="earns for bandwidth" tint="var(--gold-500)" />
        </div>
        {/* the split — 40/40/20 */}
        <div style={{ marginTop: 30 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 10 }}>
            <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, letterSpacing: '.16em', textTransform: 'uppercase', color: 'var(--fg3)' }}>Every bundle, settled automatically via Fedimint · in sats</span>
          </div>
          <div style={{ display: 'flex', height: 46, clipPath: cc(6), overflow: 'hidden', border: '1px solid var(--border)' }}>
            {[['Entry node', '50%', 'var(--cyan-500)', '#0D0D0D', 0.5], ['Exit node', '50%', 'var(--lav-500)', '#0D0D0D', 0.5]].map(([l, p, bg, fg, fl], i) =>
              <div key={l} style={{ flex: fl, background: bg, color: fg, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 8, borderLeft: i ? '1px solid rgba(13,13,13,.4)' : 'none' }}>
                <span style={{ fontFamily: 'var(--font-mono)', fontWeight: 700, fontSize: 14 }}>{p}</span>
                <span style={{ fontFamily: 'var(--font-ui)', fontWeight: 600, fontSize: 13 }}>{l}</span>
              </div>)}
          </div>
          <div style={{ display: 'flex', justifyContent: 'space-between', gap: 26, flexWrap: 'wrap', marginTop: 12 }}>
            <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--fg3)', letterSpacing: '.06em' }}><span style={{ color: 'var(--lav-400)' }}>ZERO PROTOCOL FEE</span> · 50 / 50 split between entry + exit, always</span>
            <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--fg3)', letterSpacing: '.06em' }}><span style={{ color: 'var(--gold-500)' }}>YOUR MARGIN</span> · whatever the hub charges above what it pays operators</span>
          </div>
        </div>

        {/* three points */}
        <div className="reveal-group" style={{ display: 'flex', gap: 22, marginTop: 54, flexWrap: 'wrap' }}>
          {hubPoints.map(p =>
            <div key={p.title} style={{ flex: '1 1 260px' }}>
              <Icon name={p.icon} size={22} color="var(--cyan-400)" />
              <h3 style={{ fontFamily: 'var(--font-ui)', fontWeight: 600, fontSize: 18, color: 'var(--lav-300)', margin: '14px 0 8px', letterSpacing: '-.01em' }}>{p.title}</h3>
              <p style={{ fontFamily: 'var(--font-body)', fontSize: 14, lineHeight: 1.6, color: 'var(--fg2)', margin: 0 }}>{p.body}</p>
            </div>)}
        </div>

        {/* manifesto line + CTA */}
        <div style={{ marginTop: 60, paddingTop: 44, borderTop: '1px solid var(--border)', display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', gap: 28, flexWrap: 'wrap' }}>
          <div style={{ fontFamily: 'var(--font-display)', fontWeight: 700, fontSize: 'clamp(26px, 3.4vw, 40px)', color: 'var(--lav-300)', textTransform: 'uppercase', lineHeight: 0.98, letterSpacing: '.01em', maxWidth: 560 }}>
            The hub owns nothing.<br /><span style={{ color: 'var(--cyan-400)' }}>It builds on top of everything.</span></div>
          <div style={{ display: 'flex', gap: 12 }}>
            <Button variant="gold" glow icon="arrow-up-right">Launch a Hub</Button>
            <Button variant="ghost" icon="file-code">Read the hub spec</Button>
          </div>
        </div>
      </div>
    </section>
  );
}

const TWEAK_DEFAULTS = /*EDITMODE-BEGIN*/{
  "signal": "Cyan",
  "atmosphere": 50,
  "texture": "Default"
}/*EDITMODE-END*/;

function App() {
  useLucide();
  useReveal();
  useNavScroll();
  const [t, setTweak] = useTweaks(TWEAK_DEFAULTS);

  /* Signal colour — remaps --cyan-* vars so every component responds */
  React.useEffect(() => {
    const map = {
      'Cyan':     ['#00E5FF','#5CF0FF','#00B8CC','rgba(0,229,255,.4)','0 0 0 1px rgba(0,229,255,.5),0 0 24px rgba(0,229,255,.35)'],
      'Gold':     ['#E6B800','#F5CF2E','#C79E00','rgba(230,184,0,.4)','0 0 0 1px rgba(230,184,0,.5),0 0 24px rgba(230,184,0,.3)'],
      'Lavender': ['#C0A6CF','#E2D5EA','#9D7BB0','rgba(157,123,176,.3)','0 0 0 1px rgba(157,123,176,.35),0 0 18px rgba(157,123,176,.2)'],
    }[t.signal];
    const r = document.documentElement.style;
    r.setProperty('--cyan-500',  map[0]);
    r.setProperty('--cyan-400',  map[1]);
    r.setProperty('--cyan-600',  map[2]);
    r.setProperty('--border-cyan', map[3]);
    r.setProperty('--glow-cyan', map[4]);
    r.setProperty('--neon-cyan', map[0]);
  }, [t.signal]);

  /* Atmosphere — globe glow + portrait intensity */
  React.useEffect(() => {
    const a = t.atmosphere / 100;
    document.documentElement.style.setProperty('--globe-opacity',    (0.1 + a * 0.75).toFixed(2));
    document.documentElement.style.setProperty('--portrait-opacity', (a * 0.6).toFixed(2));
  }, [t.atmosphere]);

  /* Texture — grain overlay opacity */
  React.useEffect(() => {
    const el = document.querySelector('.arfl-grain-fixed');
    if (el) el.style.opacity = ({ 'Off': '0', 'Default': '0.28', 'Heavy': '0.65' })[t.texture];
  }, [t.texture]);

  return (
    <div style={sx.page}>
      <Nav />
      <Hero />
      <Features />
      <NameSection />
      <FigureBand />
      <Audience />
      <HubLaunch />
      <Footer />
      <TweaksPanel title="Tweaks">
        <TweakSection label="Signal colour" />
        <TweakRadio label="Leads" value={t.signal}
          options={['Cyan','Gold','Lavender']}
          onChange={v => setTweak('signal', v)} />
        <TweakSection label="Atmosphere" />
        <TweakSlider label="Intensity" value={t.atmosphere} min={0} max={100}
          onChange={v => setTweak('atmosphere', v)} />
        <TweakSection label="Texture" />
        <TweakRadio label="Grain" value={t.texture}
          options={['Off','Default','Heavy']}
          onChange={v => setTweak('texture', v)} />
      </TweaksPanel>
    </div>
  );
}

ReactDOM.createRoot(document.getElementById('root')).render(<App />);
