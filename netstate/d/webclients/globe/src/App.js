import React, { useEffect, useMemo, useRef, useState } from 'react';
import Globe from 'react-globe.gl';

import GLOBE_IMG from './earth-night.jpg';
import BUMP_IMG from './earth-topology.png';
import BG_IMG from './night-sky.png';

const NETSTATE_URL = process.env.REACT_APP_NETSTATE_URL || 'https://netstated-d7bbec1ed55b.herokuapp.com/data';

export default function App() {
  const globeRef = useRef();
  const [nodes, setNodes] = useState([]);
  const [lastFetchAt, setLastFetchAt] = useState(null);
  const [hoverArc, setHoverArc] = useState(null);
  const [hoverPointId, setHoverPointId] = useState(null);

  // fetch /data every 30s
  useEffect(() => {
    let timer;
    const fetchData = async () => {
      try {
        const res = await fetch(NETSTATE_URL, { cache: 'no-store' });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const data = await res.json();
        setNodes(Array.isArray(data) ? data : []);
        setLastFetchAt(new Date());
      } catch (e) {
        console.error('Failed to load /data', e);
      } finally {
        timer = setTimeout(fetchData, 30000);
      }
    };
    fetchData();
    return () => clearTimeout(timer);
  }, []);

  // start on continental US
  useEffect(() => {
    if (!globeRef.current) return;
    const pov = { lat: 39, lng: -98, altitude: 2.5 };
    const t = setTimeout(() => globeRef.current.pointOfView(pov, 1500), 800);
    return () => clearTimeout(t);
  }, []);

  // map /data → pointsData + arcsData
  const { pointsData, arcsData } = useMemo(() => {
    const now = Date.now();
    const TTL_MS = 5 * 60 * 1000;

    const pts = nodes.map((n, i) => ({
      id: i,
      t: n.t,                // 0 uncensored, 1 censored
      lat: n.lat,
      lng: n.lon,
      lastSeen: n.lastSeen ? new Date(n.lastSeen).getTime() : now,
      label: `${n.t === 0 ? 'Uncensored' : 'Censored'} ${n.lat?.toFixed?.(2) ?? n.lat}, ${n.lon?.toFixed?.(2) ?? n.lon}`
    }));

    const arcs = [];
    pts.forEach((p, idx) => {
      const edges = nodes[idx]?.edges || [];
      edges.forEach(j => {
        const q = pts[j];
        if (!q) return;
        if (p.t === 0 && q.t === 1) {
          const recency = Math.max(0, 1 - (now - q.lastSeen) / TTL_MS);
          arcs.push({
            startId: idx,
            endId: j,
            startLat: p.lat, startLng: p.lng,
            endLat: q.lat,   endLng: q.lng,
            recency,
            label: `${p.label} → ${q.label}`
          });
        }
      });
    });

    return { pointsData: pts, arcsData: arcs };
  }, [nodes]);

  // styling
  const pointColor    = p => (p.t === 0 ? 'rgba(0,255,0,0.8)' : 'rgba(255,60,60,0.9)');
  const pointAltitude = p => (p.t === 0 ? 0.12 : 0.08);
  const pointRadius   = p => (p.t === 0 ? 0.45 : 0.35);
  const arcColor = a => {
    const isHoverArc = hoverArc === a;
    const isRelated = hoverPointId != null && (a.startId === hoverPointId || a.endId === hoverPointId);
    const baseA = Math.max(0.35, a.recency);
    const alpha = isHoverArc ? 1 : (isRelated ? Math.max(0.7, baseA) : baseA);
    return [
      `rgba(255, 200, 40, ${alpha})`,
      `rgba(255, 80, 0, ${alpha})`
    ];
  };
  const arcAltitude = () => 0.15;

  return (
    <div style={{ position: 'fixed', inset: 0, background: '#05070d' }}>
      <Globe
        ref={globeRef}
        globeImageUrl={GLOBE_IMG}
        bumpImageUrl={BUMP_IMG}
        backgroundImageUrl={BG_IMG}
        showAtmosphere
        atmosphereAltitude={0.25}
        atmosphereColor="lightskyblue"

        // points
        pointsData={pointsData}
        pointLat="lat"
        pointLng="lng"
        pointColor={pointColor}
        pointAltitude={pointAltitude}
        pointRadius={pointRadius}
        pointLabel="label"

        // arcs
        arcsData={arcsData}
        arcStartLat="startLat"
        arcStartLng="startLng"
        arcEndLat="endLat"
        arcEndLng="endLng"
        arcColor={arcColor}
        arcAltitude={arcAltitude}
        arcStroke={a => (hoverArc === a ? 1.6 : (hoverPointId != null && (a.startId === hoverPointId || a.endId === hoverPointId) ? 1.1 : 0.6))}
        arcDashLength={0.4}
        arcDashGap={0.8}
        arcDashInitialGap={() => Math.random()}
        arcDashAnimateTime={a => (hoverArc === a || (hoverPointId != null && (a.startId === hoverPointId || a.endId === hoverPointId)) ? 2500 : 4000)}
        arcsTransitionDuration={0}
        arcLabel="label"
        onArcHover={setHoverArc}

        // persistent labels on globe
        labelsData={pointsData}
        labelLat="lat"
        labelLng="lng"
        labelText={d => `${d.t === 0 ? 'U' : 'C'}${d.id}`}
        labelColor={d => d.t === 0 ? 'rgba(180,255,180,0.9)' : 'rgba(255,180,180,0.95)'}
        labelAltitude={d => pointAltitude(d) + 0.02}
        labelSize={1.0}
        labelDotRadius={0.1}

        // hover a point to highlight its routes
        onPointHover={p => setHoverPointId(p?.id ?? null)}

        waitForGlobeReady
        enablePointerInteraction
      />

      <div style={{ position: 'absolute', left: 16, bottom: 16, color: '#9fb0d8', fontFamily: 'system-ui, -apple-system, Segoe UI, Roboto, Helvetica, Arial', fontSize: 12 }}>
        <div><b>netstated</b> — censored:uncensored view · {lastFetchAt ? lastFetchAt.toLocaleTimeString() : '…'}</div>
        <div style={{ marginTop: 6, display: 'flex', gap: 12, alignItems: 'center' }}>
          <LegendSwatch color="rgba(0,255,0,0.9)" label="uncensored (volunteer)" />
          <LegendSwatch color="rgba(255,60,60,0.95)" label="censored (client)" />
          <span style={{ opacity: 0.7 }}>with hover point/arc → highlight routes & speed up</span>
        </div>
      </div>
    </div>
  );
}

function LegendSwatch({ color, label }) {
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
      <span style={{ width: 10, height: 10, borderRadius: 999, background: color, display: 'inline-block' }} />
      <span>{label}</span>
    </span>
  );
}
