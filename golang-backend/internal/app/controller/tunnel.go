package controller

import (
    "encoding/json"
    "fmt"
    "net/http"
    "strconv"
    "strings"
    "time"

	"network-panel/golang-backend/internal/app/dto"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	"network-panel/golang-backend/internal/db"

    "github.com/gin-gonic/gin"
    "log"
)

// TunnelCreate 创建隧道
// @Summary 创建隧道
// @Tags tunnel
// @Accept json
// @Produce json
// @Param data body dto.TunnelDto true "隧道信息"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/tunnel/create [post]
func TunnelCreate(c *gin.Context) {
	var req dto.TunnelDto
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	// unique name
	var cnt int64
	db.DB.Model(&model.Tunnel{}).Where("name = ?", req.Name).Count(&cnt)
	if cnt > 0 {
		c.JSON(http.StatusOK, response.ErrMsg("隧道名称已存在"))
		return
	}
	// in node exists
	var in model.Node
	if err := db.DB.First(&in, req.InNodeID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("入口节点不存在"))
		return
	}
	// set entity
	now := time.Now().UnixMilli()
	status := 1
    var owner *int64
    if uidInf, ok := c.Get("user_id"); ok { tmp := uidInf.(int64); owner = &tmp }
    t := model.Tunnel{BaseEntity: model.BaseEntity{CreatedTime: now, UpdatedTime: now, Status: &status},
        Name: req.Name, OwnerID: owner, InNodeID: req.InNodeID, InIP: in.IP, Type: req.Type, Flow: req.Flow,
        Protocol: req.Protocol, TrafficRatio: req.TrafficRatio, TCPListenAddr: req.TCPListenAddr, UDPListenAddr: req.UDPListenAddr, InterfaceName: req.InterfaceName,
    }
	if req.OutNodeID != nil {
		var out model.Node
		if db.DB.First(&out, *req.OutNodeID).Error == nil {
			t.OutNodeID = req.OutNodeID
			t.OutIP = &out.ServerIP
		}
	}
    // enforce tunnel quota for non-admin
    if roleInf, ok := c.Get("role_id"); ok && roleInf != 0 {
        uidInf, _ := c.Get("user_id"); uid := uidInf.(int64)
        var cfg model.ViteConfig; db.DB.Where("name=?","registration_default_num").First(&cfg)
        limit := 10; if n,err := strconv.Atoi(strings.TrimSpace(cfg.Value)); err==nil && n>0 { limit = n }
        var cnt int64
        db.DB.Model(&model.Tunnel{}).Where("owner_id=?", uid).Count(&cnt)
        if int(cnt) >= limit {
            c.JSON(http.StatusOK, response.ErrMsg("超出隧道数量上限")); return
        }
    }
    if err := db.DB.Create(&t).Error; err != nil {
        c.JSON(http.StatusOK, response.ErrMsg("隧道创建失败"))
        return
    }
	c.JSON(http.StatusOK, response.OkMsg("隧道创建成功"))
}

// TunnelList 隧道列表
// @Summary 隧道列表
// @Tags tunnel
// @Produce json
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/tunnel/list [post]
func TunnelList(c *gin.Context) {
    var list []model.Tunnel
    if roleInf, ok := c.Get("role_id"); ok && roleInf != 0 {
        if uidInf, ok2 := c.Get("user_id"); ok2 { db.DB.Where("owner_id=?", uidInf.(int64)).Find(&list) } else { list = []model.Tunnel{} }
    } else {
        db.DB.Find(&list)
    }
    c.JSON(http.StatusOK, response.Ok(list))
}

// TunnelUpdate 更新隧道
// @Summary 更新隧道
// @Tags tunnel
// @Accept json
// @Produce json
// @Param data body dto.TunnelUpdateDto true "隧道更新信息"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/tunnel/update [post]
func TunnelUpdate(c *gin.Context) {
	var req dto.TunnelUpdateDto
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
    var t model.Tunnel
    if err := db.DB.First(&t, req.ID).Error; err != nil {
        c.JSON(http.StatusOK, response.ErrMsg("隧道不存在"))
        return
    }
    if roleInf, ok := c.Get("role_id"); ok && roleInf != 0 {
        if uidInf, ok2 := c.Get("user_id"); ok2 {
            if t.OwnerID == nil || *t.OwnerID != uidInf.(int64) { c.JSON(http.StatusForbidden, response.ErrMsg("无权限")); return }
        }
    }
	// name unique
	var cnt int64
	db.DB.Model(&model.Tunnel{}).Where("name = ? AND id <> ?", req.Name, req.ID).Count(&cnt)
	if cnt > 0 {
		c.JSON(http.StatusOK, response.ErrMsg("隧道名称已存在"))
		return
	}
	t.Name = req.Name
	t.Flow = int(req.Flow)
	t.TCPListenAddr, t.UDPListenAddr, t.Protocol, t.InterfaceName, t.TrafficRatio = req.TCPListenAddr, req.UDPListenAddr, req.Protocol, req.InterfaceName, req.TrafficRatio
	t.UpdatedTime = time.Now().UnixMilli()
	if err := db.DB.Save(&t).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("隧道更新失败"))
		return
	}
	c.JSON(http.StatusOK, response.OkMsg("隧道更新成功"))
}

