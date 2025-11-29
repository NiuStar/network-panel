package controller

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	util "network-panel/golang-backend/internal/app/util"
	dbpkg "network-panel/golang-backend/internal/db"

	"github.com/gin-gonic/gin"
)

// POST /api/v1/forward/status
// Returns per-forward config state aggregated from agent-reported node services.
func ForwardStatusList(c *gin.Context) {
	var p struct {
		ForwardIds []int64 `json:"forwardIds"`
		UserId     *int64  `json:"userId"`
	}
	_ = c.ShouldBindJSON(&p)
	// auth: non-admin can only see own forwards
	if roleInf, ok := c.Get("role_id"); ok {
		if role, _ := roleInf.(int); role != 0 {
			if uidInf, ok2 := c.Get("user_id"); ok2 {
				if id, _ := uidInf.(int64); id > 0 {
					p.UserId = &id
				}
			}
		}
	}
	// read forwards filtered
	type row struct {
		model.Forward
		TType     int    `gorm:"column:t_type"`
		InNodeID  int64  `gorm:"column:in_node_id"`
		OutNodeID *int64 `gorm:"column:out_node_id"`
		TunnelID  int64  `gorm:"column:tunnel_id"`
		UserID    int64  `gorm:"column:user_id"`
	}
	q := dbpkg.DB.Table("forward f").
		Select("f.*, t.type as t_type, t.in_node_id, t.out_node_id").
		Joins("left join tunnel t on t.id = f.tunnel_id")
	if len(p.ForwardIds) > 0 {
		q = q.Where("f.id in ?", p.ForwardIds)
	}
	if p.UserId != nil {
		q = q.Where("f.user_id = ?", *p.UserId)
	}
	var rows []row
	q.Find(&rows)

	// strict list uses live fetch; no snapshot window needed

	type item struct {
		ForwardID int64 `json:"forwardId"`
		Ok        bool  `json:"ok"`
	}
	list := make([]item, 0, len(rows))

	for _, r := range rows {
		name := buildServiceName(r.ID, r.UserID, r.TunnelID)
		okAll := true
		// entry strict: same-name service with expected port listening
		entry := fetchServiceByName(r.InNodeID, name)
		if entry == nil {
			okAll = false
		} else {
			p := parsePortFromService(entry)
			l, haveL := getListeningFlag(entry)
			if !haveL && p > 0 {
				l = probePortListening(r.InNodeID, p)
				haveL = true
			}
			if !(p == r.InPort && haveL && l) {
				okAll = false
			}
		}
		// exit + mids for tunnel type 2
		if okAll && r.TType == 2 {
			if r.OutNodeID == nil {
				okAll = false
			}
			// exit
			if okAll {
				exit := fetchServiceByName(*r.OutNodeID, name)
				if exit == nil || r.OutPort == nil {
					okAll = false
				} else {
					p := parsePortFromService(exit)
					l, haveL := getListeningFlag(exit)
					if !haveL && p > 0 {
						l = probePortListening(*r.OutNodeID, p)
						haveL = true
					}
					if !(p == *r.OutPort && haveL && l) {
						okAll = false
					}
				}
			}
			// mids
			if okAll {
				// load expected mid ports
				expected := map[int]int{}
				var mids []model.ForwardMidPort
				dbpkg.DB.Where("forward_id = ?", r.ID).Find(&mids)
				for _, mp := range mids {
					expected[mp.Idx] = mp.Port
				}
				path := getTunnelPathNodes(r.TunnelID)
				for i, nid := range path {
					mid := fetchServiceByName(nid, fmt.Sprintf("%s_mid_%d", name, i))
					if mid == nil {
						okAll = false
						break
					}
					p := parsePortFromService(mid)
					l, haveL := getListeningFlag(mid)
					if !haveL && p > 0 {
						l = probePortListening(nid, p)
						haveL = true
					}
					if exp, ok := expected[i]; ok && exp > 0 {
						if !(p == exp && haveL && l) {
							okAll = false
							break
						}
					} else {
						if !(haveL && l) {
							okAll = false
							break
						}
					}
				}
			}
		}
		list = append(list, item{ForwardID: r.ID, Ok: okAll})
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{"list": list}))
}

