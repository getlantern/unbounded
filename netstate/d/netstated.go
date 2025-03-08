package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/getlantern/broflake/common"
	netstatecl "github.com/getlantern/broflake/netstate/client"
	"github.com/getlantern/geo"
)

const (
	ttl = 5 * time.Minute // How long do vertices live before we prune them?
)

const (
	clientTypeUncensored clientType = iota
	clientTypeCensored
)

var (
	world     multigraph
	geolookup geo.Lookup
	geoDb     string
)

type vertexLabel string
type clientType int

func (c clientType) String() string {
	switch c {
	case clientTypeCensored:
		return "censored"
	case clientTypeUncensored:
		return "uncensored"
	}

	return "unknown"
}

type publicPeerData struct {
	T        int       `json:"t"`
	Lat      float64   `json:"lat"`
	Lon      float64   `json:"lon"`
	LastSeen time.Time `json:"lastSeen"`
	Edges    []int     `json:"edges"`
}

type vertex struct {
	edges    []edge
	lastSeen time.Time
	lat      float64
	lon      float64
	t        clientType
}

// Parallel edges possess the same label but must have different IDs
// don't create multiple edges of vertex v with the same ID!
type edge struct {
	label vertexLabel
	id    string
}

// multigraph is a threadsafe multigraph represented as an adjacency list. It's an identity bearing
// multigraph (parallel edges between vertices possess distinct identities). You can use it like
// a directed graph or an undirected graph (depending on whether you create reciprocal edges). We
// use it like a directed graph, where edge direction indicates who is helping who. That is, an
// outedge from vertex A to vertex B indicates that A is an uncensored user helping censored user B.
type multigraph struct {
	data map[vertexLabel]vertex
	sync.RWMutex
}

func newMultigraph() *multigraph {
	return &multigraph{data: make(map[vertexLabel]vertex)}
}

// Idempotently add a vertex; if this vertex already exists, just update all of its properties
func (g *multigraph) addVertex(v vertexLabel, lat, lon float64, t clientType) {
	g.Lock()
	defer g.Unlock()

	if _, ok := g.data[v]; !ok {
		g.data[v] = vertex{}
	}

	vv := g.data[v]
	vv.lastSeen = time.Now()
	vv.lat = lat
	vv.lon = lon
	vv.t = t
	g.data[v] = vv
}

// Get the degree of vertex v, returns 0 if v does not exist
func (g *multigraph) degree(v vertexLabel) int {
	g.RLock()
	defer g.RUnlock()
	return len(g.data[v].edges)
}

// prune deletes expired vertices from this multigraph based on the delta between ttl and the current time
// TODO: this is an unoptimized solution, requiring two passes through the data structure
func (g *multigraph) prune(ttl time.Duration) {
	g.Lock()
	defer g.Unlock()

	now := time.Now()
	killList := make(map[vertexLabel]bool)

	// Determine which vertices are expired and delete them
	for label, vertex := range g.data {
		if vertex.lastSeen.Add(ttl).Before(now) {
			killList[label] = true
			delete(g.data, label)
		}
	}

	// Now clean up the dangling edges
	for label := range g.data {
		for i, edge := range g.data[label].edges {
			if _, ok := killList[edge.label]; ok {
				g.data[label].edges[i] = g.data[label].edges[len(g.data[label].edges)-1]

				vv := g.data[label]
				vv.edges = vv.edges[:len(vv.edges)-1]
				g.data[label] = vv
			}
		}
	}
}

// Encode this multigraph as a Graphviz graph using the 'neato' layout
func (g *multigraph) toGraphvizNeato() string {
	printedLabel := func(v vertexLabel, t clientType, lat, lon float64) string {
		return fmt.Sprintf("%v [%v]\nlat: %v, lon: %v", v, t, lat, lon)
	}

	g.RLock()
	defer g.RUnlock()

	gv := "digraph G {\n"
	gv += "\tlayout=neato\n"
	gv += "\toverlap=false\n"
	gv += "\tsep=\"+20\"\n"

	// Encode the vertices
	for vertexLabel, vertex := range g.data {
		printedVertexLabel := printedLabel(vertexLabel, vertex.t, vertex.lat, vertex.lon)
		gv += fmt.Sprintf("\t\"%v\";\n", printedVertexLabel)
	}

	// Encode the edges
	for vertexLabel, vertex := range g.data {
		printedVertexLabel := printedLabel(vertexLabel, vertex.t, vertex.lat, vertex.lon)

		for _, e := range vertex.edges {
			printedEdgeLabel := printedLabel(e.label, g.data[e.label].t, g.data[e.label].lat, g.data[e.label].lon)
			gv += fmt.Sprintf("\t\"%v\" -> \"%v\";\n", printedVertexLabel, printedEdgeLabel)
		}
	}

	gv += "}\n"
	return gv
}