// TunnelDelete 删除隧道
// @Summary 删除隧道
// @Tags tunnel
// @Accept json
// @Produce json
// @Param data body SwaggerIDReq true "隧道ID"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/tunnel/delete [post]
func TunnelDelete(c *gin.Context) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
    // usage: forwards and user_tunnel
	var cnt int64
	db.DB.Model(&model.Forward{}).Where("tunnel_id = ?", p.ID).Count(&cnt)
	if cnt > 0 {
		c.JSON(http.StatusOK, response.ErrMsg("该隧道还有转发在使用，请先删除相关转发"))
		return
	}
	db.DB.Model(&model.UserTunnel{}).Where("tunnel_id = ?", p.ID).Count(&cnt)
	if cnt > 0 {
		c.JSON(http.StatusOK, response.ErrMsg("该隧道还有用户权限关联，请先取消用户权限分配"))
		return
	}
    // permission
    if roleInf, ok := c.Get("role_id"); ok && roleInf != 0 {
        var tt model.Tunnel
        if db.DB.First(&tt, p.ID).Error == nil {
            if uidInf, ok2 := c.Get("user_id"); ok2 {
                if tt.OwnerID == nil || *tt.OwnerID != uidInf.(int64) { c.JSON(http.StatusForbidden, response.ErrMsg("无权限")); return }
            }
        }
    }
    if err := db.DB.Delete(&model.Tunnel{}, p.ID).Error; err != nil {
        c.JSON(http.StatusOK, response.ErrMsg("隧道删除失败"))
        return
    }
	c.JSON(http.StatusOK, response.OkMsg("隧道删除成功"))
}

// TunnelUserTunnel 获取用户可见隧道
// @Summary 用户可用隧道列表
// @Tags tunnel
// @Produce json
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/tunnel/user/tunnel [post]
func TunnelUserTunnel(c *gin.Context) {
	roleID, _ := c.Get("role_id")
	userID, _ := c.Get("user_id")
	var tunnels []model.Tunnel
	if roleID == 0 || roleID == nil { // admin or no token
		db.DB.Where("status = ?", 1).Find(&tunnels)
	} else {
		// only those user has permission and active
		db.DB.Raw(`select t.* from tunnel t join user_tunnel ut on ut.tunnel_id=t.id where ut.user_id=? and ut.status=1`, userID).Scan(&tunnels)
	}
	c.JSON(http.StatusOK, response.Ok(tunnels))
}

// ========== user-tunnel management ==========

// TunnelUserAssign 分配隧道权限给用户
// @Summary 分配隧道权限
// @Tags tunnel
// @Accept json
// @Produce json
// @Param data body dto.UserTunnelDto true "用户隧道权限"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/tunnel/user/assign [post]
func TunnelUserAssign(c *gin.Context) {
	var req dto.UserTunnelDto
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var cnt int64
	db.DB.Model(&model.UserTunnel{}).Where("user_id=? and tunnel_id=?", req.UserID, req.TunnelID).Count(&cnt)
	if cnt > 0 {
		c.JSON(http.StatusOK, response.ErrMsg("该用户已拥有此隧道权限"))
		return
	}
	ut := model.UserTunnel{UserID: req.UserID, TunnelID: req.TunnelID, Flow: req.Flow, Num: req.Num, FlowResetTime: req.FlowResetTime, ExpTime: req.ExpTime, SpeedID: req.SpeedID, Status: val(req.Status, 1)}
	if err := db.DB.Create(&ut).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("用户隧道权限分配失败"))
		return
	}
	c.JSON(http.StatusOK, response.OkMsg("用户隧道权限分配成功"))
}

// TunnelUserList 用户隧道权限列表
// @Summary 用户隧道权限列表
// @Tags tunnel
// @Accept json
// @Produce json
// @Param data body dto.UserTunnelQueryDto true "用户ID"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/tunnel/user/list [post]
func TunnelUserList(c *gin.Context) {
	var req dto.UserTunnelQueryDto
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	// join tunnel and speed_limit to enrich names and tunnel flow type
	var items []struct {
		model.UserTunnel
		TunnelName     string  `json:"tunnelName"`
		SpeedLimitName *string `json:"speedLimitName,omitempty"`
		TunnelFlow     *int    `json:"tunnelFlow,omitempty"`
	}
	db.DB.Table("user_tunnel ut").
		Select("ut.*, t.name as tunnel_name, sl.name as speed_limit_name, t.flow as tunnel_flow").
		Joins("left join tunnel t on t.id = ut.tunnel_id").
		Joins("left join speed_limit sl on sl.id = ut.speed_id").
		Where("ut.user_id = ?", req.UserID).
		Scan(&items)
	c.JSON(http.StatusOK, response.Ok(items))
}

// TunnelUserRemove 移除用户隧道权限
// @Summary 移除用户隧道权限
// @Tags tunnel
// @Accept json
// @Produce json
// @Param data body SwaggerIDReq true "用户隧道记录ID"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/tunnel/user/remove [post]
func TunnelUserRemove(c *gin.Context) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	// delete user forwards on this tunnel
	var ut model.UserTunnel
	if err := db.DB.First(&ut, p.ID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("未找到对应的用户隧道权限记录"))
		return
	}
	db.DB.Where("user_id = ? and tunnel_id = ?", ut.UserID, ut.TunnelID).Delete(&model.Forward{})
	db.DB.Delete(&ut)
	c.JSON(http.StatusOK, response.OkMsg("用户隧道权限删除成功"))
}

// TunnelUserUpdate 更新用户隧道权限
// @Summary 更新用户隧道权限
// @Tags tunnel
// @Accept json
// @Produce json
// @Param data body dto.UserTunnelUpdateDto true "更新内容"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/tunnel/user/update [post]
func TunnelUserUpdate(c *gin.Context) {
	var req dto.UserTunnelUpdateDto
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var ut model.UserTunnel
	if err := db.DB.First(&ut, req.ID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("用户隧道权限不存在"))
		return
	}
	ut.Flow, ut.Num = req.Flow, req.Num
	if req.FlowResetTime != nil {
		ut.FlowResetTime = req.FlowResetTime
	}
	if req.ExpTime != nil {
		ut.ExpTime = req.ExpTime
	}
	if req.Status != nil {
		ut.Status = *req.Status
	}
	ut.SpeedID = req.SpeedID
	if err := db.DB.Save(&ut).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("用户隧道权限更新失败"))
		return
	}
	c.JSON(http.StatusOK, response.OkMsg("用户隧道权限更新成功"))
}