// POST /api/v1/forward/status-detail {forwardId}
// Returns per-node status and, for missing/mismatched nodes, the actual GOST service object.
func ForwardStatusDetail(c *gin.Context) {
	var p struct {
		ForwardID int64 `json:"forwardId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	// auth: non-admin users can only view own forward
	var f model.Forward
	if err := dbpkg.DB.First(&f, p.ForwardID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("转发不存在"))
		return
	}
	if roleInf, ok := c.Get("role_id"); ok {
		if role, _ := roleInf.(int); role != 0 {
			if uidInf, ok2 := c.Get("user_id"); ok2 {
				if id, _ := uidInf.(int64); id > 0 && f.UserID != id {
					c.JSON(http.StatusForbidden, response.ErrMsg("权限不足"))
					return
				}
			}
		}
	}
	var t model.Tunnel
	_ = dbpkg.DB.First(&t, f.TunnelID).Error
	// node names cache
	nodeName := func(id int64) string {
		var n model.Node
		if err := dbpkg.DB.First(&n, id).Error; err == nil {
			return n.Name
		}
		return fmt.Sprintf("node-%d", id)
	}

	now := time.Now().UnixMilli()
	staleMs := int64(15 * 1000)
	if v := os.Getenv("FORWARD_STATUS_STALE_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			staleMs = int64(n)
		}
	}

	// no hash helpers needed for detail view with port/listening strict compare
	// helper: query one node's services and pick by name
	// use shared helper fetchServiceByName

	// load expected mid ports for this forward
	midPorts := map[int]int{}
	{
		var rows []model.ForwardMidPort
		dbpkg.DB.Where("forward_id = ?", f.ID).Find(&rows)
		for _, r := range rows {
			midPorts[r.Idx] = r.Port
		}
	}
	// build response
	type nodeItem struct {
		NodeID       int64          `json:"nodeId"`
		NodeName     string         `json:"nodeName"`
		Role         string         `json:"role"`
		Ok           bool           `json:"ok"`
		ExpectedPort *int           `json:"expectedPort,omitempty"`
		ActualPort   *int           `json:"actualPort,omitempty"`
		Listening    *bool          `json:"listening,omitempty"`
		Expected     map[string]any `json:"expected"`
		Actual       map[string]any `json:"actual,omitempty"`
	}
	out := struct {
		ForwardID int64      `json:"forwardId"`
		Nodes     []nodeItem `json:"nodes"`
	}{ForwardID: f.ID}

	name := buildServiceName(f.ID, f.UserID, f.TunnelID)
	// entry expected subset
	ifaceMap := getTunnelIfaceMap(t.ID)
	var inIface *string
	if ip, ok := ifaceMap[t.InNodeID]; ok && ip != "" {
		tmp := ip
		inIface = &tmp
	}
	expEntry := buildServiceConfig(name, f.InPort, f.RemoteAddr, inIface)
	expEntryMeta := map[string]any{"managedBy": "network-panel", "enableStats": true, "observer.period": "5s", "observer.resetTraffic": false}
	expEntry["metadata"] = expEntryMeta
	attachLimiter(expEntry, t.InNodeID)
	expEntryPort := f.InPort
	okEntry := false
	act := map[string]any(nil)
	actPort := -1
	listeningPtr := (*bool)(nil)
	if names, _, ts, ok := getNodeServiceSnapshot(t.InNodeID); ok && (now-ts) < staleMs {
		if _, present := names[name]; present {
			act = fetchServiceByName(t.InNodeID, name)
		}
	}
	// Fallback: if snapshot missing/stale or name not present, try live fetch
	if act == nil {
		if m := fetchServiceByName(t.InNodeID, name); m != nil {
			act = m
		}
	}
	if act != nil {
		p := parsePortFromService(act)
		actPort = p
		l, haveL := getListeningFlag(act)
		if !haveL && p > 0 {
			v := probePortListening(t.InNodeID, p)
			l = v
			haveL = true
		}
		if haveL {
			listeningPtr = &l
		}
		okEntry = (p == expEntryPort) && (listeningPtr != nil && *listeningPtr) && metadataMatches(act, expEntryMeta)
	}
	out.Nodes = append(out.Nodes, nodeItem{NodeID: t.InNodeID, NodeName: nodeName(t.InNodeID), Role: "entry", Ok: okEntry, ExpectedPort: &expEntryPort, ActualPort: intPtrOrNil(actPort), Listening: listeningPtr, Expected: expEntry, Actual: act})

	if t.Type == 2 {
		// exit expected
		if t.OutNodeID != nil {
			user := fmt.Sprintf("u-%d", f.ID)
			pass := util.MD5(fmt.Sprintf("%d:%d", f.ID, f.CreatedTime))[:16]
			outSvc := map[string]any{
				"name":     name,
				"addr":     fmt.Sprintf(":%d", *f.OutPort),
				"listener": map[string]any{"type": "grpc"},
				"handler":  map[string]any{"type": "relay", "auth": map[string]any{"username": user, "password": pass}},
				"metadata": map[string]any{"managedBy": "network-panel", "enableStats": true, "observer.period": "5s", "observer.resetTraffic": false},
			}
			expPort := *f.OutPort
			okOut := false
			oAct := map[string]any(nil)
			oPort := -1
			oListenPtr := (*bool)(nil)
			if names, _, ts, ok := getNodeServiceSnapshot(*t.OutNodeID); ok && (now-ts) < staleMs {
				if _, present := names[name]; present {
					oAct = fetchServiceByName(*t.OutNodeID, name)
				}
			}
			if oAct == nil {
				if m := fetchServiceByName(*t.OutNodeID, name); m != nil {
					oAct = m
				}
			}
			if oAct != nil {
				p := parsePortFromService(oAct)
				oPort = p
				l, haveL := getListeningFlag(oAct)
				if !haveL && p > 0 {
					v := probePortListening(*t.OutNodeID, p)
					l = v
					haveL = true
				}
				if haveL {
					oListenPtr = &l
				}
				okOut = (p == expPort) && (oListenPtr != nil && *oListenPtr)
			}
			out.Nodes = append(out.Nodes, nodeItem{NodeID: *t.OutNodeID, NodeName: nodeName(*t.OutNodeID), Role: "exit", Ok: okOut, ExpectedPort: &expPort, ActualPort: intPtrOrNil(oPort), Listening: oListenPtr, Expected: outSvc, Actual: oAct})
		}
		// mids
		path := getTunnelPathNodes(t.ID)
		bindMap := getTunnelBindMap(t.ID)
		for i, nid := range path {
			midName := fmt.Sprintf("%s_mid_%d", name, i)
			expMid := map[string]any{
				"name":     midName,
				"listener": map[string]any{"type": "tcp"},
				"handler":  map[string]any{"type": "forward"},
			}
			if ip, ok := bindMap[nid]; ok && ip != "" {
				expMid["metadata"] = map[string]any{"interface": ip}
			}
			okMid := false
			mAct := map[string]any(nil)
			mPort := -1
			mListenPtr := (*bool)(nil)
			if names, _, ts, ok := getNodeServiceSnapshot(nid); ok && (now-ts) < staleMs {
				if _, present := names[midName]; present {
					mAct = fetchServiceByName(nid, midName)
				}
			}
			if mAct == nil {
				if m := fetchServiceByName(nid, midName); m != nil {
					mAct = m
				}
			}
			if mAct != nil {
				p := parsePortFromService(mAct)
				mPort = p
				l, haveL := getListeningFlag(mAct)
				if !haveL && p > 0 {
					v := probePortListening(nid, p)
					l = v
					haveL = true
				}
				if haveL {
					mListenPtr = &l
				}
				// 对比端口：若存在持久化期望端口，则要求端口一致且在监听；否则仅要求在监听
				if exp, ok := midPorts[i]; ok && exp > 0 {
					okMid = (p == exp) && (mListenPtr != nil && *mListenPtr)
				} else {
					okMid = (mListenPtr != nil && *mListenPtr)
				}
			}
			var expPortPtr *int
			if exp, ok := midPorts[i]; ok && exp > 0 {
				expPortPtr = &exp
			}
			out.Nodes = append(out.Nodes, nodeItem{NodeID: nid, NodeName: nodeName(nid), Role: "mid", Ok: okMid, ExpectedPort: expPortPtr, ActualPort: intPtrOrNil(mPort), Listening: mListenPtr, Expected: expMid, Actual: mAct})
		}
	}
	c.JSON(http.StatusOK, response.Ok(out))
}

// helpers for detail comparison
func parsePortFromService(m map[string]any) int {
	if v, ok := m["port"].(float64); ok {
		return int(v)
	}
	if v, ok := m["port"].(int); ok {
		return v
	}
	if s, ok := m["addr"].(string); ok && s != "" {
		// parse trailing :port
		xs := s
		// strip ipv6 brackets if present [ip]:port
		if strings.HasPrefix(xs, "[") {
			if i := strings.LastIndex(xs, "]:"); i >= 0 {
				xs = xs[i+2:]
			} else {
				return 0
			}
		} else if i := strings.LastIndex(xs, ":"); i >= 0 {
			xs = xs[i+1:]
		} else {
			return 0
		}
		if n, err := strconv.Atoi(xs); err == nil {
			return n
		}
	}
	return 0
}

func getListeningFlag(m map[string]any) (bool, bool) {
	if b, ok := m["listening"].(bool); ok {
		return b, true
	}
	if b, ok := m["listening"].(float64); ok {
		return b != 0, true
	}
	return false, false
}

// metadataMatches checks that actual.metadata contains all key/values from expected.
func metadataMatches(actual map[string]any, expected map[string]any) bool {
	if len(expected) == 0 {
		return true
	}
	metaRaw, ok := actual["metadata"]
	if !ok {
		return false
	}
	var meta map[string]any
	switch t := metaRaw.(type) {
	case map[string]any:
		meta = t
	default:
		return false
	}
	for k, v := range expected {
		if mv, ok := meta[k]; ok {
			switch exp := v.(type) {
			case bool:
				if vb, ok2 := toBool(mv); !ok2 || vb != exp {
					return false
				}
			case string:
				if fmt.Sprint(mv) != exp {
					return false
				}
			default:
				if fmt.Sprint(mv) != fmt.Sprint(exp) {
					return false
				}
			}
		} else {
			return false
		}
	}
	return true
}

func toBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case float64:
		return x != 0, true
	case int:
		return x != 0, true
	case string:
		l := strings.ToLower(strings.TrimSpace(x))
		if l == "true" || l == "1" || l == "yes" || l == "on" {
			return true, true
		}
		if l == "false" || l == "0" || l == "no" || l == "off" {
			return false, true
		}
	}
	return false, false
}

func probePortListening(nodeID int64, port int) bool {
	reqID := RandUUID()
	payload := map[string]interface{}{"requestId": reqID, "port": port}
	if err := sendWSCommand(nodeID, "ProbePort", payload); err != nil {
		return false
	}
	ch := make(chan map[string]interface{}, 1)
	diagMu.Lock()
	diagWaiters[reqID] = ch
	diagMu.Unlock()
	select {
	case res := <-ch:
		if data, _ := res["data"].(map[string]any); data != nil {
			if v, _ := data["listening"].(bool); v {
				return true
			}
		}
	case <-time.After(3 * time.Second):
	}
	return false
}

func intPtrOrNil(v int) *int {
	if v <= 0 {
		return nil
	}
	return &v
}

// shared helpers
func fetchServiceByName(nodeID int64, name string) map[string]any {
	reqID := RandUUID()
	payload := map[string]any{"requestId": reqID, "name": name}
	// prefer precise GetService if agent supports it
	if err := sendWSCommand(nodeID, "GetService", payload); err == nil {
		ch := make(chan map[string]interface{}, 1)
		diagMu.Lock()
		diagWaiters[reqID] = ch
		diagMu.Unlock()
		select {
		case res := <-ch:
			if data, ok := res["data"].(map[string]any); ok && data != nil {
				if v, _ := data["name"].(string); v == name {
					return data
				}
			}
		case <-time.After(3 * time.Second):
		}
	}
	// fallback to QueryServices
	if err := sendWSCommand(nodeID, "QueryServices", payload); err != nil {
		return nil
	}
	ch := make(chan map[string]interface{}, 1)
	diagMu.Lock()
	diagWaiters[reqID] = ch
	diagMu.Unlock()
	select {
	case res := <-ch:
		if data, ok := res["data"].([]interface{}); ok {
			for _, it := range data {
				if m, ok2 := it.(map[string]any); ok2 {
					if v, _ := m["name"].(string); v == name {
						return m
					}
				}
			}
		}
	case <-time.After(3 * time.Second):
	}
	return nil
}