// Encode this multigraph as a slice of publicPeerDatas
func (g *multigraph) toPublicPeerData() []publicPeerData {
	g.RLock()
	defer g.RUnlock()

	// This encoding is an adjacency list which discards vertex labels and instead uses the element
	// index of each vertex as a label. Go maps don't maintain insertion order and iteration order is
	// random, so we first need to enumerate a vertex order in a lookup table to use during translation...
	peerIdx := make(map[vertexLabel]int)
	i := 0

	for vl := range g.data {
		peerIdx[vl] = i
		i++
	}

	ppd := make([]publicPeerData, len(peerIdx))

	for vl, vertex := range g.data {
		peerData := publicPeerData{T: int(vertex.t), Lat: vertex.lat, Lon: vertex.lon, LastSeen: vertex.lastSeen}
		peerEdges := []int{}

		for _, e := range vertex.edges {
			peerEdges = append(peerEdges, peerIdx[e.label])
		}

		peerData.Edges = peerEdges
		ppd[peerIdx[vl]] = peerData
	}

	return ppd
}

// GET /neato
// Fetch a Graphviz encoded representation of the global network topology, 'neato' layout
// TODO: this is a massively unoptimized approach where we prune and encode the graph upon every
// request, both of which are expensive operations. In the near future, we'll want to run a
// prune/encode job not very often, and cache the last state of the world to serve requests.
func handleNeato(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)

	// Handle preflight requests
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	world.prune(ttl)
	g := world.toGraphvizNeato()
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(g))
}

// GET /data
// Fetch a JSON encoded representation of the global network topology, structured as an array of
// peerData objects. TODO: like handleNeato above, this is massively unoptimized.
func handleData(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)

	// Handle preflight requests
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	world.prune(ttl)
	ppd := world.toPublicPeerData()

	j, err := json.Marshal(ppd)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("500"))
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(j)
}

// POST /exec
// Execute a state change
func handleExec(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)

	// Handle preflight requests
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		common.Debugf("Error: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("400\n"))
		return
	}

	inst := netstatecl.Instruction{}
	err = json.Unmarshal(b, &inst)
	if err != nil {
		common.Debugf("Error: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("400\n"))
		return
	}

	// XXX: A word about the graph we're building: as of 12/18/2023, only uncensored users report
	// to netstate, and we build a network graph based on their view of the world. When they connect
	// or disconnect from a censored consumer, they report it here, and we're able to add or remove
	// a censored vertex accordingly. One consequence of this rather funky approach is that we derive
	// vertex labels for uncensored and censored users in two different ways: for uncensored vertices,
	// we use the IP addr (no port) from their HTTP request plus their self-reported tag, but for censored
	// vertices, we use the remote IP addr (no port) and tag which as passed as operation arguments by the
	// uncensored user. Given these two different derivations, our vertex labeling scheme is pretty
	// brittle and could easily result in broken network graphs. The correct solution is to push the
	// necessary changes to censored Lantern clients such that they report themselves to netstate!

	// Depending on whether netstated is deployed behind a load balancer or some other infrastructural
	// doohickey, we may need to check a few different places to find a public IP address for the requester
	rawAddr := r.Header.Get("X-Real-Ip")
	if rawAddr == "" {
		rawAddr = r.Header.Get("X-Forwarded-For")
	}
	if rawAddr == "" {
		rawAddr = r.RemoteAddr
	}

	// rawAddr may or may not have a port; to handle the ambiguity, we'll try parsing it a couple
	// different ways in a failover pattern
	var parsedAddr net.IP

	addrPort, err := netip.ParseAddrPort(rawAddr)
	if err != nil {
		// It didn't seem to have a port, so let's try parsing it as an IP address
		parsedAddr = net.ParseIP(rawAddr)
	} else {
		// It seemed to have a port, so let's discard the port and keep the IP address
		parsedAddr = net.IP(addrPort.Addr().AsSlice())
	}

	// If we've failed to make sense of the requester's IP address, let's just not execute this state change
	if parsedAddr == nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("400\n"))
		return
	}

	localLabel := vertexLabel(fmt.Sprintf("%v (%v)", parsedAddr, inst.Tag))

	// TODO: This switch is the interpreter, we could extract it into a function
	switch inst.Op {
	case netstatecl.OpConsumerState:
		consumers := netstatecl.DecodeArgsOpConsumerState(inst.Args)

		// 1. Idempotently add a vertex representing the reporting node, updaing its lastSeen time and lat/lon
		// 2. Idempotently add a vertex representing each reported consumer, updating its lastSeen time and lat/lon
		// 3. Replace the reporting node's edges with a new set of edges representing its current consumers

		lat, lon := geolocate(geoDb, parsedAddr)
		world.addVertex(localLabel, lat, lon, clientTypeUncensored)

		var newEdges []edge

		for _, c := range consumers {
			remoteAddr, remoteTag, workerIdx := c[0], c[1], c[2]

			parsedIP := net.ParseIP(remoteAddr)
			if parsedIP == nil {
				continue
			}

			lat, lon := geolocate(geoDb, parsedIP)
			remoteLabel := vertexLabel(fmt.Sprintf("%v (%v)", remoteAddr, remoteTag))
			world.addVertex(remoteLabel, lat, lon, clientTypeCensored)
			newEdges = append(newEdges, edge{label: remoteLabel, id: workerIdx})
		}

		vv := world.data[localLabel]
		vv.edges = newEdges
		world.data[localLabel] = vv
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("200\n"))
}

