package controller

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"network-panel/golang-backend/internal/app/util"
	"strconv"
	"strings"
	"time"

	"network-panel/golang-backend/internal/app/dto"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	dbpkg "network-panel/golang-backend/internal/db"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// POST /api/v1/forward/create
func ForwardCreate(c *gin.Context) {
	var req dto.ForwardDto
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	uidInf, _ := c.Get("user_id")
	uid := uidInf.(int64)
	var tun model.Tunnel
	if err := dbpkg.DB.First(&tun, req.TunnelID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("隧道不存在"))
		return
	}
	// if user is not admin, ensure permission exists and check simple limits
	var ut model.UserTunnel
	roleInf, _ := c.Get("role_id")
	if roleInf != 0 {
		if err := dbpkg.DB.Where("user_id=? and tunnel_id=?", uid, req.TunnelID).First(&ut).Error; err != nil {
			c.JSON(http.StatusOK, response.ErrMsg("你没有该隧道权限"))
			return
		}
		// forward quota
		var cfg model.ViteConfig
		dbpkg.DB.Where("name=?", "registration_default_forward").First(&cfg)
		limit := 20
		if n, err := strconv.Atoi(strings.TrimSpace(cfg.Value)); err == nil && n > 0 {
			limit = n
		}
		var cnt int64
		dbpkg.DB.Model(&model.Forward{}).Where("user_id=?", uid).Count(&cnt)
		if int(cnt) >= limit {
			c.JSON(http.StatusOK, response.ErrMsg("超出转发数量上限"))
			return
		}
	}
	// allocate inPort if nil: find first port in range not used
	inPort := 0
	if req.InPort != nil {
		inPort = *req.InPort
	} else {
		inPort = firstFreePort(tun.InNodeID, tun, 0)
	}
	// agent-level verification and range enforcement on entry node
	var inNode model.Node
	_ = dbpkg.DB.First(&inNode, tun.InNodeID).Error
	minP := 10000
	maxP := 65535
	if inNode.PortSta > 0 {
		minP = inNode.PortSta
	}
	if inNode.PortEnd > 0 {
		maxP = inNode.PortEnd
	}
	// if provided port not in range, ignore it and pick a free one in range
	if inPort < minP || inPort > maxP {
		inPort = 0
	}
	inPort = findFreePortOnNode(tun.InNodeID, inPort, minP, maxP)
	if inPort == 0 {
		c.JSON(http.StatusOK, response.ErrMsg("隧道入口端口已满，无法分配新端口"))
		return
	}
	now := time.Now().UnixMilli()
	f := model.Forward{BaseEntity: model.BaseEntity{CreatedTime: now, UpdatedTime: now}, UserID: uid, Name: req.Name, TunnelID: req.TunnelID, InPort: inPort, RemoteAddr: req.RemoteAddr, InterfaceName: req.InterfaceName, Strategy: req.Strategy}
	// allocate outPort for tunnel-forward
	if tun.Type == 2 {
		if op := firstFreePortOut(tun, 0); op != 0 {
			// verify against agent services on exit node
			exitID := outNodeIDOr0(tun)
			var outNode model.Node
			_ = dbpkg.DB.First(&outNode, exitID).Error
			minO := 10000
			maxO := 65535
			if outNode.PortSta > 0 {
				minO = outNode.PortSta
			}
			if outNode.PortEnd > 0 {
				maxO = outNode.PortEnd
			}
			free := findFreePortOnNode(exitID, op, minO, maxO)
			if free == 0 {
				c.JSON(http.StatusOK, response.ErrMsg("隧道出口端口已满，无法分配新端口"))
				return
			}
			f.OutPort = &free
		} else {
			c.JSON(http.StatusOK, response.ErrMsg("隧道出口端口已满，无法分配新端口"))
			return
		}
	}
	if err := dbpkg.DB.Create(&f).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("端口转发创建失败"))
		return
	}
	// push to node(s)
	opId := RandUUID()
	name := buildServiceName(f.ID, f.UserID, f.TunnelID)
	if tun.Type == 2 && f.OutPort != nil {
		// gRPC HTTP 隧道（出口=relay+grpc，入口=http+chain(dialer=grpc, connector=relay)）
		user := fmt.Sprintf("u-%d", f.ID)
		pass := util.MD5(fmt.Sprintf("%d:%d", f.ID, f.CreatedTime))[:16]
		outSvc := map[string]any{
			"name":     name,
			"addr":     fmt.Sprintf(":%d", *f.OutPort),
			"listener": map[string]any{"type": "grpc"},
			// 出口不再配置 chain，仅作为 relay 服务端
			"handler":  map[string]any{"type": "relay", "auth": map[string]any{"username": user, "password": pass}},
			"metadata": map[string]any{"managedBy": "network-panel", "enableStats": true, "observer.period": "5s", "observer.resetTraffic": false},
		}
		_ = sendWSCommand(outNodeIDOr0(tun), "AddService", expandRUDP([]map[string]any{outSvc}))

		// 计算出口地址：优先使用出口节点的监听IP(在隧道编辑里配置的 inIp/bind)，否则使用隧道/节点出口IP
		outIP := getOutNodeIP(tun)
		bindMap := getTunnelBindMap(tun.ID)
		if v, ok := bindMap[outNodeIDOr0(tun)]; ok && v != "" {
			outIP = v
		}
		exitAddr := safeHostPort(outIP, *f.OutPort)

		// Multi-level path support for tunnel-forward:
		// - exit: relay over gRPC (server)
		// - mids: plain TCP forward (listen on port, forward to next hop addr:port)
		// - entry: HTTP handler with chain(connector=relay, dialer=grpc) targeting FIRST MID addr:port
		path := getTunnelPathNodes(tun.ID)
		if len(path) > 0 {
			// Pre-allocate TCP ports on mids (avoid conflicts using agent query)
			// 优化：当“上一跳出口IP”和“下一跳入口IP”均为 10.126.126.* 组网内网时，端口不受节点端口范围限制，仅需 >=1000 且未被占用
			midPorts := make([]int, len(path))
			ifaceMap := getTunnelIfaceMap(tun.ID) // 出站(接口)IP
			bindMap := getTunnelBindMap(tun.ID)   // 入站(监听)IP
			for i := range path {
				// 计算上一跳出口IP：entry 使用 inNode 的接口IP；中间节点使用前一个 mid 的接口IP
				var prevID int64
				if i == 0 {
					prevID = tun.InNodeID
				} else {
					prevID = path[i-1]
				}
				prevOut := ifaceMap[prevID]
				nextIn := bindMap[path[i]]
				overlay := isOverlayIP(prevOut) && isOverlayIP(nextIn)
				var n model.Node
				_ = dbpkg.DB.First(&n, path[i]).Error
				// 分配端口：
				// - overlay 相邻：范围固定 10000-65535，按最小可用优先
				// - 否则：遵循节点端口范围
				if overlay {
					midPorts[i] = findFreePortOnNodeAny(path[i], 10000, 10000)
				} else {
					// 受节点端口范围限制
					minP := 10000
					maxP := 65535
					if n.PortSta > 0 {
						minP = n.PortSta
					}
					if n.PortEnd > 0 {
						maxP = n.PortEnd
					}
					midPorts[i] = findFreePortOnNode(path[i], minP, minP, maxP)
				}
				if midPorts[i] == 0 {
					if overlay {
						midPorts[i] = 10000
					} else {
						minP := 10000
						if n.PortSta > 0 {
							minP = n.PortSta
						}
						midPorts[i] = minP
					}
				}
			}
			// Persist expected mid ports for strict compare
			{
				now := time.Now().UnixMilli()
				for i := 0; i < len(path); i++ {
					rec := model.ForwardMidPort{ForwardID: f.ID, Idx: i, NodeID: path[i], Port: midPorts[i], UpdatedTime: now}
					_ = dbpkg.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "forward_id"}, {Name: "idx"}}, DoUpdates: clause.Assignments(map[string]any{"node_id": rec.NodeID, "port": rec.Port, "updated_time": rec.UpdatedTime})}).Create(&rec).Error
				}
			}
			// Deploy simple TCP forward on each mid to the next hop
			for i := 0; i < len(path); i++ {
				nid := path[i]
				thisPort := midPorts[i]
				var target string
				if i < len(path)-1 {
					var nx model.Node
					if err := dbpkg.DB.First(&nx, path[i+1]).Error; err != nil {
						continue
					}
					// 优先使用下一跳的入站绑定IP（用户在隧道编辑中选的入口IP），否则回退到节点信息（preferIPv4）
					nextBind := bindMap[path[i+1]]
					host := nextBind
					if host == "" {
						host = preferIPv4(nx)
					}
					if host == "" {
						host = firstIPAny(nx)
					}
					target = safeHostPort(host, midPorts[i+1])
				} else {
					// last mid forwards to exit relay (gRPC) address directly
					target = exitAddr
				}
				midName := fmt.Sprintf("%s_mid_%d", name, i)
				var iface *string
				if ip, ok := ifaceMap[nid]; ok && ip != "" {
					tmp := ip
					iface = &tmp
				}
				// bind IP (in IP) for mids and exit (nid != entry)
				addrStr := fmt.Sprintf(":%d", thisPort)
				/*if bindIP, ok := bindMap[nid]; ok && bindIP != "" {
					addrStr = safeHostPort(bindIP, thisPort)
				}*/
				svc := map[string]any{
					"name":      midName,
					"addr":      addrStr,
					"listener":  map[string]any{"type": "tcp"},
					"handler":   map[string]any{"type": "forward"},
					"forwarder": map[string]any{"nodes": []map[string]any{{"name": "target", "addr": target}}},
					"metadata":  map[string]any{"managedBy": "network-panel", "enableStats": true, "observer.period": "5s", "observer.resetTraffic": false},
				}
				if iface != nil && *iface != "" {
					svc["metadata"].(map[string]any)["interface"] = *iface
				}
				_ = sendWSCommand(nid, "AddService", expandRUDP([]map[string]any{svc}))
				if b, err := json.Marshal(svc); err == nil {
					s := string(b)
					_ = dbpkg.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: nid, Cmd: "ForwardAddService", RequestID: opId, Success: 1, Message: fmt.Sprintf("create mid svc %s port=%d", midName, thisPort), Stdout: &s}).Error
				}
			}
			// Entry node: forward handler + chain to first mid address using dialer=grpc（负载将被各中间节点逐跳TCP转发）
			var first model.Node
			if err := dbpkg.DB.First(&first, path[0]).Error; err == nil {
				inSvc := map[string]any{
					"name":     name,
					"addr":     fmt.Sprintf(":%d", f.InPort),
					"listener": map[string]any{"type": "tcp"},
					"handler":  map[string]any{"type": "forward", "chain": "chain_" + name},
					"metadata": map[string]any{"managedBy": "network-panel", "enableStats": true, "observer.period": "5s", "observer.resetTraffic": false},
				}
				if obsName, spec := buildObserverPluginSpec(tun.InNodeID, name); obsName != "" && spec != nil {
					inSvc["observer"] = obsName
					inSvc["_observers"] = []any{spec}
				}
				attachLimiter(inSvc, tun.InNodeID)
				// attach interface for entry if configured
				if ip, ok := ifaceMap[tun.InNodeID]; ok && ip != "" {
					if meta, ok2 := inSvc["metadata"].(map[string]any); ok2 {
						meta["interface"] = ip
					}
				}
				chainName := "chain_" + name
				hopName := "hop_" + name
				node := map[string]any{
					"name": "node-" + name,
					// Important: dial to FIRST MID address; prefer该节点的入站绑定IP（用户选择的入口IP）
					"addr": safeHostPort(func() string {
						if v := bindMap[path[0]]; v != "" {
							return v
						}
						return preferIPv4(first)
					}(), midPorts[0]),
					"connector": map[string]any{"type": "relay", "auth": map[string]any{"username": user, "password": pass}},
					"dialer":    map[string]any{"type": "grpc"},
				}
				inSvc["_chains"] = []any{map[string]any{"name": chainName, "metadata": map[string]any{"managedBy": "network-panel", "enableStats": true, "observer.period": "5s", "observer.resetTraffic": false}, "hops": []any{map[string]any{"name": hopName, "nodes": []any{node}}}}}
				// forwarder 目标为多个远程地址（支持逗号分隔）
				nodes := []map[string]any{}
				for i, a := range parseRemoteAddrs(f.RemoteAddr) {
					nodes = append(nodes, map[string]any{"name": fmt.Sprintf("target_%d", i), "addr": a})
				}
				if len(nodes) == 0 {
					nodes = []map[string]any{{"name": "target_0", "addr": firstTargetHost(f.RemoteAddr)}}
				}
				inSvc["forwarder"] = map[string]any{"nodes": nodes}
				_ = sendWSCommand(tun.InNodeID, "AddService", expandRUDP([]map[string]any{inSvc}))
				if b, err := json.Marshal(inSvc); err == nil {
					s := string(b)
					_ = dbpkg.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: tun.InNodeID, Cmd: "ForwardAddService", RequestID: opId, Success: 1, Message: "create entry svc", Stdout: &s}).Error
				}
			}
		} else {
			// Original single-hop: in -> exit relay
			inSvc := map[string]any{
				"name":     name,
				"addr":     fmt.Sprintf(":%d", f.InPort),
				"listener": map[string]any{"type": "tcp"},
				"handler":  map[string]any{"type": "forward", "chain": "chain_" + name},
				"metadata": map[string]any{"managedBy": "network-panel", "enableStats": true, "observer.period": "5s", "observer.resetTraffic": false},
			}
			if obsName, spec := buildObserverPluginSpec(tun.InNodeID, name); obsName != "" && spec != nil {
				inSvc["observer"] = obsName
				inSvc["_observers"] = []any{spec}
			}
			attachLimiter(inSvc, tun.InNodeID)
			chainName := "chain_" + name
			hopName := "hop_" + name
			node := map[string]any{
				"name": "node-" + name,
				// 出口优先使用监听IP（组网场景用 10.126.126.X），否则回退到节点/隧道出口IP
				"addr":      exitAddr,
				"connector": map[string]any{"type": "relay", "auth": map[string]any{"username": user, "password": pass}},
				"dialer":    map[string]any{"type": "grpc"},
			}
			inSvc["_chains"] = []any{map[string]any{"name": chainName, "metadata": map[string]any{"managedBy": "network-panel", "enableStats": true, "observer.period": "5s", "observer.resetTraffic": false}, "hops": []any{map[string]any{"name": hopName, "nodes": []any{node}}}}}
			// forwarder 目标为多个远程地址（支持逗号分隔）
			nodes := []map[string]any{}
			for i, a := range parseRemoteAddrs(f.RemoteAddr) {
				nodes = append(nodes, map[string]any{"name": fmt.Sprintf("target_%d", i), "addr": a})
			}
			if len(nodes) == 0 {
				nodes = []map[string]any{{"name": "target_0", "addr": firstTargetHost(f.RemoteAddr)}}
			}
			inSvc["forwarder"] = map[string]any{"nodes": nodes}
			_ = sendWSCommand(tun.InNodeID, "AddService", expandRUDP([]map[string]any{inSvc}))
			if b, err := json.Marshal(inSvc); err == nil {
				s := string(b)
				_ = dbpkg.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: tun.InNodeID, Cmd: "ForwardAddService", RequestID: opId, Success: 1, Message: "create entry svc", Stdout: &s}).Error
			}
			// restart in & out to ensure effect
			// 使用 Web API 动态变更，无需重启
		}
		// 不重启，配置已生效
	} else {
		// port-forward: support multi-level path
		path := getTunnelPathNodes(tun.ID)
		ifaceMap := getTunnelIfaceMap(tun.ID)
		bindMap := getTunnelBindMap(tun.ID)
		if len(path) == 0 {
			// single hop: in-node listens on inPort and forwards to remoteAddr
			var iface *string
			// entry iface mapping takes precedence, then forward/interfaceName, then tunnel.interfaceName
			if ip, ok := ifaceMap[tun.InNodeID]; ok && ip != "" {
				tmp := ip
				iface = &tmp
			} else {
				iface = preferIface(f.InterfaceName, tun.InterfaceName)
			}
			svc := buildServiceConfig(name, f.InPort, f.RemoteAddr, iface)
			if obsName, spec := buildObserverPluginSpec(tun.InNodeID, name); obsName != "" && spec != nil {
				svc["observer"] = obsName
				svc["_observers"] = []any{spec}
			}
			attachLimiter(svc, tun.InNodeID)
			_ = sendWSCommand(tun.InNodeID, "AddService", expandRUDP([]map[string]any{svc}))
			// 不重启，配置已生效
		} else {
			// chain: [inNode -> mid1 -> mid2 -> ... -> last]
			hops := append([]int64{tun.InNodeID}, path...)
			hopPorts := make([]int, len(hops))
			// entry uses requested inPort
			hopPorts[0] = f.InPort
			for i := 1; i < len(hops); i++ {
				prevID := hops[i-1]
				curID := hops[i]
				prevOut := ifaceMap[prevID]
				curIn := bindMap[curID]
				overlay := isOverlayIP(prevOut) && isOverlayIP(curIn)
				var n model.Node
				_ = dbpkg.DB.First(&n, curID).Error
				if overlay {
					// 固定范围 10000-65535，自小向上找空闲
					p := findFreePortOnNodeAny(curID, 10000, 10000)
					if p == 0 {
						p = 10000
					}
					hopPorts[i] = p
				} else {
					minP, maxP := 10000, 65535
					if n.PortSta > 0 {
						minP = n.PortSta
					}
					if n.PortEnd > 0 {
						maxP = n.PortEnd
					}
					p := findFreePortOnNode(curID, minP, minP, maxP)
					if p == 0 {
						p = minP
					}
					hopPorts[i] = p
				}
				_ = dbpkg.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: curID, Cmd: "ForwardPortPick", RequestID: opId, Success: 1, Message: fmt.Sprintf("hop port=%d (%s)", hopPorts[i], ifThen(overlay, "overlay", "range"))}).Error
			}
			// deploy services per hop
			for i := 0; i < len(hops); i++ {
				nodeID := hops[i]
				listenPort := hopPorts[i]
				var target string
				if i < len(hops)-1 {
					var n model.Node
					if err := dbpkg.DB.First(&n, hops[i+1]).Error; err != nil {
						continue
					}
					host := bindMap[hops[i+1]]
					if host == "" {
						host = n.ServerIP
					}
					target = safeHostPort(host, hopPorts[i+1])
				} else {
					target = f.RemoteAddr
				}
				var iface *string
				if ip, ok := ifaceMap[nodeID]; ok && ip != "" {
					tmp := ip
					iface = &tmp
				} else {
					iface = preferIface(f.InterfaceName, tun.InterfaceName)
				}
				svc := buildServiceConfig(name, listenPort, target, iface)
				if i == 0 {
					attachLimiter(svc, nodeID)
				}
				_ = sendWSCommand(nodeID, "AddService", expandRUDP([]map[string]any{svc}))
				if b, err := json.Marshal(svc); err == nil {
					s := string(b)
					_ = dbpkg.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: nodeID, Cmd: "ForwardAddService", RequestID: opId, Success: 1, Message: fmt.Sprintf("create hop svc port=%d", listenPort), Stdout: &s}).Error
				}
			}
			// restart gost on all hops
			nodesToRestart := make(map[int64]struct{})
			for _, nid := range hops {
				nodesToRestart[nid] = struct{}{}
			}
			for nid := range nodesToRestart {
				if nid > 0 {
					// 不重启
				}
			}
		}
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{"requestId": opId}))
}