// TunnelDiagnose 诊断隧道
// @Summary 诊断隧道
// @Tags tunnel
// @Accept json
// @Produce json
// @Param data body SwaggerTunnelDiagnoseReq true "tunnelId, step 可选 path|overlay|remote"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/tunnel/diagnose [post]
func TunnelDiagnose(c *gin.Context) {
	var p struct {
		TunnelID int64 `json:"tunnelId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var t model.Tunnel
	if err := db.DB.First(&t, p.TunnelID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("隧道不存在"))
		return
	}
	// load nodes
	var inNode model.Node
	_ = db.DB.First(&inNode, t.InNodeID).Error
	var outNode model.Node
	if t.OutNodeID != nil {
		_ = db.DB.First(&outNode, *t.OutNodeID).Error
	}
	// build results
	results := make([]map[string]interface{}, 0, 6)
	// 入口节点连通性（用节点在线状态代替）
	results = append(results, map[string]interface{}{
		"success":     inNode.Status != nil && *inNode.Status == 1,
		"description": "入口节点连通性",
		"nodeName":    inNode.Name,
		"nodeId":      inNode.ID,
		"targetIp":    t.InIP,
		"message":     ifThen(inNode.Status != nil && *inNode.Status == 1, "节点在线", "节点离线"),
	})
	// 出口节点连通性（隧道转发时）
	if t.Type == 2 {
		results = append(results, map[string]interface{}{
			"success":     outNode.Status != nil && *outNode.Status == 1,
			"description": "出口节点连通性",
			"nodeName":    outNode.Name,
			"nodeId":      outNode.ID,
			"targetIp":    orString(ptrString(t.OutIP), outNode.ServerIP),
			"message":     ifThen(outNode.Status != nil && *outNode.Status == 1, "节点在线", "节点离线或未配置"),
		})
	}
	// 多级隧道：逐跳 ICMP（入口->每个中间->出口）
	if t.Type == 2 {
		path := getTunnelPathNodes(t.ID)
		if len(path) == 0 {
			jlog(map[string]any{"event": "iperf3_no_path", "tunnelId": t.ID})
		}
		// build pairs
		hops := make([]int64, 0, 2+len(path))
		hops = append(hops, t.InNodeID)
		hops = append(hops, path...)
		if t.OutNodeID != nil {
			hops = append(hops, *t.OutNodeID)
		}
		for i := 0; i+1 < len(hops); i++ {
			var srcN, dstN model.Node
			_ = db.DB.First(&srcN, hops[i]).Error
			_ = db.DB.First(&dstN, hops[i+1]).Error
			targetIP := dstN.ServerIP
			avg0, loss0, ok0, msg0, rid0 := diagnosePingFromNodeCtx(srcN.ID, targetIP, 3, 1500, map[string]interface{}{"src": "tunnel", "step": "path", "tunnelId": t.ID, "hopIndex": i})
			results = append(results, map[string]interface{}{
				"success":     ok0,
				"description": "逐跳连通性 (ICMP)",
				"nodeName":    srcN.Name,
				"nodeId":      srcN.ID,
				"targetIp":    targetIP,
				"averageTime": avg0,
				"packetLoss":  loss0,
				"message":     msg0,
				"reqId":       rid0,
			})
		}
	}
	// 基础配置校验
	cfgOK := true
	msg := "配置正常"
	if t.TCPListenAddr == nil || *t.TCPListenAddr == "" || t.UDPListenAddr == nil || *t.UDPListenAddr == "" {
		cfgOK = false
		msg = "TCP/UDP监听地址未完整配置"
	}
	results = append(results, map[string]interface{}{
		"success":     cfgOK,
		"description": "基础配置校验",
		"nodeName":    inNode.Name,
		"nodeId":      inNode.ID,
		"targetIp":    t.InIP,
		"message":     msg,
	})

	// 入口节点外网连通性：仅端口转发类型检查
	if t.Type == 1 {
		avg, loss, ok2, msg2, rid1 := diagnosePingFromNodeCtx(inNode.ID, "1.1.1.1", 3, 1500, map[string]interface{}{"src": "tunnel", "step": "entryPublic", "tunnelId": t.ID})
		results = append(results, map[string]interface{}{
			"success":     ok2,
			"description": "入口节点外网连通性 (ICMP 1.1.1.1)",
			"nodeName":    inNode.Name,
			"nodeId":      inNode.ID,
			"targetIp":    "1.1.1.1",
			"averageTime": avg,
			"packetLoss":  loss,
			"message":     msg2,
			"reqId":       rid1,
		})
	}
	// 出口节点外网连通性（隧道转发时）
	if t.Type == 2 && outNode.ID != 0 {
		avg2, loss2, ok3, msg3, rid2 := diagnosePingFromNodeCtx(outNode.ID, "1.1.1.1", 3, 1500, map[string]interface{}{"src": "tunnel", "step": "exitPublic", "tunnelId": t.ID})
		results = append(results, map[string]interface{}{
			"success":     ok3,
			"description": "出口节点外网连通性 (ICMP 1.1.1.1)",
			"nodeName":    outNode.Name,
			"nodeId":      outNode.ID,
			"targetIp":    "1.1.1.1",
			"averageTime": avg2,
			"packetLoss":  loss2,
			"message":     msg3,
			"reqId":       rid2,
		})
	}

	out := map[string]interface{}{
		"tunnelName": t.Name,
		"tunnelType": ifThen(t.Type == 1, "端口转发", "隧道转发"),
		"timestamp":  time.Now().UnixMilli(),
		"results":    results,
	}
	c.JSON(http.StatusOK, response.Ok(out))
}

// TunnelDiagnoseStep 隧道分步诊断
// @Summary 隧道分步诊断
// @Tags tunnel
// @Accept json
// @Produce json
// @Param data body SwaggerTunnelDiagnoseReq true "tunnelId, step: entry|entryExit|exitPublic"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/tunnel/diagnose-step [post]
func TunnelDiagnoseStep(c *gin.Context) {
	var p struct {
		TunnelID int64  `json:"tunnelId" binding:"required"`
		Step     string `json:"step" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var t model.Tunnel
	if err := db.DB.First(&t, p.TunnelID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("隧道不存在"))
		return
	}
	var inNode, outNode model.Node
	_ = db.DB.First(&inNode, t.InNodeID).Error
	if t.OutNodeID != nil {
		_ = db.DB.First(&outNode, *t.OutNodeID).Error
	}

	// avoid nil deref: compute outNodeId safely
	outId := int64(0)
	if t.OutNodeID != nil {
		outId = *t.OutNodeID
	}
	log.Printf("API /tunnel/diagnose-step tunnelId=%d step=%s inNode=%d outNode=%d", p.TunnelID, p.Step, t.InNodeID, outId)
	var res map[string]interface{}
	switch p.Step {
	case "entry":
		avg, loss, ok, msg := diagnosePingFromNode(inNode.ID, "1.1.1.1", 3, 1500)
		res = map[string]interface{}{
			"success": ok, "description": "入口节点外网连通性 (ICMP 1.1.1.1)", "nodeName": inNode.Name, "nodeId": inNode.ID,
			"targetIp": "1.1.1.1", "averageTime": avg, "packetLoss": loss, "message": msg,
		}
	case "entryExit":
		// 选择出口IP：若显式配置了 OutIP 则使用；否则优先选择 IPv4
		exitIP := ptrString(t.OutIP)
		if exitIP == "" {
			exitIP = preferIPv4(outNode)
		}
		avg, loss, ok, msg := diagnosePingFromNode(inNode.ID, exitIP, 3, 1500)
		res = map[string]interface{}{
			"success": ok, "description": "入口到出口连通性 (ICMP)", "nodeName": inNode.Name, "nodeId": inNode.ID,
			"targetIp": exitIP, "averageTime": avg, "packetLoss": loss, "message": msg,
		}
	case "path":
		if t.Type != 2 {
			c.JSON(http.StatusOK, response.ErrMsg("非隧道转发不支持路径检查"))
			return
		}
		path := getTunnelPathNodes(t.ID)
		hops := make([]int64, 0, 2+len(path))
		hops = append(hops, t.InNodeID)
		hops = append(hops, path...)
		if t.OutNodeID != nil {
			hops = append(hops, *t.OutNodeID)
		}
		items := make([]map[string]any, 0, len(hops)-1)
		for i := 0; i+1 < len(hops); i++ {
			var srcN, dstN model.Node
			_ = db.DB.First(&srcN, hops[i]).Error
			_ = db.DB.First(&dstN, hops[i+1]).Error
			target := dstN.ServerIP
			avg, loss, ok, msg := diagnosePingFromNode(srcN.ID, target, 3, 1500)
			items = append(items, map[string]any{
				"success": ok, "description": "逐跳连通性 (ICMP)", "nodeName": srcN.Name, "nodeId": srcN.ID,
				"targetIp": target, "averageTime": avg, "packetLoss": loss, "message": msg,
			})
		}
		// 返回打包的结果数组
		c.JSON(http.StatusOK, response.Ok(map[string]any{"results": items}))
		return
	case "exitPublic":
		if outNode.ID == 0 {
			c.JSON(http.StatusOK, response.ErrMsg("无出口节点"))
			return
		}
		avg, loss, ok, msg := diagnosePingFromNode(outNode.ID, "1.1.1.1", 3, 1500)
		res = map[string]interface{}{
			"success": ok, "description": "出口节点外网连通性 (ICMP 1.1.1.1)", "nodeName": outNode.Name, "nodeId": outNode.ID,
			"targetIp": "1.1.1.1", "averageTime": avg, "packetLoss": loss, "message": msg,
		}
	case "iperf3":
		// 仅隧道转发才进行 iperf3 测速：出口节点启动服务，入口节点作为客户端 -R 连接出口
		if t.Type != 2 || outNode.ID == 0 {
			c.JSON(http.StatusOK, response.ErrMsg("仅隧道转发支持iperf3"))
			return
		}
		// prefer exit bind (监听IP) if provided; fallback to tunnel.OutIP then node.ServerIP
		exitBind := ""
		if t.OutNodeID != nil {
			bm := getTunnelBindMap(t.ID)
			exitBind = bm[*t.OutNodeID]
		}
		exitIP := exitBind
		if exitIP == "" {
			exitIP = orString(ptrString(t.OutIP), outNode.ServerIP)
		}
		// 1) 出口节点启动 iperf3 server：若上一跳的出站IP与出口的监听IP均为 overlay(10.126.126.*)，则不受端口范围限制（仅需>=1000且未占用）
		// 先判断 overlay 邻接
		tmpPath := getTunnelPathNodes(t.ID)
		var prevID int64
		if len(tmpPath) > 0 {
			prevID = tmpPath[len(tmpPath)-1]
		} else {
			prevID = inNode.ID
		}
		prevOut := getTunnelIfaceMap(t.ID)[prevID]
		exitInProbe := ""
		if t.OutNodeID != nil {
			exitInProbe = getTunnelBindMap(t.ID)[*t.OutNodeID]
		}
		overlayExit := strings.HasPrefix(prevOut, "10.126.126.") && strings.HasPrefix(exitInProbe, "10.126.126.")

		// 读取出口节点端口范围（非 overlay 情况使用）
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
		var wantedSrvPort int
		if overlayExit {
			// overlay: 固定范围 10000-65535，取最小可用
			wantedSrvPort = findFreePortOnNodeAny(outNode.ID, 10000, 10000)
			if wantedSrvPort == 0 { wantedSrvPort = 10000 }
		} else {
			wantedSrvPort = findFreePortOnNode(outNode.ID, prefer, minP, maxP)
			if wantedSrvPort == 0 {
				wantedSrvPort = minP
			}
		}
		diagID := RandUUID()
		srvReq := map[string]interface{}{"requestId": RandUUID(), "mode": "iperf3", "server": true, "port": wantedSrvPort, "ctx": map[string]any{"src": "tunnel", "step": "iperf3_server", "tunnelId": t.ID, "diagId": diagID}}
		_ = sendWSCommand(outNode.ID, "Diagnose", srvReq)
		srvRes, ok := RequestDiagnose(outNode.ID, srvReq, 8*time.Second)
        if !ok {
            _ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: outNode.ID, Cmd: "DiagIperf3ServerStart-recv", RequestID: diagID, Success: 0, Message: "未响应"}).Error
            resFail := map[string]any{"success": false, "description": "iperf3 反向带宽测试", "nodeName": inNode.Name, "nodeId": inNode.ID, "message": "出口节点未响应iperf3服务启动", "diagId": diagID}
            c.JSON(http.StatusOK, response.Ok(resFail))
            return
        }
		srvPort := 0
		if data, _ := srvRes["data"].(map[string]interface{}); data != nil {
			if p2, ok2 := data["port"].(float64); ok2 {
				srvPort = int(p2)
			}
			if b, err := json.Marshal(data); err == nil {
				s := string(b)
				_ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: outNode.ID, Cmd: "DiagIperf3ServerStart-recv", RequestID: diagID, Success: 1, Message: "server started", Stdout: &s}).Error
			}
		}
		// 校验返回端口：overlay 情况仅需 >=1000；否则需在范围内
		if overlayExit {
			if srvPort < 1000 {
				srvPort = wantedSrvPort
			}
		} else {
			if srvPort <= 0 || srvPort < minP || srvPort > maxP {
				srvPort = wantedSrvPort
			}
		}
        if srvPort == 0 {
            resFail := map[string]any{"success": false, "description": "iperf3 反向带宽测试", "nodeName": inNode.Name, "nodeId": inNode.ID, "message": "iperf3服务未返回端口", "diagId": diagID}
            c.JSON(http.StatusOK, response.Ok(resFail))
            return
        }
		// 2) 沿路径临时搭建端口直转链路（入口 → 中间... → 最后 → 出口的 iperf3 端口）
		path := getTunnelPathNodes(t.ID)
		ifaceMap := getTunnelIfaceMap(t.ID)
		bindMap := getTunnelBindMap(t.ID)
		fNodes := make([]int64, 0, 1+len(path))
		fNodes = append(fNodes, inNode.ID)
		fNodes = append(fNodes, path...)
		// 为每个节点分配临时端口
		tmpPorts := make([]int, len(fNodes))
		rid := RandUUID()
		tmpNames := make([]string, len(fNodes))
		jlog(map[string]any{"event": "iperf3_path_nodes", "tunnelId": t.ID, "nodes": fNodes, "ports": tmpPorts})
        for i, nid := range fNodes {
			var n model.Node
			_ = db.DB.First(&n, nid).Error
			// overlay 优化：若上一跳出口IP与本跳入口IP均为 10.126.126.*，放宽端口范围限制，仅需 >=1000 且未占用
			prefer := n.PortSta
			if prefer <= 0 {
				prefer = 10000
			}
			if i > 0 {
				prevOut := ifaceMap[fNodes[i-1]]
				thisIn := bindMap[nid]
                if strings.HasPrefix(prevOut, "10.126.126.") && strings.HasPrefix(thisIn, "10.126.126.") {
                    // 叠加网络优化也保持端口 >=10000
                    p := findFreePortOnNodeAny(nid, 10000, 10000)
                    if p != 0 { tmpPorts[i] = p } else { tmpPorts[i] = 10000 }
                } else {
                    minP, maxP := 10000, 65535
                    if n.PortSta > 0 {
                        minP = n.PortSta
                    }
                    if n.PortEnd > 0 {
                        maxP = n.PortEnd
                    }
                    if prefer < minP {
                        prefer = minP
                    }
                    tmpPorts[i] = findFreePortOnNode(nid, prefer, minP, maxP)
                    if tmpPorts[i] == 0 {
                        tmpPorts[i] = minP
                    }
                }
            } else {
                // 第一个(入口临时)端口：若入口出站IP与下一跳入口IP(首个中间或出口)均为 overlay，则放宽；否则按节点范围
                prevOut := ifaceMap[nid]
                var nextIn string
				if len(fNodes) > 1 {
					nextIn = bindMap[fNodes[1]]
				} else if t.OutNodeID != nil {
					nextIn = bindMap[*t.OutNodeID]
				}
                if strings.HasPrefix(prevOut, "10.126.126.") && strings.HasPrefix(nextIn, "10.126.126.") {
                    // 叠加网络优化也保持端口 >=10000
                    p := findFreePortOnNodeAny(nid, 10000, 10000)
                    if p != 0 { tmpPorts[i] = p } else { tmpPorts[i] = 10000 }
                } else {
                    minP, maxP := 10000, 65535
                    if n.PortSta > 0 {
                        minP = n.PortSta
                    }
                    if n.PortEnd > 0 {
                        maxP = n.PortEnd
                    }
                    if prefer < minP {
                        prefer = minP
                    }
                    tmpPorts[i] = findFreePortOnNode(nid, prefer, minP, maxP)
                    if tmpPorts[i] == 0 {
                        tmpPorts[i] = minP
                    }
                }
            }
            // 兜底：确保临时端口不低于10000，避免与系统/保留端口冲突
            if tmpPorts[i] > 0 && tmpPorts[i] < 10000 {
                p := findFreePortOnNodeAny(nid, 10000, 10000)
                if p != 0 { tmpPorts[i] = p } else { tmpPorts[i] = 10000 }
            }
            tmpNames[i] = fmt.Sprintf("tmp_iperf3_%d_%s_%d", t.ID, rid, i)
            jlog(map[string]any{"event": "iperf3_tmp_port_pick", "tunnelId": t.ID, "nodeId": nid, "name": tmpNames[i], "port": tmpPorts[i]})
        }
		// 部署临时直转服务链
		for i := 0; i < len(fNodes); i++ {
			nid := fNodes[i]
			var target string
			if i < len(fNodes)-1 {
				var nx model.Node
				if db.DB.First(&nx, fNodes[i+1]).Error != nil {
					continue
				}
				// 连接下一跳时优先使用下一跳的绑定(入口)IP
				targetIP := bindMap[fNodes[i+1]]
				if targetIP == "" {
					targetIP = preferIPv4(nx)
				}
				target = safeHostPort(targetIP, tmpPorts[i+1])
			} else {
				target = safeHostPort(exitIP, srvPort)
			}
			var iface *string
			if ip, ok := ifaceMap[nid]; ok && ip != "" {
				tmp := ip
				iface = &tmp
			}
		svc := buildServiceConfig(tmpNames[i], tmpPorts[i], target, iface)
		// 为入口节点的临时服务绑定到 0.0.0.0，确保使用 IPv4 监听并避免仅 IPv6 监听导致本地不可达
		// 同时满足来自不同网络命名空间情况下的可见性需求
		if nid == inNode.ID {
			svc["addr"] = safeHostPort("0.0.0.0", tmpPorts[i])
		}
			_ = sendWSCommand(nid, "AddService", expandRUDP([]map[string]any{svc}))
			jlog(map[string]any{"event": "iperf3_tmp_add", "tunnelId": t.ID, "nodeId": nid, "name": tmpNames[i], "listen": tmpPorts[i], "target": target})
			// 记录到操作日志（每个节点gost临时通道配置）
			if b, err := json.Marshal(svc); err == nil {
				s := string(b)
				_ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: nid, Cmd: "DiagTmpServiceAdd", RequestID: diagID, Success: 1, Message: fmt.Sprintf("gost临时通道配置 name=%s", tmpNames[i]), Stdout: &s}).Error
			}
			// 尝试汇总 agent 的 GOST Web API 回执至操作日志（DiagTmpServiceAdd-recv）
			go func(nodeID int64, reqID string){
				t0 := time.Now().Add(-5 * time.Second).UnixMilli()
				time.Sleep(800 * time.Millisecond)
				// 抓取最近几条回执，优先匹配 services 接口，再退回到任意 gost_api/gost_api_err
				var logs []model.NodeOpLog
				_ = db.DB.Where("node_id = ? AND time_ms >= ? AND (cmd = ? OR cmd = ?)", nodeID, t0, "OpLog:gost_api", "OpLog:gost_api_err").Order("time_ms desc").Limit(10).Find(&logs).Error
				pick := model.NodeOpLog{}
				for _, it := range logs {
					if strings.Contains(it.Message, "/config/services") {
						pick = it; break
					}
				}
				if pick.ID == 0 && len(logs) > 0 { pick = logs[0] }
				if pick.ID > 0 {
					_ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: nodeID, Cmd: "DiagTmpServiceAdd-recv", RequestID: reqID, Success: 1, Message: pick.Message}).Error
				} else {
					_ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: nodeID, Cmd: "DiagTmpServiceAdd-recv", RequestID: reqID, Success: 0, Message: "未捕获到agent API回执（查看 OpLog:gost_api*）"}).Error
				}
			}(nid, diagID)
		}
		// 使用 Web API 动态添加临时服务，无需重启
		// 主动轮询各节点临时服务是否生效（最多 8 秒）
		readyAll := true
		for i := 0; i < len(fNodes); i++ {
			if !waitServiceOnNode(fNodes[i], tmpNames[i], tmpPorts[i], 8*time.Second) {
				readyAll = false
				jlog(map[string]any{"event": "iperf3_tmp_wait_timeout", "tunnelId": t.ID, "nodeId": fNodes[i], "name": tmpNames[i], "port": tmpPorts[i]})
				_ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: fNodes[i], Cmd: "DiagTmpWait", RequestID: diagID, Success: 0, Message: fmt.Sprintf("等待临时服务超时 name=%s port=%d", tmpNames[i], tmpPorts[i])}).Error
			} else {
				_ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: fNodes[i], Cmd: "DiagTmpWait", RequestID: diagID, Success: 1, Message: fmt.Sprintf("临时服务就绪 name=%s port=%d", tmpNames[i], tmpPorts[i])}).Error
			}
		}
		// 额外：逐跳 TCP 连通性验证（上一跳直连下一跳端口，最多重试3次）
		if readyAll {
			for i := 0; i+1 < len(fNodes); i++ {
				var nx model.Node
				_ = db.DB.First(&nx, fNodes[i+1]).Error
				host := preferIPv4(nx)
				okc := false
				for r := 0; r < 3 && !okc; r++ {
					_, _, okc, _ = diagnoseFromNode(fNodes[i], host, tmpPorts[i+1], 1, 1200)
					if !okc {
						time.Sleep(250 * time.Millisecond)
					}
				}
				if !okc {
					readyAll = false
					jlog(map[string]any{"event": "iperf3_tmp_tcp_fail", "tunnelId": t.ID, "from": fNodes[i], "to": host, "port": tmpPorts[i+1]})
					break
				}
			}
		}
        // 额外：读取入口节点 gost 配置文件内容用于诊断日志（best-effort）
        if readyAll {
            script := "#!/bin/sh\nset +e\nfor p in /etc/gost/gost.json /usr/local/gost/gost.json ./gost.json; do if [ -f \"$p\" ]; then echo \"PATH:$p\"; cat \"$p\"; exit 0; fi; done; echo 'PATH:NOT_FOUND'; exit 0\n"
            req := map[string]any{"requestId": RandUUID(), "timeoutSec": 8, "content": script}
            if res, ok := RequestOp(inNode.ID, "RunScript", req, 10*time.Second); ok {
                msg := "ok"
                var so string
                if d, _ := res["data"].(map[string]any); d != nil {
                    if m, _ := d["message"].(string); m != "" { msg = m }
                    if s, _ := d["stdout"].(string); s != "" { so = s }
                }
                if so != "" { _ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: inNode.ID, Cmd: "DiagGostConfigRead", RequestID: diagID, Success: 1, Message: msg, Stdout: &so}).Error } else { _ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: inNode.ID, Cmd: "DiagGostConfigRead", RequestID: diagID, Success: 1, Message: msg}).Error }
            } else {
                _ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: inNode.ID, Cmd: "DiagGostConfigRead", RequestID: diagID, Success: 0, Message: "未响应"}).Error
            }
        }
        if !readyAll {
            jlog(map[string]any{"event": "iperf3_tmp_ready_partial", "tunnelId": t.ID})
            // 清理临时服务
            for i := 0; i < len(fNodes); i++ {
                _ = sendWSCommand(fNodes[i], "DeleteService", map[string]any{"services": expandNamesWithRUDP([]string{tmpNames[i]})})
                _ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: fNodes[i], Cmd: "DiagTmpServiceDel", RequestID: diagID, Success: 1, Message: fmt.Sprintf("删除gost临时通道配置 name=%s", tmpNames[i])}).Error
            }
            resFail := map[string]any{"success": false, "description": "iperf3 反向带宽测试", "nodeName": inNode.Name, "nodeId": inNode.ID, "message": "临时通道未完全就绪，已中止 iperf3 测试", "diagId": diagID}
            c.JSON(http.StatusOK, response.Ok(resFail))
            return
        }
        // 3) 入口作为 iperf3 客户端，连接本机临时入口端口（先做一次直连出口TCP预检）
        // 预检：从入口直接 TCP 连出口 iperf3 端口，失败则中止
        avg0, loss0, ok0, msg0, _ := diagnoseFromNodeCtx(inNode.ID, exitIP, srvPort, 1, 1500, map[string]interface{}{"src": "tunnel", "step": "iperf3_tcp_probe", "tunnelId": t.ID})
        _ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: inNode.ID, Cmd: "DiagIperf3TcpProbe", RequestID: diagID, Success: ifThen(ok0, 1, 0), Message: fmt.Sprintf("TCP %s avg=%vms loss=%v%% msg=%s", ifThen(ok0, "ok", "fail"), avg0, loss0, msg0)}).Error
        if !ok0 {
			// 清理临时服务
            for i := 0; i < len(fNodes); i++ {
                _ = sendWSCommand(fNodes[i], "DeleteService", map[string]any{"services": expandNamesWithRUDP([]string{tmpNames[i]})})
                _ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: fNodes[i], Cmd: "DiagTmpServiceDel", RequestID: diagID, Success: 1, Message: fmt.Sprintf("删除gost临时通道配置 name=%s", tmpNames[i])}).Error
            }
            resFail := map[string]any{"success": false, "description": "iperf3 反向带宽测试", "nodeName": inNode.Name, "nodeId": inNode.ID, "message": "入口直连出口端口不可达，已中止iperf3测试", "diagId": diagID}
            c.JSON(http.StatusOK, response.Ok(resFail))
            return
        }
        // 预检：入口从 127.0.0.1 连本地临时入口端口（超时可配置，默认3s）
        localTimeout := readDiagLocalProbeTimeoutMs()
        avg1, loss1, ok1, msg1, _ := diagnoseFromNodeCtx(inNode.ID, "127.0.0.1", tmpPorts[0], 1, localTimeout, map[string]interface{}{"src": "tunnel", "step": "iperf3_local_probe", "tunnelId": t.ID})
		_ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: inNode.ID, Cmd: "DiagIperf3LocalProbe", RequestID: diagID, Success: ifThen(ok1, 1, 0), Message: fmt.Sprintf("LOCAL %s avg=%vms loss=%v%% msg=%s", ifThen(ok1, "ok", "fail"), avg1, loss1, msg1)}).Error
		if !ok1 {
			// 回读 QueryServices 的实际监听状态与地址，若处于 listening，则稍作等待后重试一次
			svcs := queryNodeServicesRaw(inNode.ID)
			found := false
			listening := false
			addr := ""
			for _, s := range svcs {
				name, _ := s["name"].(string)
				if name == tmpNames[0] {
					found = true
					if b, ok := s["listening"].(bool); ok {
						listening = b
					}
					if v, ok := s["addr"].(string); ok {
						addr = v
					}
					break
				}
				// 兜底按端口匹配
				if !found {
					if v, ok := s["port"].(int); ok && v == tmpPorts[0] {
						found = true
						if b, ok2 := s["listening"].(bool); ok2 {
							listening = b
						}
						if vv, ok2 := s["addr"].(string); ok2 {
							addr = vv
						}
					}
				}
			}
			info := fmt.Sprintf("found=%v listening=%v addr=%s", found, listening, addr)
			_ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: inNode.ID, Cmd: "DiagIperf3LocalProbe-recheck", RequestID: diagID, Success: ifThen(listening, 1, 0), Message: info}).Error
			if listening {
                // 小延迟再试一次
                time.Sleep(250 * time.Millisecond)
                avg1b, loss1b, ok1b, msg1b, _ := diagnoseFromNodeCtx(inNode.ID, "127.0.0.1", tmpPorts[0], 1, localTimeout, map[string]interface{}{"src": "tunnel", "step": "iperf3_local_probe_retry", "tunnelId": t.ID})
				_ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: inNode.ID, Cmd: "DiagIperf3LocalProbeRetry", RequestID: diagID, Success: ifThen(ok1b, 1, 0), Message: fmt.Sprintf("LOCAL %s avg=%vms loss=%v%% msg=%s", ifThen(ok1b, "ok", "fail"), avg1b, loss1b, msg1b)}).Error
				if ok1b {
					ok1 = true
				} else {
					// 保留原失败路径
        for i := 0; i < len(fNodes); i++ {
            _ = sendWSCommand(fNodes[i], "DeleteService", map[string]any{"services": expandNamesWithRUDP([]string{tmpNames[i]})})
            _ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: fNodes[i], Cmd: "DiagTmpServiceDel", RequestID: diagID, Success: 1, Message: fmt.Sprintf("删除gost临时通道配置 name=%s", tmpNames[i])}).Error
        }
					resFail := map[string]any{"success": false, "description": "iperf3 反向带宽测试", "nodeName": inNode.Name, "nodeId": inNode.ID, "message": "入口本地临时端口不可达，已中止iperf3测试", "diagId": diagID}
					c.JSON(http.StatusOK, response.Ok(resFail))
					return
				}
			} else {
				for i := 0; i < len(fNodes); i++ {
					_ = sendWSCommand(fNodes[i], "DeleteService", map[string]any{"services": []string{tmpNames[i]}})
					_ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: fNodes[i], Cmd: "DiagTmpServiceDel", RequestID: diagID, Success: 1, Message: fmt.Sprintf("删除gost临时通道配置 name=%s", tmpNames[i])}).Error
				}
				resFail := map[string]any{"success": false, "description": "iperf3 反向带宽测试", "nodeName": inNode.Name, "nodeId": inNode.ID, "message": "入口本地临时端口不可达，已中止iperf3测试", "diagId": diagID}
				c.JSON(http.StatusOK, response.Ok(resFail))
				return
			}
		}
        // 小延迟，确保链路稳定
        time.Sleep(300 * time.Millisecond)
        // 入口 iperf3 客户端
        cliReq := map[string]interface{}{"requestId": RandUUID(), "mode": "iperf3", "client": true, "host": "127.0.0.1", "port": tmpPorts[0], "duration": 5, "reverse": true, "ctx": map[string]any{"src": "tunnel", "step": "iperf3_client_path", "tunnelId": t.ID, "diagId": diagID}}
		_ = sendWSCommand(inNode.ID, "Diagnose", cliReq)
		cliRes, ok := RequestDiagnose(inNode.ID, cliReq, 20*time.Second)
        if !ok {
            _ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: inNode.ID, Cmd: "DiagIperf3Client-recv", RequestID: diagID, Success: 0, Message: "客户端未响应"}).Error
            resFail := map[string]any{"success": false, "description": "iperf3 反向带宽测试", "nodeName": inNode.Name, "nodeId": inNode.ID, "message": "入口节点未响应iperf3客户端", "diagId": diagID}
            c.JSON(http.StatusOK, response.Ok(resFail))
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
			if b2, err := json.Marshal(data); err == nil {
				s := string(b2)
				_ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: inNode.ID, Cmd: "DiagIperf3Client-recv", RequestID: diagID, Success: ifThen(success, 1, 0), Message: msgI, Stdout: &s}).Error
			}
		}
		// 诊断完成后清理临时服务
		for i := 0; i < len(fNodes); i++ {
			_ = sendWSCommand(fNodes[i], "DeleteService", map[string]any{"services": []string{tmpNames[i]}})
			_ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: fNodes[i], Cmd: "DiagTmpServiceDel", RequestID: diagID, Success: 1, Message: fmt.Sprintf("删除gost临时通道配置 name=%s", tmpNames[i])}).Error
		}
		// 结束后尝试关闭出口 iperf3 服务（best-effort）
		kill := map[string]any{"requestId": RandUUID(), "timeoutSec": 10, "content": fmt.Sprintf("#!/bin/sh\nset +e\npkill -f 'iperf3.*-s.*-p %d' >/dev/null 2>&1 || true\nexit 0\n", srvPort)}
		if res, ok := RequestOp(outNode.ID, "RunScript", kill, 12*time.Second); ok {
			msg := "ok"
			if d, _ := res["data"].(map[string]any); d != nil {
				if m, _ := d["message"].(string); m != "" {
					msg = m
				}
			}
			_ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: outNode.ID, Cmd: "DiagIperf3ServerStop", RequestID: diagID, Success: 1, Message: msg}).Error
		} else {
			_ = db.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: outNode.ID, Cmd: "DiagIperf3ServerStop", RequestID: diagID, Success: 0, Message: "未响应"}).Error
		}
		res = map[string]interface{}{
			"success": success, "description": "iperf3 反向带宽测试", "nodeName": inNode.Name, "nodeId": inNode.ID,
			"targetIp": exitIP, "targetPort": srvPort, "message": msgI, "bandwidthMbps": bw, "diagId": diagID,
		}
	default:
		c.JSON(http.StatusOK, response.ErrMsg("未知诊断步骤"))
		return
	}
	c.JSON(http.StatusOK, response.Ok(res))
}