// TODO: delete me and replace with a real CORS strategy!
func enableCors(w *http.ResponseWriter) {
	(*w).Header().Set("Access-Control-Allow-Origin", "*")
	(*w).Header().Set("Access-Control-Allow-Credentials", "*")
	(*w).Header().Set("Access-Control-Allow-Methods", "GET,HEAD,OPTIONS,POST,PUT")
	(*w).Header().Set(
		"Access-Control-Allow-Headers",
		"Access-Control-Allow-Headers, Origin,Accept, X-Requested-With, Content-Type, "+
			"Access-Control-Request-Method, Access-Control-Request-Headers",
	)
}

func geolocate(geoDb string, addr net.IP) (lat float64, lon float64) {
	if geoDb != "" {
		lat, lon = geolookup.LatLong(addr)
	}

	return lat, lon
}

func main() {
	// If GEODB is unspecified, we'll run netstated sans geolocation
	geoDb = os.Getenv("GEODB")

	if geoDb != "" {
		_, err := url.ParseRequestURI(geoDb)

		if err != nil {
			common.Debugf("GEODB is not a valid URL! We won't perform geolocation...")
		} else {
			common.Debugf("Using %v for geolocation...", geoDb)
		}
	} else {
		common.Debug("GEODB not specified! We won't perform geolocation...")
	}

	// If UNSAFE == 1, we'll expose the Graphviz-related endpoints which are useful for debugging,
	// but which surface private user data including IP address
	unsafe, err := strconv.ParseInt(os.Getenv("UNSAFE"), 10, 64)

	if err != nil {
		common.Debugf("UNSAFE not specified or not valid! Using default value...")
	}

	if unsafe == 0 {
		common.Debugf("UNSAFE=0, we won't expose Graphviz endpoints...")
	} else if unsafe == 1 {
		common.Debugf("*** WARNING *** UNSAFE=1, we'll expose Graphviz endpoints!")
	}

	// The gv client is hardcoded to hit the /neato endpoint on port 8080, so we don't currently
	// support running netstated on a different port
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	world = *newMultigraph()

	if geoDb != "" {
		// XXX: The API for github.com/getlantern/geo requires 3 repetitive arguments for reasons that
		// aren't clear. To make it easier to configure netstated, we'll assume that GEODB is a URL
		// pointing to a compressed MaxMind database file ending in .tar.gz, and we'll extract the
		// 2nd and 3rd arguments from the 1st.
		filenameUnzipped := strings.ReplaceAll(path.Base(geoDb), ".tar.gz", "")

		geolookup = geo.LatLongFromWeb(
			geoDb,
			filenameUnzipped,
			24*time.Hour,
			filenameUnzipped,
			geo.LatLong,
		)

		geolookup.Ready()
	}

	srv := &http.Server{
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		Addr:         fmt.Sprintf(":%v", port),
	}

	if unsafe == 1 {
		http.Handle("/", http.FileServer(http.Dir("./webclients/gv/public")))
		http.HandleFunc("/neato", handleNeato)
	}

	if unsafe == 1 {
		http.Handle("/globe/", http.StripPrefix("/globe/", http.FileServer(http.Dir("./webclients/globe/build"))))
	}

	http.HandleFunc("/data", handleData)
	http.HandleFunc("/exec", handleExec)
	common.Debugf("netstated listening on %v", srv.Addr)
	err = srv.ListenAndServe()
	if err != nil {
		common.Debug(err)
	}
}