// POST /api/v1/forward/list
func ForwardList(c *gin.Context) {
	roleInf, _ := c.Get("role_id")
	uidInf, _ := c.Get("user_id")
	var res []struct {
		model.Forward
		TunnelName string `json:"tunnelName"`
		InIp       string `json:"inIp"`
	}
	q := dbpkg.DB.Table("forward f").Select("f.*, t.name as tunnel_name, t.in_ip as in_ip").Joins("left join tunnel t on t.id = f.tunnel_id")
	if roleInf != 0 {
		q = q.Where("f.user_id = ?", uidInf)
	}
	q.Scan(&res)
	c.JSON(http.StatusOK, response.Ok(res))
}

// POST /api/v1/forward/update
func ForwardUpdate(c *gin.Context) {
	var req dto.ForwardUpdateDto
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var f model.Forward
	if err := dbpkg.DB.First(&f, req.ID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("转发不存在"))
		return
	}
	// permission: non-admin can only update own forward
	if roleInf, ok := c.Get("role_id"); ok && roleInf != 0 {
		if uidInf, ok2 := c.Get("user_id"); ok2 {
			if f.UserID != uidInf.(int64) {
				c.JSON(http.StatusForbidden, response.ErrMsg("无权限"))
				return
			}
		}
	}
	// ensure tunnel exists
	var tun model.Tunnel
	if err := dbpkg.DB.First(&tun, req.TunnelID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("隧道不存在"))
		return
	}
	if req.Name != "" {
		f.Name = req.Name
	}
	if req.TunnelID != 0 {
		f.TunnelID = req.TunnelID
	}
	if req.InPort != nil {
		// enforce entry node port range; if out of range, try to pick a free one within
		var node model.Node
		_ = dbpkg.DB.First(&node, tun.InNodeID).Error
		minP := 10000
		maxP := 65535
		if node.PortSta > 0 {
			minP = node.PortSta
		}
		if node.PortEnd > 0 {
			maxP = node.PortEnd
		}
		v := *req.InPort
		if v < minP || v > maxP {
			v = findFreePortOnNode(tun.InNodeID, 0, minP, maxP)
			if v == 0 {
				c.JSON(http.StatusOK, response.ErrMsg("入口端口超出范围且无法分配可用端口"))
				return
			}
		}
		f.InPort = v
	}
	if req.RemoteAddr != "" {
		f.RemoteAddr = req.RemoteAddr
	}
	f.InterfaceName, f.Strategy = req.InterfaceName, req.Strategy
	f.UpdatedTime = time.Now().UnixMilli()
	if err := dbpkg.DB.Save(&f).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("端口转发更新失败"))
		return
	}
	// push update
	opId := RandUUID()
	name := buildServiceName(f.ID, f.UserID, f.TunnelID)
	if tun.Type == 2 {
		// ensure outPort exists as TLS tunnel port
		if f.OutPort == nil {
			if op := firstFreePortOut(tun, f.ID); op != 0 {
				f.OutPort = &op
				dbpkg.DB.Model(&model.Forward{}).Where("id=?", f.ID).Update("out_port", op)
			} else {
				c.JSON(http.StatusOK, response.ErrMsg("隧道出口端口已满，无法分配新端口"))
				return
			}
		}
		// update out-node gRPC relay service
		// apply bind IP (in IP) for exit if configured
		bindMap := getTunnelBindMap(tun.ID)
		addrStr := fmt.Sprintf(":%d", *f.OutPort)
		if ip, ok := bindMap[outNodeIDOr0(tun)]; ok && ip != "" {
			addrStr = safeHostPort(ip, *f.OutPort)
		}
		outSvc := map[string]any{
			"name":     name,
			"addr":     addrStr,
			"listener": map[string]any{"type": "grpc"},
			"handler":  map[string]any{"type": "relay", "auth": map[string]any{"username": fmt.Sprintf("u-%d", f.ID), "password": util.MD5(fmt.Sprintf("%d:%d", f.ID, f.CreatedTime))[:16]}},
			"metadata": map[string]any{"managedBy": "network-panel", "enableStats": true, "observer.period": "5s", "observer.resetTraffic": false},
		}
		_ = sendWSCommand(outNodeIDOr0(tun), "AddService", expandRUDP([]map[string]any{outSvc}))
		if b, err := json.Marshal(outSvc); err == nil {
			s := string(b)
			_ = dbpkg.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: outNodeIDOr0(tun), Cmd: "ForwardAddService", RequestID: opId, Success: 1, Message: "update out svc", Stdout: &s}).Error
		}

		// multi-hop mids (update or create)
		path := getTunnelPathNodes(tun.ID)
		ifaceMap := getTunnelIfaceMap(tun.ID)
		var entryTarget string
		if len(path) > 0 {
			// allocate ports for mids per overlay rule
			midPorts := make([]int, len(path))
			for i := range path {
				var prevID int64
				if i == 0 {
					prevID = tun.InNodeID
				} else {
					prevID = path[i-1]
				}
				prevOut := ifaceMap[prevID]
				nextIn := bindMap[path[i]]
				overlay := isOverlayIP(prevOut) && isOverlayIP(nextIn)
				var n model.Node
				_ = dbpkg.DB.First(&n, path[i]).Error
				if overlay {
					p := findFreePortOnNodeAny(path[i], 10000, 10000)
					if p == 0 {
						p = 10000
					}
					midPorts[i] = p
				} else {
					minP, maxP := 10000, 65535
					if n.PortSta > 0 {
						minP = n.PortSta
					}
					if n.PortEnd > 0 {
						maxP = n.PortEnd
					}
					p := findFreePortOnNode(path[i], minP, minP, maxP)
					if p == 0 {
						p = minP
					}
					midPorts[i] = p
				}
				_ = dbpkg.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: path[i], Cmd: "ForwardPortPick", RequestID: opId, Success: 1, Message: fmt.Sprintf("mid port=%d (%s)", midPorts[i], ifThen(overlay, "overlay", "range"))}).Error
			}
			// Persist expected mid ports for strict compare (update)
			{
				now := time.Now().UnixMilli()
				for i := 0; i < len(path); i++ {
					rec := model.ForwardMidPort{ForwardID: f.ID, Idx: i, NodeID: path[i], Port: midPorts[i], UpdatedTime: now}
					_ = dbpkg.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "forward_id"}, {Name: "idx"}}, DoUpdates: clause.Assignments(map[string]any{"node_id": rec.NodeID, "port": rec.Port, "updated_time": rec.UpdatedTime})}).Create(&rec).Error
				}
			}
			// update services on mids
			for i := 0; i < len(path); i++ {
				nid := path[i]
				thisPort := midPorts[i]
				var target string
				if i < len(path)-1 {
					var nx model.Node
					if err := dbpkg.DB.First(&nx, path[i+1]).Error; err != nil {
						continue
					}
					host := bindMap[path[i+1]]
					if host == "" {
						host = preferIPv4(nx)
					}
					if host == "" {
						host = firstIPAny(nx)
					}
					target = safeHostPort(host, midPorts[i+1])
				} else {
					// last mid forwards directly to exit relay (gRPC) on out node
					// Use exit bind IP if provided; otherwise fall back to tunnel/Node ServerIP
					exHost := ""
					if ip, ok := bindMap[outNodeIDOr0(tun)]; ok && ip != "" {
						exHost = ip
					} else {
						exHost = getOutNodeIP(tun)
					}
					target = safeHostPort(exHost, *f.OutPort)
				}
				midName := fmt.Sprintf("%s_mid_%d", buildServiceName(f.ID, f.UserID, f.TunnelID), i)
				var iface *string
				if ip, ok := ifaceMap[nid]; ok && ip != "" {
					tmp := ip
					iface = &tmp
				}
				// listener addr with bind if provided
				listen := fmt.Sprintf(":%d", thisPort)
				if bindIP, ok := bindMap[nid]; ok && bindIP != "" {
					listen = safeHostPort(bindIP, thisPort)
				}
				svc := map[string]any{
					"name":      midName,
					"addr":      listen,
					"listener":  map[string]any{"type": "tcp"},
					"handler":   map[string]any{"type": "forward"},
					"forwarder": map[string]any{"nodes": []map[string]any{{"name": "target", "addr": target}}},
					"metadata":  map[string]any{"managedBy": "network-panel", "enableStats": true, "observer.period": "5s", "observer.resetTraffic": false},
				}
				if iface != nil && *iface != "" {
					svc["metadata"].(map[string]any)["interface"] = *iface
				}
				_ = sendWSCommand(nid, "AddService", expandRUDP([]map[string]any{svc}))
				if b, err := json.Marshal(svc); err == nil {
					s := string(b)
					_ = dbpkg.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: nid, Cmd: "ForwardAddService", RequestID: opId, Success: 1, Message: fmt.Sprintf("update mid svc %s port=%d", midName, thisPort), Stdout: &s}).Error
				}
			}
			// compute entry target to first mid
			host0 := bindMap[path[0]]
			if host0 == "" {
				var firstN model.Node
				_ = dbpkg.DB.First(&firstN, path[0]).Error
				host0 = preferIPv4(firstN)
			}
			entryTarget = safeHostPort(host0, midPorts[0])
		}
		if b, err := json.Marshal(outSvc); err == nil {
			s := string(b)
			_ = dbpkg.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: outNodeIDOr0(tun), Cmd: "ForwardAddService", RequestID: opId, Success: 1, Message: "update out svc", Stdout: &s}).Error
		}

		// update in-node entry service with chain(dialer=grpc, connector=relay) and forwarder target (remote)
		// 出口地址优先使用出口节点的监听IP(隧道编辑中的 inIp/bind)，否则回退到隧道/节点出口IP
		outIP := getOutNodeIP(tun)
		if ipBind, ok := bindMap[outNodeIDOr0(tun)]; ok && ipBind != "" {
			outIP = ipBind
		}
		exitAddr := safeHostPort(outIP, *f.OutPort)
		// 入口 forwarder 统一指向远程地址（取第一项），附带入口节点 interface（若配置）
		ifaceMap = getTunnelIfaceMap(tun.ID)
		var inIface *string
		if ip, ok := ifaceMap[tun.InNodeID]; ok && ip != "" {
			tmp := ip
			inIface = &tmp
		}
		inSvc := buildServiceConfig(name, f.InPort, f.RemoteAddr, inIface)
		if obsName, spec := buildObserverPluginSpec(tun.InNodeID, name); obsName != "" && spec != nil {
			inSvc["observer"] = obsName
			inSvc["_observers"] = []any{spec}
		}
		attachLimiter(inSvc, tun.InNodeID)
		chainName := "chain_" + name
		hopName := "hop_" + name
		// ensure handler is forward and attach chain
		if h, ok := inSvc["handler"].(map[string]any); ok {
			h["type"] = "forward"
			h["chain"] = chainName
		} else {
			inSvc["handler"] = map[string]any{"type": "forward", "chain": chainName}
		}
		user := fmt.Sprintf("u-%d", f.ID)
		pass := util.MD5(fmt.Sprintf("%d:%d", f.ID, f.CreatedTime))[:16]
		// entry chain target: if multi-hop, dial first mid; else dial exit relay
		if len(path) == 0 {
			entryTarget = exitAddr
		}
		node := map[string]any{"name": "node-" + name, "addr": entryTarget, "connector": map[string]any{"type": "relay", "auth": map[string]any{"username": user, "password": pass}}, "dialer": map[string]any{"type": "grpc"}}
		inSvc["_chains"] = []any{map[string]any{"name": chainName, "hops": []any{map[string]any{"name": hopName, "nodes": []any{node}}}}}
		_ = sendWSCommand(tun.InNodeID, "AddService", expandRUDP([]map[string]any{inSvc}))
		if b, err := json.Marshal(inSvc); err == nil {
			s := string(b)
			_ = dbpkg.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: tun.InNodeID, Cmd: "ForwardUpdateService", RequestID: opId, Success: 1, Message: "update entry svc", Stdout: &s}).Error
		}
		// 强制重启入口与出口节点的 gost，确保更新立即生效
		// 不重启，配置已生效
	} else {
		// port-forward (type=1): support multi-level path similar to create
		path := getTunnelPathNodes(tun.ID)
		ifaceMap := getTunnelIfaceMap(tun.ID)
		bindMap := getTunnelBindMap(tun.ID)
		if len(path) == 0 {
			// single hop: entry forwards directly to remoteAddr
			var iface *string
			if ip, ok := ifaceMap[tun.InNodeID]; ok && ip != "" {
				tmp := ip
				iface = &tmp
			} else {
				iface = preferIface(f.InterfaceName, tun.InterfaceName)
			}
			svc := buildServiceConfig(name, f.InPort, f.RemoteAddr, iface)
			if obsName, spec := buildObserverPluginSpec(tun.InNodeID, name); obsName != "" && spec != nil {
				svc["observer"] = obsName
				svc["_observers"] = []any{spec}
			}
			attachLimiter(svc, tun.InNodeID)
			_ = sendWSCommand(tun.InNodeID, "AddService", expandRUDP([]map[string]any{svc}))
			if b, err := json.Marshal(svc); err == nil {
				s := string(b)
				_ = dbpkg.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: tun.InNodeID, Cmd: "ForwardUpdateService", RequestID: opId, Success: 1, Message: "update entry svc (type1-single)", Stdout: &s}).Error
			}
			// 不重启
		} else {
			// chain: [inNode -> mid1 -> ... -> last]; entry uses f.InPort
			hops := append([]int64{tun.InNodeID}, path...)
			hopPorts := make([]int, len(hops))
			hopPorts[0] = f.InPort
			for i := 1; i < len(hops); i++ {
				prevID := hops[i-1]
				curID := hops[i]
				prevOut := ifaceMap[prevID]
				curIn := bindMap[curID]
				overlay := isOverlayIP(prevOut) && isOverlayIP(curIn)
				var n model.Node
				_ = dbpkg.DB.First(&n, curID).Error
				if overlay {
					p := findFreePortOnNodeAny(curID, 10000, 10000)
					if p == 0 {
						p = 10000
					}
					hopPorts[i] = p
				} else {
					minP, maxP := 10000, 65535
					if n.PortSta > 0 {
						minP = n.PortSta
					}
					if n.PortEnd > 0 {
						maxP = n.PortEnd
					}
					p := findFreePortOnNode(curID, minP, minP, maxP)
					if p == 0 {
						p = minP
					}
					hopPorts[i] = p
				}
				_ = dbpkg.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: curID, Cmd: "ForwardPortPick", RequestID: opId, Success: 1, Message: fmt.Sprintf("hop port=%d", hopPorts[i])}).Error
			}
			// deploy services per hop (upsert)
			for i := 0; i < len(hops); i++ {
				nodeID := hops[i]
				listenPort := hopPorts[i]
				var target string
				if i < len(hops)-1 {
					var n model.Node
					if err := dbpkg.DB.First(&n, hops[i+1]).Error; err != nil {
						continue
					}
					host := bindMap[hops[i+1]]
					if host == "" {
						host = preferIPv4(n)
					}
					if host == "" {
						host = firstIPAny(n)
					}
					target = safeHostPort(host, hopPorts[i+1])
				} else {
					target = firstTargetHost(f.RemoteAddr)
				}
				var iface *string
				if ip, ok := ifaceMap[nodeID]; ok && ip != "" {
					tmp := ip
					iface = &tmp
				} else {
					iface = preferIface(f.InterfaceName, tun.InterfaceName)
				}
				svc := buildServiceConfig(name, listenPort, target, iface)
				if i == 0 {
					attachLimiter(svc, nodeID)
				}
				_ = sendWSCommand(nodeID, "AddService", expandRUDP([]map[string]any{svc}))
				if b, err := json.Marshal(svc); err == nil {
					s := string(b)
					_ = dbpkg.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: nodeID, Cmd: "ForwardAddService", RequestID: opId, Success: 1, Message: fmt.Sprintf("update hop svc port=%d", listenPort), Stdout: &s}).Error
				}
			}
			// restart gost on all hops to apply changes
			nodesToRestart := make(map[int64]struct{})
			for _, nid := range hops {
				nodesToRestart[nid] = struct{}{}
			}
			for nid := range nodesToRestart {
				if nid > 0 {
					// 不重启
				}
			}
		}
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{"msg": "端口转发更新成功", "requestId": opId}))
}