// waitServiceOnNode polls QueryServices via WS to check if a temporary service is present.
// It matches by service name or by listening port.
func waitServiceOnNode(nodeID int64, name string, port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		svcs := queryNodeServicesRaw(nodeID)
		// match by name or port
		for _, s := range svcs {
			listening := false
			if b, ok := s["listening"].(bool); ok {
				listening = b
			}
			if n, ok := s["name"].(string); ok && n == name && listening {
				return true
			}
			if v, ok := s["addr"].(string); ok {
				if parsePort(v) == port && listening {
					return true
				}
			}
			if lst, ok := s["listener"].(map[string]any); ok {
				if v, ok2 := lst["addr"].(string); ok2 {
					if parsePort(v) == port && listening {
						return true
					}
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// preferIPv4 chooses an IPv4 address from Node fields if available; otherwise returns ServerIP
func preferIPv4(n model.Node) string {
	// Node.IP may contain comma-separated list
	if n.IP != "" {
		parts := strings.Split(n.IP, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" && !strings.Contains(p, ":") { // IPv4
				return p
			}
		}
	}
	// fallback: if ServerIP is IPv4
	if n.ServerIP != "" && !strings.Contains(n.ServerIP, ":") {
		return n.ServerIP
	}
	// last resort: ServerIP itself (IPv6 acceptable when both ends support it)
	return n.ServerIP
}

func ifThen[T any](cond bool, a T, b T) T {
	if cond {
		return a
	}
	return b
}
func ptrString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func orString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func defaultPortForProtocol(p *string) int {
	if p == nil {
		return 443
	}
	switch *p {
	case "tls", "wss", "mtls", "mwss":
		return 443
	case "tcp", "mtcp":
		return 80
	default:
		return 443
	}
}

func val(p *int, d int) int {
	if p == nil {
		return d
	}
	return *p
}
