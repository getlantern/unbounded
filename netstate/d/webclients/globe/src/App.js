import logo from './logo.svg';
import './App.css';
import Globe from 'react-globe.gl';
import { useState, useEffect } from 'react';

function App() {
  const netstatedUrl = process.env.REACT_APP_NETSTATE_URL || "https://netstated-d7bbec1ed55b.herokuapp.com/data";
  const [peerData, setPeerData] = useState([]);

  useEffect(() => {
    fetchData();

    const fetchInterval = setInterval(fetchData, 30000);

    return () => {
      clearInterval(fetchInterval);
    }
  }, []);
  
  const fetchData = () => {
    fetch(netstatedUrl).then(res => res.json()).then((data) => {
      setPeerData(data);
      console.log(data);
    });  
  }
  
  // A little example to illustrate the structure of the JSON returned by netstated, you can 
  // uncomment this and use it for testing...
  /**
  const exampleData = [
    {t: 0, lat: 40.730610, lon: -73.935242, lastSeen: null, edges: [3, 3, 4, 5]},                               // an uncensored peer in NY
    {t: 0, lat: 39.9526, lon: -75.165222, lastSeen: null, edges: [3, 5]},                                       // an uncensored peer in Philly
    {t: 0, lat: 36.1716, lon: -115.176468, lastSeen: null, edges: [4]},                                         // an uncensored peer somewhere out west
    {t: 1, lat: 35.7219, lon: 51.3347, lastSeen: (new Date()).toISOString(), edges: []},                        // a censored peer somewhere
    {t: 1, lat: 55.7558, lon: 37.6173, lastSeen: new Date(Date.now() - 1 * 60000).toISOString(), edges: []},    // a censored peer somewhere
    {t: 1, lat: 31.2304, lon: 121.469170, lastSeen: new Date(Date.now() - 3 * 60000).toISOString(), edges: []}, // a censored peer somewhere
    {t: 1, lat: 27.1963, lon: 56.2884, lastSeen: new Date(Date.now() - 5.5 * 60000).toISOString(), edges: []}   // a censored peer somewhere
  ];
  */
  
  const arcsData = peerData.map(peer => peer.edges.map((edge) => {
    return {
      startLat: peer.lat, 
      startLng: peer.lon, 
      endLat: peerData[edge].lat, 
      endLng: peerData[edge].lon, 
      color: ["green", "red"]
    };
  })).flat(); 
  
  const pointsData = peerData.map((peer) => {
    return {
      lat: peer.lat, 
      lng: peer.lon, 
      color: peer.t === 0 ? "green" : "red"
    };
  });
  
  const reqTime = Date.now();
  
  const heatmapsData = peerData.filter(peer => peer.t === 1).map((peer) => {
    const ttl = 5 * 60000;
    const w = (ttl - (reqTime - new Date(peer.lastSeen).getTime())) / ttl;
    return {lat: peer.lat, lng: peer.lon, weight: w}; 
  });
  
  return (
    <div className="App">
      <Globe
        globeImageUrl="//unpkg.com/three-globe/example/img/earth-night.jpg"
        arcsData={arcsData}
        arcColor="color"
        arcAltitude={0.3}
        pointsData={pointsData}
        pointColor="color"
        pointAltitude={0.005}
        pointRadius={0.2}
        heatmapsData={[heatmapsData]}
        heatmapBaseAltitude={0.001}
        heatmapPointLat="lat"
        heatmapPointLng="lng"
        heatmapPointWeight={"weight"}
        heatmapBandwidth={2}
      />,
    </div>
  );
}

export default App;
