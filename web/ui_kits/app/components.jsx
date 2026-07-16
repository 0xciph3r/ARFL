/* ARFL Private-Connection Client — frame + primitives. Loaded first. */

/* ── Shape language B: top-right + bottom-left chamfer ── */
const appCC = (c=16) => `polygon(0% 0%, calc(100% - ${c}px) 0%, 100% ${c}px, 100% 100%, ${c}px 100%, 0% calc(100% - ${c}px))`;

function AppChamfer({ cut=14, bg='var(--surface)', border='var(--border)', padding='15px 22px', style={}, children, onClick, disabled }) {
  return (
    <div onClick={!disabled ? onClick : undefined}
      style={{ clipPath: appCC(cut), background: disabled ? 'var(--char-300)' : border, padding: 1, opacity: disabled ? 0.45 : 1, cursor: disabled ? 'default' : 'pointer' }}>
      <div style={{ clipPath: appCC(cut), background: bg, padding, ...style }}>{children}</div>
    </div>
  );
}

const Icon = ({ name, size = 22, color = 'currentColor', style }) =>
  <i data-lucide={name} style={{ width: size, height: size, color, ...style }}></i>;

/* ── Brand mark: reticle/viewfinder corner brackets around the wordmark. ── */
function BrandFrame({ children, padding = 6, arm = 8, stroke = 1.5, color = 'var(--cyan-500)', style = {} }) {
  const base = { position: 'absolute', width: arm, height: arm };
  return (
    <div style={{ position: 'relative', display: 'inline-flex', padding, ...style }}>
      <span style={{ ...base, top: 0, left: 0, borderTop: `${stroke}px solid ${color}`, borderLeft: `${stroke}px solid ${color}` }} />
      <span style={{ ...base, top: 0, right: 0, borderTop: `${stroke}px solid ${color}`, borderRight: `${stroke}px solid ${color}` }} />
      <span style={{ ...base, bottom: 0, left: 0, borderBottom: `${stroke}px solid ${color}`, borderLeft: `${stroke}px solid ${color}` }} />
      <span style={{ ...base, bottom: 0, right: 0, borderBottom: `${stroke}px solid ${color}`, borderRight: `${stroke}px solid ${color}` }} />
      {children}
    </div>
  );
}

function useLucide() {
  React.useEffect(() => { if (window.lucide) window.lucide.createIcons({ attrs: { 'stroke-width': 1.75 } }); });
}

/* ---- Phone frame ---- */
const W = 390, H = 844;
function PhoneFrame({ children }) {
  return (
    <div style={{
      width: W, height: H, borderRadius: 46, background: '#000',
      border: '1px solid #2a2535', padding: 11, position: 'relative',
      boxShadow: '0 40px 120px rgba(0,0,0,.8), 0 0 0 1px rgba(157,123,176,.08), var(--glow-cyan)',
      flexShrink: 0,
    }}>
      <div className="grain-over" style={{
        width: '100%', height: '100%', borderRadius: 36, overflow: 'hidden',
        background: 'var(--bg)', position: 'relative',
      }}>
        {/* status bar */}
        <div style={{
          position: 'absolute', top: 0, left: 0, right: 0, height: 46, zIndex: 40,
          display: 'flex', alignItems: 'center', justifyContent: 'space-between',
          padding: '0 26px', fontFamily: 'var(--font-mono)', fontSize: 13, color: 'var(--fg1)',
        }}>
          <span>9:41</span>
          <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
            <Icon name="signal" size={14} /><Icon name="wifi" size={14} /><Icon name="battery-full" size={16} />
          </div>
        </div>
        {/* notch */}
        <div style={{ position: 'absolute', top: 10, left: '50%', transform: 'translateX(-50%)', width: 118, height: 26, background: '#000', borderRadius: 14, zIndex: 41 }}></div>
        {children}
      </div>
    </div>
  );
}

/* ---- Tab bar ---- */
function TabBar({ active, onChange }) {
  const tabs = [['connect', 'power', 'Connect'], ['nodes', 'globe', 'Nodes'], ['wallet', 'zap', 'Wallet'], ['earn', 'server', 'Earn']];
  return (
    <div style={{
      position: 'absolute', bottom: 0, left: 0, right: 0, height: 88, zIndex: 45,
      background: 'rgba(13,13,13,0.72)', backdropFilter: 'blur(18px)', borderTop: '1px solid var(--border)',
      display: 'flex', alignItems: 'flex-start', justifyContent: 'space-around', padding: '14px 10px 0',
    }}>
      {tabs.map(([id, ic, lbl]) => <Tab key={id} {...{ id, ic, lbl, active, onChange }} />)}
    </div>
  );
}
function Tab({ id, ic, lbl, active, onChange }) {
  const on = active === id;
  return (
    <button onClick={() => onChange(id)} style={{
      background: 'none', border: 'none', cursor: 'pointer', display: 'flex', flexDirection: 'column',
      alignItems: 'center', gap: 5, width: 64, color: on ? 'var(--cyan-400)' : 'var(--fg3)',
    }}>
      <Icon name={ic} size={22} color={on ? 'var(--cyan-400)' : 'var(--fg3)'} />
      <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, letterSpacing: '.04em' }}>{lbl}</span>
    </button>
  );
}

/* ---- shared button ---- */
function AppButton({ children, variant = 'gold', full, onClick, icon, disabled }) {
  const [down, setDown] = React.useState(false);
  const pal = {
    gold: ['var(--gold-500)', 'var(--gold-600)', '#0D0D0D'],
    cyan: ['var(--cyan-500)', 'var(--cyan-600)', '#0D0D0D'],
    red:  ['var(--red-600)', 'var(--red-700)', '#f7e3e3'],
    ghost:['var(--char-400)', 'var(--char-500)', 'var(--lav-300)'],
  }[variant];
  return (
    <button disabled={disabled} onClick={onClick}
      onMouseDown={() => setDown(true)} onMouseUp={() => setDown(false)} onMouseLeave={() => setDown(false)}
      style={{
        width: full ? '100%' : 'auto', display: 'inline-flex', alignItems: 'center', justifyContent: 'center', gap: 9,
        fontFamily: 'var(--font-ui)', fontWeight: 600, fontSize: 16, cursor: disabled ? 'default' : 'pointer',
        padding: '15px 22px', clipPath: appCC(14), color: pal[2], border: 'none',
        background: down ? pal[1] : pal[0],
        opacity: disabled ? 0.45 : 1, transform: down ? 'scale(0.98)' : 'scale(1)',
        transition: 'all .15s var(--ease-out)',
      }}>
      {icon && <Icon name={icon} size={19} color={pal[2]} />}{children}
    </button>
  );
}

/* ---- screen scaffold (scrolls under fixed chrome) ---- */
function Screen({ children, pad = true, scroll = true }) {
  return <div style={{
    position: 'absolute', inset: 0, paddingTop: 46, overflowY: scroll ? 'auto' : 'hidden',
    overflowX: 'hidden',
  }}>
    <div style={{ padding: pad ? '8px 22px 110px' : 0 }}>{children}</div>
  </div>;
}

Object.assign(window, { Icon, useLucide, PhoneFrame, TabBar, AppButton, AppChamfer, BrandFrame, appCC, Screen, W, H });