// POST /api/v1/forward/delete
func ForwardDelete(c *gin.Context) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	// fetch forward first
	var f model.Forward
	_ = dbpkg.DB.First(&f, p.ID).Error
	var tun model.Tunnel
	_ = dbpkg.DB.First(&tun, f.TunnelID).Error
	name := buildServiceName(f.ID, f.UserID, f.TunnelID)
	if tun.Type == 2 && f.OutPort != nil {
		// 删除入口与出口上的主服务
		_ = sendWSCommand(tun.InNodeID, "DeleteService", map[string]any{"services": expandNamesWithRUDP([]string{name})})
		_ = sendWSCommand(outNodeIDOr0(tun), "DeleteService", map[string]any{"services": expandNamesWithRUDP([]string{name})})
		// 删除多级路径的中间节点 mid 服务（name_mid_i）
		path := getTunnelPathNodes(tun.ID)
		for i := 0; i < len(path); i++ {
			midName := fmt.Sprintf("%s_mid_%d", name, i)
			_ = sendWSCommand(path[i], "DeleteService", map[string]any{"services": expandNamesWithRUDP([]string{midName})})
		}
	} else {
		// 端口转发：删除入口上的服务
		_ = sendWSCommand(tun.InNodeID, "DeleteService", map[string]any{"services": expandNamesWithRUDP([]string{name})})
		// 若端口转发也采用了多级路径（各 hop 使用相同 name），尝试在中间节点删除同名服务
		path := getTunnelPathNodes(tun.ID)
		for _, nid := range path {
			_ = sendWSCommand(nid, "DeleteService", map[string]any{"services": expandNamesWithRUDP([]string{name})})
		}
	}
	// cleanup expected mid ports
	_ = dbpkg.DB.Where("forward_id = ?", p.ID).Delete(&model.ForwardMidPort{}).Error
	if err := dbpkg.DB.Delete(&model.Forward{}, p.ID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("端口转发删除失败"))
		return
	}
	c.JSON(http.StatusOK, response.OkMsg("端口转发删除成功"))
}

