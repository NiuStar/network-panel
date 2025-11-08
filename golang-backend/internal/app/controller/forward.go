package controller

import (
    "fmt"
    "encoding/json"
    "log"
    "net"
    "net/http"
    "network-panel/golang-backend/internal/app/util"
    "strings"
    "time"
    "strconv"

    "github.com/gin-gonic/gin"
    "network-panel/golang-backend/internal/app/dto"
    "network-panel/golang-backend/internal/app/model"
    "network-panel/golang-backend/internal/app/response"
    dbpkg "network-panel/golang-backend/internal/db"
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
	}
    // allocate inPort if nil: find first port in range not used
    inPort := 0
    if req.InPort != nil {
        inPort = *req.InPort
    } else {
        inPort = firstFreePort(tun.InNodeID, tun, 0)
    }
    // agent-level verification: pick free port on entry node if occupied
    var inNode model.Node
    _ = dbpkg.DB.First(&inNode, tun.InNodeID).Error
    minP := 10000; maxP := 65535
    if inNode.PortSta > 0 { minP = inNode.PortSta }
    if inNode.PortEnd > 0 { maxP = inNode.PortEnd }
    inPort = findFreePortOnNode(tun.InNodeID, inPort, minP, maxP)
    if inPort == 0 {
        c.JSON(http.StatusOK, response.ErrMsg("隧道入口端口已满，无法分配新端口"))
        return
    }
	now := time.Now().UnixMilli()
	f := model.Forward{BaseEntity: model.BaseEntity{CreatedTime: now, UpdatedTime: now}, UserID: uid, Name: req.Name, TunnelID: req.TunnelID, InPort: inPort, RemoteAddr: req.RemoteAddr, InterfaceName: req.InterfaceName, Strategy: req.Strategy}
	// allocate outPort for legacy tunnel-forward only (no SS params)
    if tun.Type == 2 {
        if !(req.SsPort != nil && req.SsPassword != nil && *req.SsPassword != "") {
            if op := firstFreePortOut(tun, 0); op != 0 {
                // verify against agent services on exit node
                exitID := outNodeIDOr0(tun)
                var outNode model.Node
                _ = dbpkg.DB.First(&outNode, exitID).Error
                minO := 10000; maxO := 65535
                if outNode.PortSta > 0 { minO = outNode.PortSta }
                if outNode.PortEnd > 0 { maxO = outNode.PortEnd }
                free := findFreePortOnNode(exitID, op, minO, maxO)
                if free == 0 { c.JSON(http.StatusOK, response.ErrMsg("隧道出口端口已满，无法分配新端口")); return }
                f.OutPort = &free
            } else {
                c.JSON(http.StatusOK, response.ErrMsg("隧道出口端口已满，无法分配新端口"))
                return
            }
        }
    }
	if err := dbpkg.DB.Create(&f).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("端口转发创建失败"))
		return
	}
    // push to node(s)
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
			"metadata": map[string]any{"managedBy": "network-panel"},
		}
		_ = sendWSCommand(outNodeIDOr0(tun), "AddService", []map[string]any{outSvc})

        outIP := getOutNodeIP(tun)
        exitAddr := safeHostPort(outIP, *f.OutPort)

        // Multi-level path support for tunnel-forward:
        // - exit: relay over gRPC (server)
        // - mids: plain TCP forward (listen on port, forward to next hop addr:port)
        // - entry: HTTP handler with chain(connector=relay, dialer=grpc) targeting FIRST MID addr:port
        path := getTunnelPathNodes(tun.ID)
        if len(path) > 0 {
            // Pre-allocate TCP ports on mids (avoid conflicts using agent query)
            midPorts := make([]int, len(path))
            for i := range path {
                // prefer use inPort as baseline, within node port range if needed
                var n model.Node
                _ = dbpkg.DB.First(&n, path[i]).Error
                minP := 10000; maxP := 65535
                if n.PortSta > 0 { minP = n.PortSta }
                if n.PortEnd > 0 { maxP = n.PortEnd }
                midPorts[i] = findFreePortOnNode(path[i], f.InPort, minP, maxP)
                if midPorts[i] == 0 { midPorts[i] = f.InPort }
            }
            // Deploy simple TCP forward on each mid to the next hop
            for i := 0; i < len(path); i++ {
                nid := path[i]
                thisPort := midPorts[i]
                var target string
                if i < len(path)-1 {
                    var nx model.Node
                    if err := dbpkg.DB.First(&nx, path[i+1]).Error; err != nil { continue }
                    target = safeHostPort(preferIPv4(nx), midPorts[i+1])
                } else {
                    // last mid forwards to exit relay (gRPC) address directly
                    target = exitAddr
                }
                midName := fmt.Sprintf("%s_mid_%d", name, i)
                svc := buildServiceConfig(midName, thisPort, target, nil)
                _ = sendWSCommand(nid, "AddService", []map[string]any{svc})
            }
            // Entry node: forward handler + chain to first mid address using dialer=grpc（负载将被各中间节点逐跳TCP转发）
            var first model.Node
            if err := dbpkg.DB.First(&first, path[0]).Error; err == nil {
                inSvc := map[string]any{
                    "name":     name,
                    "addr":     fmt.Sprintf(":%d", f.InPort),
                    "listener": map[string]any{"type": "tcp"},
                    "handler":  map[string]any{"type": "forward", "chain": "chain_" + name},
                    "metadata": map[string]any{"managedBy": "network-panel"},
                }
                chainName := "chain_" + name
                hopName := "hop_" + name
                node := map[string]any{
                    "name":      "node-" + name,
                    // Important: dial to FIRST MID address; bytes are gRPC frames that will be forwarded hop-by-hop to exit
                    "addr":      safeHostPort(preferIPv4(first), midPorts[0]),
                    "connector": map[string]any{"type": "relay", "auth": map[string]any{"username": user, "password": pass}},
                    "dialer":    map[string]any{"type": "grpc"},
                }
                inSvc["_chains"] = []any{map[string]any{"name": chainName, "metadata": map[string]any{"managedBy": "network-panel"}, "hops": []any{map[string]any{"name": hopName, "nodes": []any{node}}}}}
                // forwarder 目标为远程地址（取第一项）
                inSvc["forwarder"] = map[string]any{"nodes": []map[string]any{{"name": "target", "addr": firstTargetHost(f.RemoteAddr)}}}
                _ = sendWSCommand(tun.InNodeID, "AddService", []map[string]any{inSvc})
            }
        } else {
            // Original single-hop: in -> exit relay
            inSvc := map[string]any{
                "name":     name,
                "addr":     fmt.Sprintf(":%d", f.InPort),
                "listener": map[string]any{"type": "tcp"},
                "handler":  map[string]any{"type": "forward", "chain": "chain_" + name},
                "metadata": map[string]any{"managedBy": "network-panel"},
            }
            chainName := "chain_" + name
            hopName := "hop_" + name
            node := map[string]any{
                "name":      "node-" + name,
                "addr":      exitAddr,
                "connector": map[string]any{"type": "relay", "auth": map[string]any{"username": user, "password": pass}},
                "dialer":    map[string]any{"type": "grpc"},
            }
            inSvc["_chains"] = []any{map[string]any{"name": chainName, "metadata": map[string]any{"managedBy": "network-panel"}, "hops": []any{map[string]any{"name": hopName, "nodes": []any{node}}}}}
            // forwarder 目标为远程地址（取第一项）
            inSvc["forwarder"] = map[string]any{"nodes": []map[string]any{{"name": "target", "addr": firstTargetHost(f.RemoteAddr)}}}
            _ = sendWSCommand(tun.InNodeID, "AddService", []map[string]any{inSvc})
            // restart in & out to ensure effect
            _ = sendWSCommand(tun.InNodeID, "RestartGost", map[string]any{"reason":"forward_create"})
            _ = sendWSCommand(outNodeIDOr0(tun), "RestartGost", map[string]any{"reason":"forward_create"})
        }
        // restart gost on involved nodes to ensure services take effect
        nodesToRestart := make(map[int64]struct{})
        nodesToRestart[tun.InNodeID] = struct{}{}
        if len(path) > 0 { for _, nid := range path { nodesToRestart[nid] = struct{}{} } }
        nodesToRestart[outNodeIDOr0(tun)] = struct{}{}
        for nid := range nodesToRestart { if nid > 0 { _ = sendWSCommand(nid, "RestartGost", map[string]any{"reason":"forward_create"}) } }
    } else {
        // port-forward: support multi-level path
        path := getTunnelPathNodes(tun.ID)
        if len(path) == 0 {
            // single hop: in-node listens on inPort and forwards to remoteAddr
            svc := buildServiceConfig(name, f.InPort, f.RemoteAddr, preferIface(f.InterfaceName, tun.InterfaceName))
            _ = sendWSCommand(tun.InNodeID, "AddService", []map[string]any{svc})
            _ = sendWSCommand(tun.InNodeID, "RestartGost", map[string]any{"reason":"forward_create"})
        } else {
            // chain: [inNode -> mid1 -> mid2 -> ... -> last], each listens on same inPort and forwards to next hop (last forwards to final remote)
            hops := append([]int64{tun.InNodeID}, path...)
            for i := 0; i < len(hops); i++ {
                nodeID := hops[i]
                var target string
                if i < len(hops)-1 {
                    // next hop server IP: use Node.server_ip
                    var n model.Node
                    if err := dbpkg.DB.First(&n, hops[i+1]).Error; err == nil {
                        target = safeHostPort(n.ServerIP, f.InPort)
                    } else {
                        continue
                    }
                } else {
                    // last hop forwards to final remote
                    target = f.RemoteAddr
                }
                svc := buildServiceConfig(name, f.InPort, target, preferIface(f.InterfaceName, tun.InterfaceName))
                _ = sendWSCommand(nodeID, "AddService", []map[string]any{svc})
            }
            // restart gost on all hops
            nodesToRestart := make(map[int64]struct{})
            for _, nid := range append([]int64{tun.InNodeID}, path...) { nodesToRestart[nid] = struct{}{} }
            for nid := range nodesToRestart { if nid > 0 { _ = sendWSCommand(nid, "RestartGost", map[string]any{"reason":"forward_create"}) } }
        }
    }
    c.JSON(http.StatusOK, response.OkNoData())
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
		f.InPort = *req.InPort
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
		// update out-node TLS tunnel service
		outSvc := map[string]any{
			"name":     name,
			"addr":     fmt.Sprintf(":%d", *f.OutPort),
			"listener": map[string]any{"type": "tls"},
			"handler":  map[string]any{"type": "rtcp"},
			"metadata": map[string]any{"managedBy": "network-panel"},
		}
        _ = sendWSCommand(outNodeIDOr0(tun), "AddService", []map[string]any{outSvc})

        // update in-node entry service with chain(dialer=tls) and forwarder target=exitAddr
        outIP := getOutNodeIP(tun)
        exitAddr := safeHostPort(outIP, *f.OutPort)
        // 入口 forwarder 统一指向远程地址（取第一项）
        inSvc := buildServiceConfig(name, f.InPort, firstTargetHost(f.RemoteAddr), preferIface(f.InterfaceName, tun.InterfaceName))
        chainName := "chain_" + name
        hopName := "hop_" + name
        // ensure handler is forward and attach chain
        if h, ok := inSvc["handler"].(map[string]any); ok {
            h["type"] = "forward"
            h["chain"] = chainName
        } else {
            inSvc["handler"] = map[string]any{"type":"forward","chain":chainName}
        }
        node := map[string]any{"name": "exit-tls", "addr": exitAddr, "dialer": map[string]any{"type": "tls"}}
        inSvc["_chains"] = []any{map[string]any{"name": chainName, "hops": []any{map[string]any{"name": hopName, "nodes": []any{node}}}}}
        _ = sendWSCommand(tun.InNodeID, "UpdateService", []map[string]any{inSvc})
        // 强制重启入口与出口节点的 gost，确保更新立即生效
        _ = sendWSCommand(tun.InNodeID, "RestartGost", map[string]any{"reason":"forward_update"})
        _ = sendWSCommand(outNodeIDOr0(tun), "RestartGost", map[string]any{"reason":"forward_update"})
    } else {
        svc := buildServiceConfig(name, f.InPort, f.RemoteAddr, preferIface(f.InterfaceName, tun.InterfaceName))
        _ = sendWSCommand(tun.InNodeID, "UpdateService", []map[string]any{svc})
        _ = sendWSCommand(tun.InNodeID, "RestartGost", map[string]any{"reason":"forward_update"})
    }
    c.JSON(http.StatusOK, response.OkMsg("端口转发更新成功"))
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
        _ = sendWSCommand(tun.InNodeID, "DeleteService", map[string]any{"services": []string{name}})
        _ = sendWSCommand(outNodeIDOr0(tun), "DeleteService", map[string]any{"services": []string{name}})
        // 删除多级路径的中间节点 mid 服务（name_mid_i）
        path := getTunnelPathNodes(tun.ID)
        for i := 0; i < len(path); i++ {
            midName := fmt.Sprintf("%s_mid_%d", name, i)
            _ = sendWSCommand(path[i], "DeleteService", map[string]any{"services": []string{midName}})
        }
    } else {
        // 端口转发：删除入口上的服务
        _ = sendWSCommand(tun.InNodeID, "DeleteService", map[string]any{"services": []string{name}})
        // 若端口转发也采用了多级路径（各 hop 使用相同 name），尝试在中间节点删除同名服务
        path := getTunnelPathNodes(tun.ID)
        for _, nid := range path {
            _ = sendWSCommand(nid, "DeleteService", map[string]any{"services": []string{name}})
        }
    }
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
		_ = sendWSCommand(t.InNodeID, "PauseService", map[string]interface{}{"services": []string{name}})
		if t.Type == 2 {
			_ = sendWSCommand(outNodeIDOr0(t), "PauseService", map[string]interface{}{"services": []string{name}})
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
		_ = sendWSCommand(t.InNodeID, "ResumeService", map[string]interface{}{"services": []string{name}})
		if t.Type == 2 {
			_ = sendWSCommand(outNodeIDOr0(t), "ResumeService", map[string]interface{}{"services": []string{name}})
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

	// 远端地址格式校验（仅校验语法）
	addrOK := true
	addrMsg := "地址格式正确"
	targetHostPort := firstTargetHost(f.RemoteAddr)
	targetHost, targetPort := splitHostPortSafe(targetHostPort)
	if targetHost == "" || targetPort == 0 {
		addrOK = false
		addrMsg = "远端地址格式无效，需为 host:port 或多地址以逗号分隔"
	}
	results = append(results, map[string]interface{}{
		"success":     addrOK,
		"description": "远端地址校验",
		"nodeName":    ifThen(t.Type == 2, outNode.Name, inNode.Name),
		"nodeId":      ifThen[int64](t.Type == 2, outNode.ID, inNode.ID),
		"targetIp":    targetHost,
		"targetPort":  targetPort,
		"message":     addrMsg,
	})

	// 从节点侧进行真实网络诊断（tcp connect 多次测量）
	if addrOK {
		// 选择执行诊断的节点：端口转发在入口节点执行，隧道转发在出口节点执行
		var runNode model.Node
		var runNodeID int64
		if t.Type == 2 {
			runNode, runNodeID = outNode, outNode.ID
		} else {
			runNode, runNodeID = inNode, inNode.ID
		}
		avg, loss, ok, msg, rid := diagnoseFromNodeCtx(runNodeID, targetHost, targetPort, 3, 1500, map[string]interface{}{"src": "forward", "step": "nodeRemote", "forwardId": f.ID})
		results = append(results, map[string]interface{}{
			"success":     ok,
			"description": ifThen(t.Type == 2, "出口节点到远端连通性", "入口节点到远端连通性"),
			"nodeName":    runNode.Name,
			"nodeId":      runNodeID,
			"targetIp":    targetHost,
			"targetPort":  targetPort,
			"averageTime": avg,
			"packetLoss":  loss,
			"message":     msg,
			"reqId":       rid,
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
	var t model.Tunnel
	_ = dbpkg.DB.First(&t, f.TunnelID).Error
	var inNode, outNode model.Node
	_ = dbpkg.DB.First(&inNode, t.InNodeID).Error
	if t.Type == 2 && t.OutNodeID != nil {
		_ = dbpkg.DB.First(&outNode, *t.OutNodeID).Error
	}

    outId := int64(0)
    if t.OutNodeID != nil { outId = *t.OutNodeID }
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
        if t.Type == 2 && t.OutNodeID != nil { hops = append(hops, *t.OutNodeID) }
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
        // final: last hop node to remote host:port via TCP
        hp := firstTargetHost(f.RemoteAddr)
        host, port := splitHostPortSafe(hp)
        if len(hops) > 0 && host != "" && port > 0 {
            last := hops[len(hops)-1]
            var lastN model.Node
            _ = dbpkg.DB.First(&lastN, last).Error
            avg, loss, ok, msg, rid := diagnoseFromNodeCtx(last, host, port, 3, 1500, map[string]interface{}{"src": "forward", "step": "remote", "forwardId": f.ID})
            items = append(items, map[string]any{
                "success": ok, "description": "最终到远端连通性 (TCP)", "nodeName": lastN.Name, "nodeId": last,
                "targetIp": host, "targetPort": port, "averageTime": avg, "packetLoss": loss, "message": msg, "reqId": rid,
            })
        }
        c.JSON(http.StatusOK, response.Ok(map[string]any{"results": items}))
        return
    case "iperf3":
        if t.Type != 2 {
            c.JSON(http.StatusOK, response.ErrMsg("仅隧道转发支持iperf3"))
            return
        }
		exitIP := orString(ptrString(t.OutIP), outNode.ServerIP)
		// 1) 让出口节点随机端口启动服务器
		srvReq := map[string]interface{}{"requestId": RandUUID(), "mode": "iperf3", "server": true, "port": 0}
		srvRes, ok := RequestDiagnose(outNode.ID, srvReq, 6*time.Second)
		if !ok {
			c.JSON(http.StatusOK, response.ErrMsg("出口节点未响应iperf3服务启动"))
			return
		}
		srvPort := 0
		if data, _ := srvRes["data"].(map[string]interface{}); data != nil {
			if p, ok2 := toFloat(data["port"]); ok2 {
				srvPort = int(p)
			}
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
        if p == "" { continue }
        if v, err := strconv.ParseInt(p, 10, 64); err == nil { ids = append(ids, v) }
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
		"forwarder": map[string]any{
			"nodes": []map[string]any{{
				"name": "target",
				"addr": target,
			}},
		},
	}
	// attach panel-managed marker and optional interface
	meta := map[string]any{"managedBy": "network-panel"}
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
    diagMu.Lock(); diagWaiters[reqID] = ch; diagMu.Unlock()
    select {
    case res := <-ch:
        if data, ok := res["data"].([]interface{}); ok {
            for _, it := range data {
                m, _ := it.(map[string]interface{})
                if m == nil { continue }
                if v, ok2 := m["addr"].(string); ok2 {
                    if p := parsePort(v); p > 0 { ports[p] = true; continue }
                }
                if lst, ok2 := m["listener"].(map[string]interface{}); ok2 {
                    if v, ok3 := lst["addr"].(string); ok3 {
                        if p := parsePort(v); p > 0 { ports[p] = true }
                    }
                }
            }
        }
    case <-time.After(3 * time.Second):
        diagMu.Lock(); delete(diagWaiters, reqID); diagMu.Unlock()
    }
    return ports
}

func parsePort(addr string) int {
    host, port, err := net.SplitHostPort(addr)
    _ = host
    if err != nil { return 0 }
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
    diagMu.Lock(); diagWaiters[reqID] = ch; diagMu.Unlock()
    defer func(){ diagMu.Lock(); delete(diagWaiters, reqID); diagMu.Unlock() }()
    select {
    case res := <-ch:
        if data, ok := res["data"].([]interface{}); ok {
            out := make([]map[string]any, 0, len(data))
            for _, it := range data { if m, ok2 := it.(map[string]any); ok2 { out = append(out, m) } }
            return out
        }
    case <-time.After(3 * time.Second):
    }
    return nil
}

func findFreePortOnNode(nodeID int64, prefer int, min int, max int) int {
    used := queryNodeServicePorts(nodeID)
    start := prefer
    if start <= 0 { start = min }
    if start >= min && start <= max && !used[start] { return start }
    for p := start + 1; p <= max; p++ { if !used[p] { return p } }
    for p := start - 1; p >= min; p-- { if !used[p] { return p } }
    return 0
}

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
	baseMeta := map[string]any{"managedBy": "network-panel"}
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
