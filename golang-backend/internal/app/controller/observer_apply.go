package controller

import (
    "time"

    dbpkg "network-panel/golang-backend/internal/db"
    "network-panel/golang-backend/internal/app/model"
    "network-panel/golang-backend/internal/app/util"
    "fmt"
)

// EnsureObserverOnce scans all forwards and pushes an UpdateService patch to attach the shared observer
// to entry services. It is safe to call repeatedly; agent will merge observers without duplicating.
func EnsureObserverOnce() {
    // For each node, recompute desired port-forward entry services (with observer) and add/upsert.
    var nodes []model.Node
    dbpkg.DB.Find(&nodes)
    for _, n := range nodes {
        svcs := desiredServices(n.ID) // port-forward only; includes observer injection
        if len(svcs) == 0 { continue }
        _ = sendWSCommand(n.ID, "AddService", svcs)
    }
}

// EnsureObserverLoop runs periodically to enforce observer attachment.
func EnsureObserverLoop(interval time.Duration) {
    if interval <= 0 { interval = 60 * time.Second }
    ticker := time.NewTicker(interval)
    defer ticker.Stop()
    for {
        EnsureObserverOnce()
        <-ticker.C
    }
}

// Build observer-only patches for tunnel-forward entry services on a node.
// Returns []map suitable for UpdateService: {name, handler:{observer}, _observers:[...]}
func BuildTunnelEntryObserverPatches(nodeID int64) []map[string]any {
    type row struct {
        ID       int64
        UserID   int64
        TunnelID int64
        InNodeID int64
        TType    int
    }
    var rows []row
    dbpkg.DB.Table("forward f").
        Select("f.id, f.user_id, f.tunnel_id, t.in_node_id, t.type as t_type").
        Joins("left join tunnel t on t.id = f.tunnel_id").
        Where("t.type = 2 AND t.in_node_id = ?", nodeID).
        Scan(&rows)
    patches := make([]map[string]any, 0, len(rows))
    for _, r := range rows {
        name := buildServiceName(r.ID, r.UserID, r.TunnelID)
        if obsName, spec := buildObserverPluginSpec(nodeID, name); obsName != "" && spec != nil {
            patches = append(patches, map[string]any{
                "name":       name,
                "observer":   obsName,
                "_observers": []any{spec},
            })
        }
    }
    return patches
}

// BuildTunnelEntryServices builds full entry services (AddService payload) for tunnel-forwards hosted on a node.
// For multi-level path tunnels, this helper currently skips full rebuild and relies on observer patches to avoid
// re-deriving mid hop dynamic ports; single-hop tunnels are fully constructed.
func BuildTunnelEntryServices(nodeID int64) []map[string]any {
    type row struct {
        model.Forward
        TType        int     `gorm:"column:t_type"`
        InNodeID     int64   `gorm:"column:in_node_id"`
        OutNodeID    *int64  `gorm:"column:out_node_id"`
        TInterface   *string `gorm:"column:t_interface"`
    }
    var rows []row
    dbpkg.DB.Table("forward f").
        Select("f.*, t.type as t_type, t.in_node_id, t.out_node_id, t.interface_name as t_interface").
        Joins("left join tunnel t on t.id = f.tunnel_id").
        Where("t.type = 2 AND t.in_node_id = ?", nodeID).
        Scan(&rows)
    if len(rows) == 0 { return nil }

    // fetch path and bind maps per tunnel id once to avoid extra queries
    // we will reuse forward.go helpers from same package
    services := make([]map[string]any, 0, len(rows))
    for _, r := range rows {
        // Skip if no inPort or not tunnel-forward
        if r.TType != 2 || r.InPort <= 0 { continue }
        // If multi-level path configured, skip full rebuild here (avoid drift); observer patch will still apply.
        if path := getTunnelPathNodes(r.TunnelID); len(path) > 0 {
            continue
        }
        // Build entry service same as ForwardCreate (single-hop tunnel case)
        name := buildServiceName(r.ID, r.UserID, r.TunnelID)
        user := fmt.Sprintf("u-%d", r.ID)
        pass := util.MD5(fmt.Sprintf("%d:%d", r.ID, r.CreatedTime))[:16]

        // Resolve exit address host:port
        var tun model.Tunnel
        _ = dbpkg.DB.First(&tun, r.TunnelID).Error
        outIP := getOutNodeIP(tun)
        bindMap := getTunnelBindMap(tun.ID)
        exitID := outNodeIDOr0(tun)
        if v, ok := bindMap[exitID]; ok && v != "" {
            outIP = v
        }
        outPort := 0
        if r.OutPort != nil { outPort = *r.OutPort }
        exitAddr := safeHostPort(outIP, outPort)

        svc := map[string]any{
            "name":     name,
            "addr":     fmt.Sprintf(":%d", r.InPort),
            "listener": map[string]any{"type": "tcp"},
            "handler":  map[string]any{"type": "forward", "chain": "chain_" + name},
            "metadata": map[string]any{"managedBy": "network-panel", "enableStats": true, "observer.period": "5s", "observer.resetTraffic": false},
        }
        // Attach service-level observer (top-level) and register plugin via _observers
        if obsName, spec := buildObserverPluginSpec(nodeID, name); obsName != "" && spec != nil {
            svc["observer"] = obsName
            svc["_observers"] = []any{spec}
        }

        chainName := "chain_" + name
        hopName := "hop_" + name
        node := map[string]any{
            "name": "node-" + name,
            "addr": exitAddr,
            "connector": map[string]any{"type": "relay", "auth": map[string]any{"username": user, "password": pass}},
            "dialer":    map[string]any{"type": "grpc"},
        }
        svc["_chains"] = []any{map[string]any{"name": chainName, "metadata": map[string]any{"managedBy": "network-panel", "enableStats": true, "observer.period": "5s", "observer.resetTraffic": false}, "hops": []any{map[string]any{"name": hopName, "nodes": []any{node}}}}}
        // Forwarder target is remoteAddr's first host
        svc["forwarder"] = map[string]any{"nodes": []map[string]any{{"name": "target", "addr": firstTargetHost(r.RemoteAddr)}}}

        // Optional interface preference
        iface := preferIface(r.InterfaceName, r.TInterface)
        if iface != nil && *iface != "" {
            if meta, _ := svc["metadata"].(map[string]any); meta != nil {
                meta["interface"] = *iface
            }
        }
        services = append(services, svc)
    }
    return services
}