// POST /api/v1/forward/force-delete
func ForwardForceDelete(c *gin.Context) { ForwardDelete(c) }

// POST /api/v1/forward/pause
func ForwardPause(c *gin.Context) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var f model.Forward
	if err := dbpkg.DB.First(&f, p.ID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("转发不存在"))
		return
	}
	// set status
	dbpkg.DB.Model(&model.Forward{}).Where("id = ?", p.ID).Update("status", 0)
	// send pause to node(s)
	var t model.Tunnel
	if err := dbpkg.DB.First(&t, f.TunnelID).Error; err == nil {
		name := buildServiceName(f.ID, f.UserID, f.TunnelID)
		_ = sendWSCommand(t.InNodeID, "PauseService", map[string]interface{}{"services": expandNamesWithRUDP([]string{name})})
		if t.Type == 2 {
			_ = sendWSCommand(outNodeIDOr0(t), "PauseService", map[string]interface{}{"services": expandNamesWithRUDP([]string{name})})
		}
	}
	c.JSON(http.StatusOK, response.OkNoData())
}

// POST /api/v1/forward/resume
func ForwardResume(c *gin.Context) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var f model.Forward
	if err := dbpkg.DB.First(&f, p.ID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("转发不存在"))
		return
	}
	// set status
	dbpkg.DB.Model(&model.Forward{}).Where("id = ?", p.ID).Update("status", 1)
	// send resume to node(s)
	var t model.Tunnel
	if err := dbpkg.DB.First(&t, f.TunnelID).Error; err == nil {
		name := buildServiceName(f.ID, f.UserID, f.TunnelID)
		_ = sendWSCommand(t.InNodeID, "ResumeService", map[string]interface{}{"services": expandNamesWithRUDP([]string{name})})
		if t.Type == 2 {
			_ = sendWSCommand(outNodeIDOr0(t), "ResumeService", map[string]interface{}{"services": expandNamesWithRUDP([]string{name})})
		}
	}
	c.JSON(http.StatusOK, response.OkNoData())
}

