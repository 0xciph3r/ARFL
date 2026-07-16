/* ARFL Private-Connection Client — orchestration */

function App() {
  useLucide();
  const [tab, setTab] = React.useState('connect');
  const [status, setStatus] = React.useState('disconnected'); // disconnected | connecting | connected
  const [node, setNode] = React.useState(NODES[0]);
  const [sess, setSess] = React.useState({ up: 0, down: 0, sats: 0 });
  const [walletSats, setWalletSats] = React.useState(50000);
  const [nodeRunning, setNodeRunning] = React.useState(false);
  const [earned, setEarned] = React.useState(184210);

  // connect lifecycle
  const toggleConnect = () => {
    if (status === 'disconnected') {
      setStatus('connecting');
      setSess({ up: 0, down: 0, sats: 0 });
      setTimeout(() => setStatus('connected'), 1600);
    } else {
      setStatus('disconnected');
    }
  };

  // live throughput + per-bundle Lightning spend while connected
  React.useEffect(() => {
    if (status !== 'connected') return;
    const t = setInterval(() => {
      setSess(s => {
        const dDown = Math.random() * 2.4 + 0.6;
        const dUp = Math.random() * 0.8 + 0.2;
        const newDown = s.down + dDown;
        // bill 1 sat per ~0.06 GB-ish cadence (simplified visual)
        const newSats = Math.floor((newDown + s.up + dUp) / 40 * node.sats) || s.sats;
        return { down: newDown, up: s.up + dUp, sats: Math.max(s.sats, newSats) };
      });
    }, 700);
    return () => clearInterval(t);
  }, [status, node]);

  // deduct spend from wallet as it accrues
  React.useEffect(() => {
    if (status === 'connected') setWalletSats(w => Math.max(0, 50000 - sess.sats));
  }, [sess.sats, status]);

  // system operator earnings tick
  React.useEffect(() => {
    if (!nodeRunning) return;
    const t = setInterval(() => setEarned(e => e + Math.floor(Math.random() * 40 + 10)), 900);
    return () => clearInterval(t);
  }, [nodeRunning]);

  return (
    <PhoneFrame>
      <div style={{ position: 'absolute', inset: 0 }} key={tab}>
        <div style={{ position: 'absolute', inset: 0 }}>
          {tab === 'connect' && <ConnectScreen status={status} node={node}
            sessUp={sess.up} sessDown={sess.down} sessSats={sess.sats}
            onToggle={toggleConnect} onPickNode={() => setTab('nodes')} />}
          {tab === 'nodes' && <NodesScreen node={node} onSelect={(n) => { setNode(n); setTab('connect'); }} />}
          {tab === 'wallet' && <WalletScreen sats={walletSats} />}
          {tab === 'earn' && <EarnScreen running={nodeRunning} onToggle={() => setNodeRunning(r => !r)} earned={earned} served={nodeRunning ? '1.4' : '1.2'} />}
        </div>
      </div>
      <TabBar active={tab} onChange={setTab} />
    </PhoneFrame>
  );
}

ReactDOM.createRoot(document.getElementById('root')).render(<App />);