// --- Multi-level path full rebuild on reconnect ---

// helper: cache of node services for quick lookup by name->port
type svcIndex struct {
    byName map[string]int // name -> port
}

func makeSvcIndex(list []map[string]any) *svcIndex {
    m := map[string]int{}
    for _, s := range list {
        name, _ := s["name"].(string)
        if name == "" { continue }
        port := 0
        if v, _ := s["addr"].(string); v != "" {
            port = parsePort(v)
        }
        if port == 0 {
            if lst, ok := s["listener"].(map[string]any); ok {
                if v, ok2 := lst["addr"].(string); ok2 { port = parsePort(v) }
            }
        }
        if port > 0 { m[name] = port }
    }
    return &svcIndex{byName: m}
}

// BuildPathServicesForReconnect returns per-node services for all forwards involving rootNode (entry/path/exit)
func BuildPathServicesForReconnect(rootNode int64) map[int64][]map[string]any {
    // Preload all forwards and tunnels
    type row struct {
        model.Forward
        TType      int     `gorm:"column:t_type"`
        InNodeID   int64   `gorm:"column:in_node_id"`
        OutNodeID  *int64  `gorm:"column:out_node_id"`
        OutIP      *string `gorm:"column:out_ip"`
        TInterface *string `gorm:"column:t_interface"`
    }
    var rows []row
    dbpkg.DB.Table("forward f").
        Select("f.*, t.type as t_type, t.in_node_id, t.out_node_id, t.out_ip, t.interface_name as t_interface").
        Joins("left join tunnel t on t.id = f.tunnel_id").
        Scan(&rows)
    if len(rows) == 0 { return nil }

    // cache: node services and used ports
    svcCache := map[int64]*svcIndex{}
    usedPortsCache := map[int64]map[int]bool{}
    getSvcIdx := func(nodeID int64) *svcIndex {
        if idx, ok := svcCache[nodeID]; ok { return idx }
        list := queryNodeServicesRaw(nodeID)
        idx := makeSvcIndex(list)
        svcCache[nodeID] = idx
        return idx
    }
    getUsed := func(nodeID int64) map[int]bool {
        if m, ok := usedPortsCache[nodeID]; ok { return m }
        m := queryNodeServicePorts(nodeID)
        usedPortsCache[nodeID] = m
        return m
    }

    out := map[int64][]map[string]any{}
    for _, r := range rows {
        // path for this tunnel
        path := getTunnelPathNodes(r.TunnelID)
        if len(path) == 0 { continue }
        // determine whether this forward involves rootNode (entry/path/exit)
        involves := (r.InNodeID == rootNode)
        if !involves {
            for _, nid := range path { if nid == rootNode { involves = true; break } }
        }
        if !involves && r.TType == 2 && r.OutNodeID != nil && *r.OutNodeID == rootNode { involves = true }
        if !involves { continue }

        name := buildServiceName(r.ID, r.UserID, r.TunnelID)
        ifaceMap := getTunnelIfaceMap(r.TunnelID)
        bindMap := getTunnelBindMap(r.TunnelID)

        if r.TType == 1 {
            // port-forward with path: hops = entry + mids
            hops := append([]int64{r.InNodeID}, path...)
            hopPorts := make([]int, len(hops))
            hopPorts[0] = r.InPort
            // precompute ports for mids
            for i := 1; i < len(hops); i++ {
                curID := hops[i]
                // prefer existing named service port
                midIdx := i - 1
                svcName := fmt.Sprintf("%s_mid_%d", name, midIdx)
                if p, ok := getSvcIdx(curID).byName[svcName]; ok && p > 0 {
                    hopPorts[i] = p
                    continue
                }
                // allocate new port
                prevID := hops[i-1]
                prevOut := ifaceMap[prevID]
                curIn := bindMap[curID]
                overlay := isOverlayIP(prevOut) && isOverlayIP(curIn)
                if overlay {
                    hopPorts[i] = findFreePortOnNodeAny(curID, 10000, 10000)
                    if hopPorts[i] == 0 { hopPorts[i] = 10000 }
                } else {
                    // infer range from node
                    var n model.Node
                    _ = dbpkg.DB.First(&n, curID).Error
                    minP, maxP := 10000, 65535
                    if n.PortSta > 0 { minP = n.PortSta }
                    if n.PortEnd > 0 { maxP = n.PortEnd }
                    // Avoid used
                    _ = getUsed(curID)
                    hopPorts[i] = findFreePortOnNode(curID, minP, minP, maxP)
                    if hopPorts[i] == 0 { hopPorts[i] = minP }
                }
            }
            // build services per hop and group by node
            for i := 0; i < len(hops); i++ {
                nodeID := hops[i]
                listenPort := hopPorts[i]
                var target string
                if i < len(hops)-1 {
                    nextID := hops[i+1]
                    host := bindMap[nextID]
                    if host == "" {
                        // pick IPv4 when possible
                        var nx model.Node
                        _ = dbpkg.DB.First(&nx, nextID).Error
                        host = preferIPv4(nx)
                        if host == "" { host = firstIPAny(nx) }
                    }
                    target = safeHostPort(host, hopPorts[i+1])
                } else {
                    target = firstTargetHost(r.RemoteAddr)
                }
                var iface *string
                if ip, ok := ifaceMap[nodeID]; ok && ip != "" { tmp := ip; iface = &tmp } else { iface = preferIface(r.InterfaceName, r.TInterface) }
                svcName := name
                if i > 0 { svcName = fmt.Sprintf("%s_mid_%d", name, i-1) }
                svc := buildServiceConfig(svcName, listenPort, target, iface)
                if obsName, spec := buildObserverPluginSpec(nodeID, name); obsName != "" && spec != nil {
                    svc["observer"] = obsName
                    svc["_observers"] = []any{spec}
                }
                out[nodeID] = append(out[nodeID], svc)
            }
            continue
        }

        // tunnel-forward (type=2) with path
        // compute mid ports for path nodes
        midPorts := make([]int, len(path))
        for i := 0; i < len(path); i++ {
            curID := path[i]
            svcName := fmt.Sprintf("%s_mid_%d", name, i)
            if p, ok := getSvcIdx(curID).byName[svcName]; ok && p > 0 { midPorts[i] = p; continue }
            // allocate
            prevID := r.InNodeID
            if i > 0 { prevID = path[i-1] }
            prevOut := ifaceMap[prevID]
            curIn := bindMap[curID]
            overlay := isOverlayIP(prevOut) && isOverlayIP(curIn)
            if overlay {
                midPorts[i] = findFreePortOnNodeAny(curID, 10000, 10000)
                if midPorts[i] == 0 { midPorts[i] = 10000 }
            } else {
                var n model.Node
                _ = dbpkg.DB.First(&n, curID).Error
                minP, maxP := 10000, 65535
                if n.PortSta > 0 { minP = n.PortSta }
                if n.PortEnd > 0 { maxP = n.PortEnd }
                _ = getUsed(curID)
                midPorts[i] = findFreePortOnNode(curID, minP, minP, maxP)
                if midPorts[i] == 0 { midPorts[i] = minP }
            }
        }
        // exit relay service (ensure)
        if r.OutPort != nil && r.OutNodeID != nil {
            user := fmt.Sprintf("u-%d", r.ID)
            pass := util.MD5(fmt.Sprintf("%d:%d", r.ID, r.CreatedTime))[:16]
            outSvc := map[string]any{
                "name":     name,
                "addr":     fmt.Sprintf(":%d", *r.OutPort),
                "listener": map[string]any{"type": "grpc"},
                "handler":  map[string]any{"type": "relay", "auth": map[string]any{"username": user, "password": pass}},
                "metadata": map[string]any{"managedBy": "network-panel", "enableStats": true, "observer.period": "5s", "observer.resetTraffic": false},
            }
            out[*r.OutNodeID] = append(out[*r.OutNodeID], outSvc)
        }
        // entry service with chain to first mid
        if len(path) > 0 {
            user := fmt.Sprintf("u-%d", r.ID)
            pass := util.MD5(fmt.Sprintf("%d:%d", r.ID, r.CreatedTime))[:16]
            inSvc := map[string]any{
                "name":     name,
                "addr":     fmt.Sprintf(":%d", r.InPort),
                "listener": map[string]any{"type": "tcp"},
                "handler":  map[string]any{"type": "forward", "chain": "chain_" + name},
                "metadata": map[string]any{"managedBy": "network-panel", "enableStats": true, "observer.period": "5s", "observer.resetTraffic": false},
            }
            if obsName, spec := buildObserverPluginSpec(r.InNodeID, name); obsName != "" && spec != nil {
                inSvc["observer"] = obsName
                inSvc["_observers"] = []any{spec}
            }
            // first mid target
            firstID := path[0]
            var firstN model.Node
            _ = dbpkg.DB.First(&firstN, firstID).Error
            node := map[string]any{
                "name": "node-" + name,
                "addr": safeHostPort(func() string { h := preferIPv4(firstN); if h=="" { h = firstIPAny(firstN) }; return h }(), midPorts[0]),
                "connector": map[string]any{"type": "relay", "auth": map[string]any{"username": user, "password": pass}},
                "dialer":    map[string]any{"type": "grpc"},
            }
            chainName := "chain_" + name
            hopName := "hop_" + name
            inSvc["_chains"] = []any{map[string]any{"name": chainName, "metadata": map[string]any{"managedBy": "network-panel", "enableStats": true, "observer.period": "5s", "observer.resetTraffic": false}, "hops": []any{map[string]any{"name": hopName, "nodes": []any{node}}}}}
            inSvc["forwarder"] = map[string]any{"nodes": []map[string]any{{"name": "target", "addr": firstTargetHost(r.RemoteAddr)}}}
            out[r.InNodeID] = append(out[r.InNodeID], inSvc)
        }
        // mid services: each forwards to next hop or remote target
        for i := 0; i < len(path); i++ {
            nodeID := path[i]
            listenPort := midPorts[i]
            var target string
            if i < len(path)-1 {
                nextID := path[i+1]
                var nx model.Node
                _ = dbpkg.DB.First(&nx, nextID).Error
                host := bindMap[nextID]
                if host == "" { host = preferIPv4(nx); if host=="" { host = firstIPAny(nx) } }
                target = safeHostPort(host, midPorts[i+1])
            } else {
                target = firstTargetHost(r.RemoteAddr)
            }
            var iface *string
            if ip, ok := ifaceMap[nodeID]; ok && ip != "" { tmp := ip; iface = &tmp } else { iface = preferIface(r.InterfaceName, r.TInterface) }
            midName := fmt.Sprintf("%s_mid_%d", name, i)
            svc := buildServiceConfig(midName, listenPort, target, iface)
            if obsName, spec := buildObserverPluginSpec(nodeID, name); obsName != "" && spec != nil {
                svc["observer"] = obsName
                svc["_observers"] = []any{spec}
            }
            out[nodeID] = append(out[nodeID], svc)
        }
    }
    return out
}