// POST /api/v1/forward/diagnose
func ForwardDiagnose(c *gin.Context) {
	var p struct {
		ForwardID int64 `json:"forwardId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var f model.Forward
	if err := dbpkg.DB.First(&f, p.ForwardID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("转发不存在"))
		return
	}
	// auth: admin or owner or user has tunnel permission
	if roleInf, ok := c.Get("role_id"); ok {
		if role, _ := roleInf.(int); role != 0 {
			if uidInf, ok2 := c.Get("user_id"); ok2 {
				uid := uidInf.(int64)
				if f.UserID != uid {
					var utCnt int64
					dbpkg.DB.Model(&model.UserTunnel{}).Where("user_id=? and tunnel_id=?", uid, f.TunnelID).Count(&utCnt)
					if utCnt == 0 {
						c.JSON(http.StatusForbidden, response.ErrMsg("权限不足"))
						return
					}
				}
			}
		}
	}
	var t model.Tunnel
	_ = dbpkg.DB.First(&t, f.TunnelID).Error
	var inNode model.Node
	_ = dbpkg.DB.First(&inNode, t.InNodeID).Error
	var outNode model.Node
	if t.Type == 2 && t.OutNodeID != nil {
		_ = dbpkg.DB.First(&outNode, *t.OutNodeID).Error
	}

	results := make([]map[string]interface{}, 0, 6)
	// 入口节点连通性
	results = append(results, map[string]interface{}{
		"success":     inNode.Status != nil && *inNode.Status == 1,
		"description": "入口节点连通性",
		"nodeName":    inNode.Name,
		"nodeId":      inNode.ID,
		"targetIp":    t.InIP,
		"targetPort":  f.InPort,
		"message":     ifThen(inNode.Status != nil && *inNode.Status == 1, "节点在线", "节点离线"),
	})

	// 入口端口范围校验
	portOK := false
	var portMsg string
	var node model.Node
	if err := dbpkg.DB.First(&node, t.InNodeID).Error; err == nil {
		portOK = f.InPort >= node.PortSta && f.InPort <= node.PortEnd
		if portOK {
			portMsg = "端口在节点可用范围内"
		} else {
			portMsg = "端口不在节点可用范围内"
		}
	} else {
		portMsg = "无法获取入口节点信息"
	}
	results = append(results, map[string]interface{}{
		"success":     portOK,
		"description": "入口端口校验",
		"nodeName":    inNode.Name,
		"nodeId":      inNode.ID,
		"targetIp":    t.InIP,
		"targetPort":  f.InPort,
		"message":     portMsg,
	})

	// 远端地址格式校验 + 节点侧真实诊断（对每个远端地址逐一测试）
	addrs := parseRemoteAddrs(f.RemoteAddr)
	if len(addrs) == 0 {
		addrs = []string{firstTargetHost(f.RemoteAddr)}
	}
	// 选择执行诊断的节点：端口转发在入口节点执行，隧道转发在出口节点执行
	var runNode model.Node
	var runNodeID int64
	if t.Type == 2 {
		runNode, runNodeID = outNode, outNode.ID
	} else {
		runNode, runNodeID = inNode, inNode.ID
	}
	for _, hp := range addrs {
		host, port := splitHostPortSafe(hp)
		okFmt := host != "" && port > 0
		results = append(results, map[string]interface{}{
			"success":     okFmt,
			"description": "远端地址校验",
			"nodeName":    ifThen(t.Type == 2, outNode.Name, inNode.Name),
			"nodeId":      ifThen[int64](t.Type == 2, outNode.ID, inNode.ID),
			"targetIp":    host,
			"targetPort":  port,
			"message":     ifThen(okFmt, "地址格式正确", "远端地址格式无效，需为 host:port 或多地址以逗号分隔"),
		})
		if okFmt {
			avg, loss, ok, msg, rid := diagnoseFromNodeCtx(runNodeID, host, port, 3, 1500, map[string]interface{}{"src": "forward", "step": "nodeRemote", "forwardId": f.ID})
			results = append(results, map[string]interface{}{
				"success":     ok,
				"description": ifThen(t.Type == 2, "出口节点到远端连通性", "入口节点到远端连通性"),
				"nodeName":    runNode.Name,
				"nodeId":      runNodeID,
				"targetIp":    host,
				"targetPort":  port,
				"averageTime": avg,
				"packetLoss":  loss,
				"message":     msg,
				"reqId":       rid,
			})
		}
	}

	// 诊断日志：按节点顺序输出服务清单与状态
	{
		name := buildServiceName(f.ID, f.UserID, f.TunnelID)
		// 构建 hops 顺序：端口转发=入口+中继；隧道转发=入口+中继+出口
		path := getTunnelPathNodes(t.ID)
		hops := make([]int64, 0, 2+len(path))
		hops = append(hops, t.InNodeID)
		hops = append(hops, path...)
		if t.Type == 2 && t.OutNodeID != nil {
			hops = append(hops, *t.OutNodeID)
		}
		makeRole := func(idx int) string {
			if idx == 0 {
				return "entry"
			}
			if t.Type == 2 && idx == len(hops)-1 {
				return "exit"
			}
			return "mid"
		}
		// 按节点收集
		hopDetails := make([]map[string]interface{}, 0, len(hops))
		for i, nid := range hops {
			var n model.Node
			_ = dbpkg.DB.First(&n, nid).Error
			role := makeRole(i)
			// 读取该节点上与该 forward 相关的服务配置
			services := queryNodeServicesRaw(nid)
			matched := make([]map[string]interface{}, 0)
			for _, s := range services {
				// 过滤：服务名等于 name 或以 name_mid_ 前缀（隧道中继）
				if v, ok := s["name"].(string); ok {
					if v == name || strings.HasPrefix(v, name+"_mid_") {
						// 提取状态与范围
						addr, _ := s["addr"].(string)
						port := parsePort(addr)
						listening := false
						if b, ok2 := s["listening"].(bool); ok2 {
							listening = b
						}
						minP, maxP := 0, 0
						inRange := true
						if n.PortSta > 0 && n.PortEnd > 0 {
							minP, maxP = n.PortSta, n.PortEnd
							inRange = (port >= minP && port <= maxP)
						}
						msg := ""
						if !listening {
							if port == 0 {
								msg = "服务未配置有效端口或未解析到端口"
							} else {
								msg = "服务未监听端口，可能启动失败或被占用"
							}
						}
						// listener/handler 类型
						lt := ""
						if lst, ok3 := s["listener"].(map[string]any); ok3 {
							if x, ok4 := lst["type"].(string); ok4 {
								lt = x
							}
						}
						ht := ""
						if h, ok3 := s["handler"].(map[string]any); ok3 {
							if x, ok4 := h["type"].(string); ok4 {
								ht = x
							}
						}
						matched = append(matched, map[string]interface{}{
							"name":      v,
							"addr":      addr,
							"port":      port,
							"listener":  lt,
							"handler":   ht,
							"listening": listening,
							"inRange":   inRange,
							"range": func() string {
								if minP > 0 && maxP > 0 {
									return fmt.Sprintf("%d-%d", minP, maxP)
								}
								return ""
							}(),
							"message":  msg,
							"metadata": s["metadata"],
						})
					}
				}
			}
			hopDetails = append(hopDetails, map[string]interface{}{
				"nodeId":   nid,
				"nodeName": n.Name,
				"role":     role,
				"services": matched,
			})
		}
		results = append(results, map[string]interface{}{
			"success":     true,
			"description": "节点服务清单",
			"hops":        hopDetails,
		})
	}

	// 隧道转发：入口到出口连通性（从入口节点 TCP 连接出口IP:出口端口）
	if t.Type == 2 && f.OutPort != nil {
		exitIP := orString(ptrString(t.OutIP), outNode.ServerIP)
		avg3, loss3, ok3, msg3, rid3 := diagnoseFromNodeCtx(inNode.ID, exitIP, *f.OutPort, 3, 1500, map[string]interface{}{"src": "forward", "step": "entryExit", "forwardId": f.ID})
		results = append(results, map[string]interface{}{
			"success":     ok3,
			"description": "入口到出口连通性",
			"nodeName":    inNode.Name,
			"nodeId":      inNode.ID,
			"targetIp":    exitIP,
			"targetPort":  *f.OutPort,
			"averageTime": avg3,
			"packetLoss":  loss3,
			"message":     msg3,
			"reqId":       rid3,
		})
	}

	// 可选：iperf3 反向带宽测试（若节点支持且出口端已部署 iperf3 服务）
	if t.Type == 2 {
		exitIP := orString(ptrString(t.OutIP), outNode.ServerIP)
		payload := map[string]interface{}{
			"requestId": RandUUID(),
			"host":      exitIP,
			"port":      5201, // 常见 iperf3 端口
			"mode":      "iperf3",
			"reverse":   true,
			"duration":  5,
		}
		if res, ok := RequestDiagnose(inNode.ID, payload, 7*time.Second); ok {
			data, _ := res["data"].(map[string]interface{})
			success := false
			if v, ok2 := data["success"].(bool); ok2 {
				success = v
			}
			msgI := ""
			if m, ok2 := data["message"].(string); ok2 {
				msgI = m
			}
			bw := 0.0
			if b, ok2 := data["bandwidthMbps"].(float64); ok2 {
				bw = b
			}
			results = append(results, map[string]interface{}{
				"success":       success,
				"description":   "iperf3 反向带宽测试",
				"nodeName":      inNode.Name,
				"nodeId":        inNode.ID,
				"targetIp":      exitIP,
				"targetPort":    5201,
				"message":       msgI,
				"bandwidthMbps": bw,
			})
		} else {
			results = append(results, map[string]interface{}{
				"success":     false,
				"description": "iperf3 反向带宽测试",
				"nodeName":    inNode.Name,
				"nodeId":      inNode.ID,
				"targetIp":    exitIP,
				"targetPort":  5201,
				"message":     "节点未响应或未支持 iperf3 测试",
			})
		}
	}

	// 隧道转发时校验出口端口
	if t.Type == 2 {
		outPortOK := f.OutPort != nil && *f.OutPort >= outNode.PortSta && *f.OutPort <= outNode.PortEnd
		results = append(results, map[string]interface{}{
			"success":     outPortOK,
			"description": "出口端口校验",
			"nodeName":    outNode.Name,
			"nodeId":      outNode.ID,
			"targetIp":    orString(ptrString(t.OutIP), outNode.ServerIP),
			"targetPort":  ifThen(outPortOK, *f.OutPort, 0),
			"message":     ifThen(outPortOK, "端口在节点可用范围内", "出口端口未分配或超出范围"),
		})
	}

	out := map[string]interface{}{
		"forwardName": f.Name,
		"timestamp":   time.Now().UnixMilli(),
		"results":     results,
	}
	c.JSON(http.StatusOK, response.Ok(out))
}

// POST /api/v1/forward/diagnose-step {forwardId, step: entryExit|nodeRemote|iperf3|path}
func ForwardDiagnoseStep(c *gin.Context) {
	var p struct {
		ForwardID int64  `json:"forwardId" binding:"required"`
		Step      string `json:"step" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var f model.Forward
	if err := dbpkg.DB.First(&f, p.ForwardID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("转发不存在"))
		return
	}
	// auth: admin or owner or user has tunnel permission
	if roleInf, ok := c.Get("role_id"); ok {
		if role, _ := roleInf.(int); role != 0 {
			if uidInf, ok2 := c.Get("user_id"); ok2 {
				uid := uidInf.(int64)
				if f.UserID != uid {
					var utCnt int64
					dbpkg.DB.Model(&model.UserTunnel{}).Where("user_id=? and tunnel_id=?", uid, f.TunnelID).Count(&utCnt)
					if utCnt == 0 {
						c.JSON(http.StatusForbidden, response.ErrMsg("权限不足"))
						return
					}
				}
			}
		}
	}
	var t model.Tunnel
	_ = dbpkg.DB.First(&t, f.TunnelID).Error
	var inNode, outNode model.Node
	_ = dbpkg.DB.First(&inNode, t.InNodeID).Error
	if t.Type == 2 && t.OutNodeID != nil {
		_ = dbpkg.DB.First(&outNode, *t.OutNodeID).Error
	}

	outId := int64(0)
	if t.OutNodeID != nil {
		outId = *t.OutNodeID
	}
	log.Printf("API /forward/diagnose-step forwardId=%d step=%s inNode=%d outNode=%d tunnelType=%d", p.ForwardID, p.Step, t.InNodeID, outId, t.Type)
	var res map[string]interface{}
	switch p.Step {
	case "entryExit":
		if t.Type == 2 {
			// 隧道转发：入口到出口
			if f.OutPort == nil || outNode.ID == 0 {
				c.JSON(http.StatusOK, response.ErrMsg("未分配出口端口或无出口节点"))
				return
			}
			exitIP := orString(ptrString(t.OutIP), outNode.ServerIP)
			avg, loss, ok, msg, rid := diagnoseFromNodeCtx(inNode.ID, exitIP, *f.OutPort, 3, 1500, map[string]interface{}{"src": "forward", "step": "entryExit", "forwardId": f.ID})
			res = map[string]interface{}{
				"success": ok, "description": "入口到出口连通性", "nodeName": inNode.Name, "nodeId": inNode.ID,
				"targetIp": exitIP, "targetPort": *f.OutPort, "averageTime": avg, "packetLoss": loss, "message": msg, "reqId": rid,
			}
		} else {
			// 端口转发：无出口节点，直接验证入口节点到远端
			hp := firstTargetHost(f.RemoteAddr)
			host, port := splitHostPortSafe(hp)
			avg, loss, ok, msg, rid := diagnoseFromNodeCtx(inNode.ID, host, port, 3, 1500, map[string]interface{}{"src": "forward", "step": "entryRemote", "forwardId": f.ID})
			res = map[string]interface{}{
				"success": ok, "description": "入口节点到远端连通性", "nodeName": inNode.Name, "nodeId": inNode.ID,
				"targetIp": host, "targetPort": port, "averageTime": avg, "packetLoss": loss, "message": msg, "reqId": rid,
			}
		}
	case "nodeRemote":
		// 在隧道转发时从出口节点访问远端，否则从入口节点
		runNode := inNode
		runNodeID := inNode.ID
		if t.Type == 2 {
			runNode = outNode
			runNodeID = outNode.ID
		}
		hp := firstTargetHost(f.RemoteAddr)
		host, port := splitHostPortSafe(hp)
		avg, loss, ok, msg, rid := diagnoseFromNodeCtx(runNodeID, host, port, 3, 1500, map[string]interface{}{"src": "forward", "step": "nodeRemote", "forwardId": f.ID})
		res = map[string]interface{}{
			"success": ok, "description": ifThen(t.Type == 2, "出口节点到远端连通性", "入口节点到远端连通性"),
			"nodeName": runNode.Name, "nodeId": runNodeID, "targetIp": host, "targetPort": port, "averageTime": avg, "packetLoss": loss, "message": msg, "reqId": rid,
		}
	case "path":
		// 逐跳检查：端口转发型（type=1）：入口->中间节点(ICMP)，最后一跳到远端(TCP)
		//          隧道转发型（type=2）：入口->中间->出口(ICMP)，最后出口->远端(TCP)
		path := getTunnelPathNodes(t.ID)
		hops := make([]int64, 0, 2+len(path))
		hops = append(hops, t.InNodeID)
		hops = append(hops, path...)
		if t.Type == 2 && t.OutNodeID != nil {
			hops = append(hops, *t.OutNodeID)
		}
		items := make([]map[string]any, 0)
		// ICMP for node-to-node hops
		for i := 0; i+1 < len(hops); i++ {
			var srcN, dstN model.Node
			_ = dbpkg.DB.First(&srcN, hops[i]).Error
			_ = dbpkg.DB.First(&dstN, hops[i+1]).Error
			target := dstN.ServerIP
			avg, loss, ok, msg, rid := diagnosePingFromNodeCtx(srcN.ID, target, 3, 1500, map[string]interface{}{"src": "forward", "step": "path", "forwardId": f.ID, "hopIndex": i})
			items = append(items, map[string]any{
				"success": ok, "description": "逐跳连通性 (ICMP)", "nodeName": srcN.Name, "nodeId": srcN.ID,
				"targetIp": target, "averageTime": avg, "packetLoss": loss, "message": msg, "reqId": rid,
			})
		}
		// final: last hop node to EACH remote host:port via TCP
		addrs := parseRemoteAddrs(f.RemoteAddr)
		if len(addrs) == 0 {
			addrs = []string{firstTargetHost(f.RemoteAddr)}
		}
		if len(hops) > 0 {
			last := hops[len(hops)-1]
			var lastN model.Node
			_ = dbpkg.DB.First(&lastN, last).Error
			for _, hp := range addrs {
				host, port := splitHostPortSafe(hp)
				if host == "" || port <= 0 {
					continue
				}
				avg, loss, ok, msg, rid := diagnoseFromNodeCtx(last, host, port, 3, 1500, map[string]interface{}{"src": "forward", "step": "remote", "forwardId": f.ID})
				items = append(items, map[string]any{
					"success": ok, "description": "最终到远端连通性 (TCP)", "nodeName": lastN.Name, "nodeId": last,
					"targetIp": host, "targetPort": port, "averageTime": avg, "packetLoss": loss, "message": msg, "reqId": rid,
				})
			}
		}
		c.JSON(http.StatusOK, response.Ok(map[string]any{"results": items}))
		return
	case "iperf3":
		if t.Type != 2 {
			c.JSON(http.StatusOK, response.ErrMsg("仅隧道转发支持iperf3"))
			return
		}
		exitIP := orString(ptrString(t.OutIP), outNode.ServerIP)
		// 1) 让出口节点在端口范围内启动服务器
		minP, maxP := 10000, 65535
		if outNode.PortSta > 0 {
			minP = outNode.PortSta
		}
		if outNode.PortEnd > 0 {
			maxP = outNode.PortEnd
		}
		prefer := outNode.PortSta
		if prefer <= 0 {
			prefer = minP
		}
		wantedSrvPort := findFreePortOnNode(outNode.ID, prefer, minP, maxP)
		if wantedSrvPort == 0 {
			wantedSrvPort = minP
		}
		srvReq := map[string]interface{}{"requestId": RandUUID(), "mode": "iperf3", "server": true, "port": wantedSrvPort}
		srvRes, ok := RequestDiagnose(outNode.ID, srvReq, 6*time.Second)
		if !ok {
			c.JSON(http.StatusOK, response.ErrMsg("出口节点未响应iperf3服务启动"))
			return
		}
		srvPort := wantedSrvPort
		if data, _ := srvRes["data"].(map[string]interface{}); data != nil {
			if p, ok2 := toFloat(data["port"]); ok2 {
				srvPort = int(p)
			}
		}
		if srvPort < minP || srvPort > maxP {
			srvPort = wantedSrvPort
		}
		if srvPort == 0 {
			c.JSON(http.StatusOK, response.ErrMsg("iperf3服务未返回端口"))
			return
		}
		// 2) 入口节点作为客户端 -R 到出口
		cliReq := map[string]interface{}{"requestId": RandUUID(), "mode": "iperf3", "client": true, "host": exitIP, "port": srvPort, "reverse": true, "duration": 5}
		cliRes, ok := RequestDiagnose(inNode.ID, cliReq, 12*time.Second)
		if !ok {
			c.JSON(http.StatusOK, response.ErrMsg("入口节点未响应iperf3客户端"))
			return
		}
		data, _ := cliRes["data"].(map[string]interface{})
		success := false
		msgI := ""
		bw := 0.0
		if data != nil {
			if v, ok2 := data["success"].(bool); ok2 {
				success = v
			}
			if m, ok2 := data["message"].(string); ok2 {
				msgI = m
			}
			if b, ok2 := data["bandwidthMbps"].(float64); ok2 {
				bw = b
			}
		}
		// Accumulate diagnosis traffic into usage when succeed
		if success && bw > 0 {
			duration := 5.0 // seconds (client payload uses 5s)
			bytes := int64((bw * 1e6 / 8.0) * duration)
			// add to out_flow as diagnostic consumption
			dbpkg.DB.Model(&model.Forward{}).Where("id=?", f.ID).
				Updates(map[string]any{"out_flow": gorm.Expr("out_flow + ?", bytes), "updated_time": time.Now().UnixMilli()})
			dbpkg.DB.Model(&model.User{}).Where("id=?", f.UserID).
				Updates(map[string]any{"out_flow": gorm.Expr("out_flow + ?", bytes), "updated_time": time.Now().UnixMilli()})
			var ut model.UserTunnel
			if err := dbpkg.DB.Where("user_id=? and tunnel_id=?", f.UserID, f.TunnelID).First(&ut).Error; err == nil && ut.ID > 0 {
				dbpkg.DB.Model(&model.UserTunnel{}).Where("id=?", ut.ID).
					Updates(map[string]any{"out_flow": gorm.Expr("out_flow + ?", bytes)})
			}
		}
		res = map[string]interface{}{
			"success": success, "description": "iperf3 反向带宽测试", "nodeName": inNode.Name, "nodeId": inNode.ID,
			"targetIp": exitIP, "targetPort": srvPort, "message": msgI, "bandwidthMbps": bw,
		}
	default:
		c.JSON(http.StatusOK, response.ErrMsg("未知诊断步骤"))
		return
	}
	c.JSON(http.StatusOK, response.Ok(res))
}

