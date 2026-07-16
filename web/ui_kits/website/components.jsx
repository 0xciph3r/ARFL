/* ARFL Marketing Website — shared components
   Loaded before app.jsx. Exports to window at the end. */

/* ── Shape language B: top-right + bottom-left chamfer ── */
const cc = (c=16) => `polygon(0% 0%, calc(100% - ${c}px) 0%, 100% ${c}px, 100% 100%, ${c}px 100%, 0% calc(100% - ${c}px))`;

/* Chamfer wrapper — handles border via outer/inner double-div pattern */
function Chamfer({ cut=16, bg='var(--surface)', border='var(--border)', padding=18, style={}, children, onClick, className }) {
  return (
    <div onClick={onClick} className={className}
      style={{ clipPath: cc(cut), background: border, padding: 1 }}>
      <div style={{ clipPath: cc(cut), background: bg, padding, ...style }}>{children}</div>
    </div>
  );
}

const Icon = ({ name, size = 20, color = 'currentColor', stroke = 1.75, style }) =>
  <i data-lucide={name} style={{ width: size, height: size, color, strokeWidth: stroke, ...style }}></i>;

/* ── useReveal hook — adds .visible to .reveal / .reveal-group when in view ── */
function useReveal() {
  React.useEffect(() => {
    const els = document.querySelectorAll('.reveal, .reveal-group');
    const io = new IntersectionObserver((entries) => {
      entries.forEach(e => { if (e.isIntersecting) { e.target.classList.add('visible'); io.unobserve(e.target); } });
    }, { threshold: 0.12 });
    els.forEach(el => io.observe(el));
    return () => io.disconnect();
  });
}

/* ── Nav scroll opacity ── */
function useNavScroll() {
  React.useEffect(() => {
    const nav = document.querySelector('nav[style]');
    const onScroll = () => { if (nav) nav.classList.toggle('nav-scrolled', window.scrollY > 40); };
    window.addEventListener('scroll', onScroll, { passive: true });
    return () => window.removeEventListener('scroll', onScroll);
  }, []);
}

/* re-run lucide after every render so dynamically added <i> become svg */
function useLucide(dep) {
  React.useEffect(() => {
    if (window.lucide) window.lucide.createIcons({ attrs: { 'stroke-width': 1.75 } });
  });
}

const navStyles = {
  bar: {
    position: 'fixed', top: 0, left: 0, right: 0, zIndex: 100,
    display: 'flex', alignItems: 'center', justifyContent: 'space-between',
    padding: '16px 40px',
    background: 'rgba(13,13,13,0.55)', backdropFilter: 'blur(14px)',
    borderBottom: '1px solid var(--border)',
  },
  brand: { display: 'flex', alignItems: 'center', gap: 12 },
  word: { fontFamily: 'var(--font-wordmark)', fontWeight: 700, fontSize: 24, letterSpacing: '.05em', color: 'var(--lav-300)', textTransform: 'uppercase', lineHeight: 1 },
  links: { display: 'flex', gap: 30, alignItems: 'center' },
  link: { fontFamily: 'var(--font-ui)', fontWeight: 500, fontSize: 14, color: 'var(--fg2)', letterSpacing: '-.01em', cursor: 'pointer', transition: 'color .2s' },
};

function Nav() {
  const items = ['Protocol', 'Network', 'Docs', 'Manifesto'];
  return (
    <nav style={navStyles.bar}>
      <div style={navStyles.brand}>
        {/* A2 chamfer mark — lavender border */}
        <div style={{ width:30, height:30, clipPath:'polygon(0% 0%, calc(100% - 7px) 0%, 100% 7px, 100% 100%, 7px 100%, 0% calc(100% - 7px))', background:'var(--lav-500)', padding:1, flexShrink:0 }}>
          <div style={{ clipPath:'polygon(0% 0%, calc(100% - 7px) 0%, 100% 7px, 100% 100%, 7px 100%, 0% calc(100% - 7px))', background:'var(--pitch-black)', width:'100%', height:'100%' }}></div>
        </div>
        <span style={navStyles.word}>ARFL</span>
        <span className="kana" style={{ fontFamily: 'var(--font-ui)', fontWeight: 600, color: 'var(--blood-red)', fontSize: 11, letterSpacing: '.08em' }}>עֲרָפֶל</span>
      </div>
      <div style={navStyles.links}>
        {items.map(i => <span key={i} style={navStyles.link}
          onMouseEnter={e => e.currentTarget.style.color = 'var(--lav-300)'}
          onMouseLeave={e => e.currentTarget.style.color = 'var(--fg2)'}>{i}</span>)}
        <Button small variant="cyan">Get ARFL</Button>
      </div>
    </nav>
  );
}

/* ---- Button ---- */
function Button({ children, variant = 'gold', small, glow, onClick, icon }) {
  const [hover, setHover] = React.useState(false);
  const [down, setDown] = React.useState(false);
  const palette = {
    gold: ['var(--gold-500)', 'var(--gold-400)', 'var(--gold-600)', 'var(--fg-on-color)'],
    cyan: ['var(--cyan-500)', 'var(--cyan-400)', 'var(--cyan-600)', 'var(--fg-on-color)'],
    red:  ['var(--red-600)', 'var(--red-500)', 'var(--red-700)', 'var(--fg-on-red)'],
    ghost:['var(--char-400)', 'var(--char-300)', 'var(--char-500)', 'var(--lav-300)'],
  }[variant];
  const cut = small ? 8 : 14;
  const bg = down ? palette[2] : hover ? palette[1] : palette[0];
  return (
    <button onClick={onClick}
      onMouseEnter={() => setHover(true)} onMouseLeave={() => { setHover(false); setDown(false); }}
      onMouseDown={() => setDown(true)} onMouseUp={() => setDown(false)}
      style={{
        display: 'inline-flex', alignItems: 'center', gap: 8,
        fontFamily: 'var(--font-ui)', fontWeight: 600, fontSize: small ? 13 : 15,
        letterSpacing: '-.01em', cursor: 'pointer',
        padding: small ? '8px 16px' : '13px 24px',
        clipPath: cc(cut),
        background: bg, color: palette[3], border: 'none',
        boxShadow: (glow && hover) ? 'var(--glow-gold)' : 'none',
        transform: down ? 'scale(0.97)' : 'scale(1)',
        transition: 'all .18s var(--ease-out)',
      }}>
      {children}{icon && <Icon name={icon} size={small ? 15 : 17} />}
    </button>
  );
}

/* ---- Eyebrow ---- */
const Eyebrow = ({ children, color = 'var(--cyan-500)' }) =>
  <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12, letterSpacing: '.2em', textTransform: 'uppercase', color }}>{children}</div>;

Object.assign(window, { Icon, useLucide, useReveal, useNavScroll, Nav, Button, Eyebrow, Chamfer, cc });