func containsColon(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return true
		}
	}
	return false
}
func firstTargetHost(addr string) string {
	// remoteAddr may be comma-separated list; return first host part
	for i := 0; i < len(addr); i++ {
		if addr[i] == ',' {
			addr = addr[:i]
			break
		}
	}
	// strip brackets for IPv6 if present; keep host
	// host:port format assumed
	return addr
}

// parseRemoteAddrs splits a remoteAddr string into a list of host:port targets.
// Accepts comma-separated list produced by frontend; trims spaces and drops empty items.
func parseRemoteAddrs(remote string) []string {
	if remote == "" {
		return nil
	}
	parts := strings.Split(remote, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// getTunnelPathNodes reads optional multi-level path from ViteConfig (name: tunnel_path_<id>), JSON array of node IDs
func getTunnelPathNodes(tunnelID int64) []int64 {
	var cfg model.ViteConfig
	key := fmt.Sprintf("tunnel_path_%d", tunnelID)
	if err := dbpkg.DB.Where("name = ?", key).First(&cfg).Error; err != nil || cfg.Value == "" {
		return nil
	}
	var ids []int64
	if e := json.Unmarshal([]byte(cfg.Value), &ids); e == nil {
		return ids
	}
	// also support comma separated values
	parts := strings.Split(cfg.Value, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if v, err := strconv.ParseInt(p, 10, 64); err == nil {
			ids = append(ids, v)
		}
	}
	return ids
}

func splitHostPortSafe(hp string) (string, int) {
	host, portStr, err := net.SplitHostPort(hp)
	if err != nil {
		return "", 0
	}
	p, _ := net.LookupPort("tcp", portStr)
	if p == 0 {
		return host, 0
	}
	return host, p
}

// Ask node via WS Diagnose; if node不回执则返回 ok=false
// diagnoseFromNode and tcpDialFallback are implemented in diagnose_util.go

// POST /api/v1/forward/update-order {forwards: [{id, inx}]}
func ForwardUpdateOrder(c *gin.Context) {
	var p struct {
		Forwards []struct {
			ID  int64 `json:"id"`
			Inx *int  `json:"inx"`
		} `json:"forwards"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	for _, it := range p.Forwards {
		dbpkg.DB.Model(&model.Forward{}).Where("id = ?", it.ID).Update("inx", it.Inx)
	}
	c.JSON(http.StatusOK, response.OkNoData())
}

// naive free port allocator within [port_sta, port_end] by scanning forward records for this tunnel
func firstFreePort(inNodeID int64, t model.Tunnel, excludeForwardID int64) int {
	if t.Type != 1 { // for tunnel-forward we cannot determine here
		// try TCPListenAddr: not implementing scanning
	}
	// Use node's port range
	var node model.Node
	if err := dbpkg.DB.First(&node, inNodeID).Error; err != nil {
		return 0
	}
	busy := map[int]bool{}
	var list []model.Forward
	dbpkg.DB.Where("tunnel_id = ?", t.ID).Find(&list)
	for _, f := range list {
		if f.ID != excludeForwardID {
			busy[f.InPort] = true
		}
	}
	for p := node.PortSta; p <= node.PortEnd; p++ {
		if !busy[p] {
			return p
		}
	}
	return 0
}

// buildServiceName follows forwardId_userId_userTunnelId (userTunnelId taken as 0 for admin or when missing)
func buildServiceName(forwardID int64, userID int64, tunnelID int64) string {
	// try find user_tunnel id
	var ut model.UserTunnel
	var utID int64 = 0
	if err := dbpkg.DB.Where("user_id=? and tunnel_id=?", userID, tunnelID).First(&ut).Error; err == nil {
		utID = ut.ID
	}
	return fmt.Sprintf("%d_%d_%d", forwardID, userID, utID)
}

// buildServiceConfig constructs a gost ServiceConfig JSON with listener port and forward target
func buildServiceConfig(name string, listenPort int, target string, iface *string) map[string]any {
	// Gost service JSON (v3): use top-level addr (NOT listener.addr)
	// https://gost.run/tutorials/reverse-proxy-tunnel/#__tabbed_2_2
	svc := map[string]any{
		"name": name,
		"addr": fmt.Sprintf(":%d", listenPort),
		"listener": map[string]any{
			"type": "tcp",
		},
		"handler": map[string]any{
			"type": "forward",
		},
		// forwarder.nodes: support multiple remote targets (comma-separated)
		"forwarder": map[string]any{},
	}
	nodes := []map[string]any{}
	addrs := parseRemoteAddrs(target)
	if len(addrs) == 0 {
		addrs = []string{target}
	}
	for i, a := range addrs {
		nodes = append(nodes, map[string]any{"name": fmt.Sprintf("target_%d", i), "addr": a})
	}
	svc["forwarder"].(map[string]any)["nodes"] = nodes
	// attach panel-managed marker and enable stats/observer defaults; optional interface
	meta := map[string]any{"managedBy": "network-panel", "enableStats": true, "observer.period": "5s", "observer.resetTraffic": false}
	if iface != nil && *iface != "" {
		meta["interface"] = *iface
	}
	svc["metadata"] = meta
	return svc
}

// ---- Helpers for multi-level tunnel: query in-use ports and pick free port ----

func queryNodeServicePorts(nodeID int64) map[int]bool {
	ports := map[int]bool{}
	reqID := RandUUID()
	payload := map[string]any{"requestId": reqID}
	if err := sendWSCommand(nodeID, "QueryServices", payload); err != nil {
		return ports
	}
	ch := make(chan map[string]interface{}, 1)
	diagMu.Lock()
	diagWaiters[reqID] = ch
	diagMu.Unlock()
	select {
	case res := <-ch:
		if data, ok := res["data"].([]interface{}); ok {
			for _, it := range data {
				m, _ := it.(map[string]interface{})
				if m == nil {
					continue
				}
				if v, ok2 := m["addr"].(string); ok2 {
					if p := parsePort(v); p > 0 {
						ports[p] = true
						continue
					}
				}
				if lst, ok2 := m["listener"].(map[string]interface{}); ok2 {
					if v, ok3 := lst["addr"].(string); ok3 {
						if p := parsePort(v); p > 0 {
							ports[p] = true
						}
					}
				}
			}
		}
	case <-time.After(3 * time.Second):
		diagMu.Lock()
		delete(diagWaiters, reqID)
		diagMu.Unlock()
	}
	return ports
}

func parsePort(addr string) int {
	host, port, err := net.SplitHostPort(addr)
	_ = host
	if err != nil {
		return 0
	}
	v, _ := net.LookupPort("tcp", port)
	return v
}

// query raw services list for a node via WS (best-effort)
func queryNodeServicesRaw(nodeID int64) []map[string]any {
	reqID := RandUUID()
	payload := map[string]any{"requestId": reqID}
	if err := sendWSCommand(nodeID, "QueryServices", payload); err != nil {
		return nil
	}
	ch := make(chan map[string]interface{}, 1)
	diagMu.Lock()
	diagWaiters[reqID] = ch
	diagMu.Unlock()
	defer func() { diagMu.Lock(); delete(diagWaiters, reqID); diagMu.Unlock() }()
	select {
	case res := <-ch:
		if data, ok := res["data"].([]interface{}); ok {
			out := make([]map[string]any, 0, len(data))
			for _, it := range data {
				if m, ok2 := it.(map[string]any); ok2 {
					out = append(out, m)
				}
			}
			return out
		}
	case <-time.After(3 * time.Second):
	}
	return nil
}

// suggestPortsViaAgent asks the node agent for nearest higher free ports above base
// using OS-level checks (e.g., lsof/ss). Returns up to 'count' ports.
func suggestPortsViaAgent(nodeID int64, base int, count int) []int {
	if count <= 0 {
		count = 10
	}
	reqID := RandUUID()
	payload := map[string]any{"requestId": reqID, "base": base, "count": count}
	if err := sendWSCommand(nodeID, "SuggestPorts", payload); err != nil {
		return nil
	}
	ch := make(chan map[string]interface{}, 1)
	diagMu.Lock()
	diagWaiters[reqID] = ch
	diagMu.Unlock()
	defer func() { diagMu.Lock(); delete(diagWaiters, reqID); diagMu.Unlock() }()
	select {
	case res := <-ch:
		if data, ok := res["data"].(map[string]interface{}); ok {
			if arr, ok2 := data["ports"].([]interface{}); ok2 {
				out := make([]int, 0, len(arr))
				for _, v := range arr {
					switch x := v.(type) {
					case float64:
						out = append(out, int(x))
					case int:
						out = append(out, x)
					}
				}
				return out
			}
		}
	case <-time.After(3 * time.Second):
	}
	return nil
}

// probePortViaAgent asks the node agent to check if a port is currently listening (OS-level).
// returns true if port is FREE (not listening), false otherwise or on error.
func probePortViaAgent(nodeID int64, port int) bool {
	reqID := RandUUID()
	payload := map[string]any{"requestId": reqID, "port": port}
	if err := sendWSCommand(nodeID, "ProbePort", payload); err != nil {
		return false
	}
	ch := make(chan map[string]interface{}, 1)
	diagMu.Lock()
	diagWaiters[reqID] = ch
	diagMu.Unlock()
	defer func() { diagMu.Lock(); delete(diagWaiters, reqID); diagMu.Unlock() }()
	select {
	case res := <-ch:
		if data, ok := res["data"].(map[string]interface{}); ok {
			if v, ok2 := data["listening"].(bool); ok2 {
				return !v
			}
		}
	case <-time.After(2 * time.Second):
	}
	return false
}

func findFreePortOnNode(nodeID int64, prefer int, min int, max int) int {
	used := queryNodeServicePorts(nodeID)
	start := prefer
	if start < min || start > max {
		start = min
	}
	if start <= 0 {
		start = min
	}
	// Prefer agent suggestions first to avoid OS-level conflicts (include base)
	if sug := suggestPortsViaAgent(nodeID, start-1, 50); len(sug) > 0 {
		for _, p := range sug {
			if p >= min && p <= max && !used[p] {
				return p
			}
		}
	}
	// Probe subsequent windows via agent suggestions (chunked) before falling back to local scan
	for base := start; base <= max; base += 50 {
		if sug := suggestPortsViaAgent(nodeID, base-1, 50); len(sug) > 0 {
			for _, p := range sug {
				if p >= min && p <= max && !used[p] {
					return p
				}
			}
		}
	}
	// Fallback: verify per-port via agent probe to avoid collisions
	for p := start; p <= max; p++ {
		if used[p] {
			continue
		}
		if probePortViaAgent(nodeID, p) {
			return p
		}
	}
	for p := start - 1; p >= min; p-- {
		if used[p] {
			continue
		}
		if probePortViaAgent(nodeID, p) {
			return p
		}
	}
	return 0
}

// findFreePortOnNodeAny picks a free TCP port on node >= min (ignores node's configured port range)
func findFreePortOnNodeAny(nodeID int64, prefer int, min int) int {
	if min < 1 {
		min = 1
	}
	used := queryNodeServicePorts(nodeID)
	start := prefer
	if start < min {
		start = min
	}
	if sug := suggestPortsViaAgent(nodeID, start, 10); len(sug) > 0 {
		for _, p := range sug {
			if p >= min && !used[p] {
				return p
			}
		}
	}
	if start >= min && !used[start] {
		return start
	}
	for p := start + 1; p <= 65535; p++ {
		if !used[p] {
			return p
		}
	}
	for p := start - 1; p >= min; p-- {
		if !used[p] {
			return p
		}
	}
	return 0
}

func isOverlayIP(ip string) bool { return strings.HasPrefix(ip, "10.126.126.") }

// build shadowsocks server service on exit node
func buildSSService(name string, listenPort int, password string, method string, opts ...map[string]any) map[string]any {
	if method == "" {
		method = "AEAD_CHACHA20_POLY1305"
	}
	svc := map[string]any{
		"name":     name,
		"addr":     fmt.Sprintf(":%d", listenPort),
		"listener": map[string]any{"type": "tcp"},
		"handler": map[string]any{
			"type": "ss",
			"auth": map[string]any{"username": method, "password": password},
		},
	}
	// optional extras: observer, limiter, rlimiter, metadata
	// base metadata includes panel marker
	baseMeta := map[string]any{"managedBy": "network-panel", "enableStats": true, "observer.period": "5s", "observer.resetTraffic": false}
	if len(opts) > 0 && opts[0] != nil {
		o := opts[0]
		if v, ok := o["observer"].(string); ok && v != "" {
			svc["observer"] = v
		}
		if v, ok := o["limiter"].(string); ok && v != "" {
			svc["limiter"] = v
		}
		if v, ok := o["rlimiter"].(string); ok && v != "" {
			svc["rlimiter"] = v
		}
		if v, ok := o["metadata"].(map[string]any); ok && v != nil {
			// merge and preserve managedBy
			for k, val := range v {
				baseMeta[k] = val
			}
		}
	}
	svc["metadata"] = baseMeta
	return svc
}

func preferIface(a *string, b *string) *string {
	if a != nil && *a != "" {
		return a
	}
	return b
}

func getOutNodeIP(t model.Tunnel) string {
	if t.OutIP != nil && *t.OutIP != "" {
		return *t.OutIP
	}
	if t.OutNodeID != nil {
		var n model.Node
		if err := dbpkg.DB.First(&n, *t.OutNodeID).Error; err == nil {
			return n.ServerIP
		}
	}
	return "127.0.0.1"
}

// safeHostPort formats host and port into host:port with IPv6 brackets if needed
func safeHostPort(host string, port int) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return fmt.Sprintf("[%s]:%d", host, port)
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// firstIPAny returns the first non-empty IP from Node.IP list or ServerIP.
func firstIPAny(n model.Node) string {
	if n.IP != "" {
		parts := strings.Split(n.IP, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				return p
			}
		}
	}
	if n.ServerIP != "" {
		return n.ServerIP
	}
	return ""
}

// helper to safely get OutNodeID as int64
func outNodeIDOr0(t model.Tunnel) int64 {
	if t.OutNodeID != nil {
		return *t.OutNodeID
	}
	return 0
}

// find free out port on out-node range
func firstFreePortOut(t model.Tunnel, excludeForwardID int64) int {
	if t.OutNodeID == nil {
		return 0
	}
	var outNode model.Node
	if err := dbpkg.DB.First(&outNode, *t.OutNodeID).Error; err != nil {
		return 0
	}
	busy := map[int]bool{}
	var list []model.Forward
	dbpkg.DB.Where("tunnel_id = ?", t.ID).Find(&list)
	for _, f := range list {
		if f.ID != excludeForwardID && f.OutPort != nil {
			busy[*f.OutPort] = true
		}
	}
	for p := outNode.PortSta; p <= outNode.PortEnd; p++ {
		if !busy[p] {
			return p
		}
	}
	return 0
}
