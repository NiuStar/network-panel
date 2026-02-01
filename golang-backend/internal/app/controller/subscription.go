package controller

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	"network-panel/golang-backend/internal/app/util"
	dbpkg "network-panel/golang-backend/internal/db"
	tpl "network-panel/golang-backend/template"
)

type subProxy struct {
	ID       int64
	Name     string
	Group    string
	Type     string
	Server   string
	Port     int
	Cipher   string
	Password string
	Params   map[string]interface{}
}

type subSkip struct {
	ID     int64
	Name   string
	Group  string
	Reason string
}

// SubscriptionClash returns clash config for user
// @Summary Clash 订阅
// @Tags subscription
// @Produce text/plain
// @Router /api/v1/subscription/clash [get]
func SubscriptionClash(c *gin.Context) {
	user, items, skipped, ok := subscriptionItems(c)
	if !ok {
		return
	}
	c.Header("subscription-userinfo", buildSubHeader(user.OutFlow, user.InFlow, user.Flow*1024*1024*1024, valOr0(user.ExpTime)/1000))
	cfg := buildClashConfig(items, skipped)
	c.Data(http.StatusOK, "text/yaml; charset=utf-8", []byte(cfg))
}

// SubscriptionClashMeta returns clash meta config for user
// @Summary Clash Meta 订阅
// @Tags subscription
// @Produce text/plain
// @Router /api/v1/subscription/clash-meta [get]
func SubscriptionClashMeta(c *gin.Context) {
	user, items, skipped, ok := subscriptionItems(c)
	if !ok {
		return
	}
	c.Header("subscription-userinfo", buildSubHeader(user.OutFlow, user.InFlow, user.Flow*1024*1024*1024, valOr0(user.ExpTime)/1000))
	cfg := buildClashMetaConfig(items, skipped)
	c.Data(http.StatusOK, "text/yaml; charset=utf-8", []byte(cfg))
}

// SubscriptionShadowrocket returns shadowrocket compatible subscription
// @Summary Shadowrocket 订阅
// @Tags subscription
// @Produce text/plain
// @Router /api/v1/subscription/shadowrocket [get]
func SubscriptionShadowrocket(c *gin.Context) {
	user, items, skipped, ok := subscriptionItems(c)
	if !ok {
		return
	}
	c.Header("subscription-userinfo", buildSubHeader(user.OutFlow, user.InFlow, user.Flow*1024*1024*1024, valOr0(user.ExpTime)/1000))
	cfg := buildShadowrocket(items, skipped)
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(cfg))
}

// SubscriptionSurge returns surge config (ver=5/6)
// @Summary Surge 订阅
// @Tags subscription
// @Produce text/plain
// @Router /api/v1/subscription/surge [get]
func SubscriptionSurge(c *gin.Context) {
	user, items, skipped, ok := subscriptionItems(c)
	if !ok {
		return
	}
	ver := strings.TrimSpace(c.DefaultQuery("ver", "5"))
	c.Header("subscription-userinfo", buildSubHeader(user.OutFlow, user.InFlow, user.Flow*1024*1024*1024, valOr0(user.ExpTime)/1000))
	cfg := buildSurgeConfig(items, skipped, ver, requestFullURL(c))
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(cfg))
}

// SubscriptionSingbox returns sing-box subscription
// @Summary Sing-box 订阅
// @Tags subscription
// @Produce application/json
// @Router /api/v1/subscription/singbox [get]
func SubscriptionSingbox(c *gin.Context) {
	user, items, skipped, ok := subscriptionItems(c)
	if !ok {
		return
	}
	c.Header("subscription-userinfo", buildSubHeader(user.OutFlow, user.InFlow, user.Flow*1024*1024*1024, valOr0(user.ExpTime)/1000))
	cfg := buildSingboxConfig(items, skipped)
	c.Data(http.StatusOK, "application/json; charset=utf-8", []byte(cfg))
}

// SubscriptionV2ray returns v2ray compatible subscription
// @Summary V2Ray 订阅
// @Tags subscription
// @Produce text/plain
// @Router /api/v1/subscription/v2ray [get]
func SubscriptionV2ray(c *gin.Context) {
	user, items, skipped, ok := subscriptionItems(c)
	if !ok {
		return
	}
	c.Header("subscription-userinfo", buildSubHeader(user.OutFlow, user.InFlow, user.Flow*1024*1024*1024, valOr0(user.ExpTime)/1000))
	cfg := buildV2raySubscription(items, skipped)
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(cfg))
}

// SubscriptionQX returns Quantumult X subscription
// @Summary Quantumult X 订阅
// @Tags subscription
// @Produce text/plain
// @Router /api/v1/subscription/qx [get]
func SubscriptionQX(c *gin.Context) {
	user, items, skipped, ok := subscriptionItems(c)
	if !ok {
		return
	}
	c.Header("subscription-userinfo", buildSubHeader(user.OutFlow, user.InFlow, user.Flow*1024*1024*1024, valOr0(user.ExpTime)/1000))
	cfg := buildQXConfig(items, skipped)
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(cfg))
}

func subscriptionItems(c *gin.Context) (model.User, []subProxy, []subSkip, bool) {
	token := extractToken(c)
	if token == "" || !util.ValidateToken(token) {
		c.JSON(http.StatusUnauthorized, response.ErrMsg("未登录或token无效"))
		return model.User{}, nil, nil, false
	}
	uid := util.GetUserID(token)
	role := util.GetRoleID(token)
	var user model.User
	if err := dbpkg.DB.First(&user, uid).Error; err != nil {
		c.JSON(http.StatusUnauthorized, response.ErrMsg("未登录或token无效"))
		return model.User{}, nil, nil, false
	}

	var forwards []model.Forward
	q := dbpkg.DB.Model(&model.Forward{})
	if role != 0 {
		q = q.Where("user_id = ?", uid)
	}
	// only active forwards (status is nil or 1)
	q = q.Where("status IS NULL OR status = 1")
	if err := q.Find(&forwards).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("获取转发失败"))
		return model.User{}, nil, nil, false
	}
	if len(forwards) == 0 {
		return user, []subProxy{}, []subSkip{}, true
	}

	tunnelIDs := make([]int64, 0, len(forwards))
	tunnelSet := map[int64]struct{}{}
	for _, f := range forwards {
		if f.TunnelID <= 0 {
			continue
		}
		if _, ok := tunnelSet[f.TunnelID]; ok {
			continue
		}
		tunnelSet[f.TunnelID] = struct{}{}
		tunnelIDs = append(tunnelIDs, f.TunnelID)
	}
	var tunnels []model.Tunnel
	if len(tunnelIDs) > 0 {
		dbpkg.DB.Where("id IN ?", tunnelIDs).Find(&tunnels)
	}
	tunnelMap := map[int64]model.Tunnel{}
	inNodeIDs := map[int64]struct{}{}
	outNodeIDs := map[int64]struct{}{}
	outExitIDs := map[int64]struct{}{}
	for _, t := range tunnels {
		tunnelMap[t.ID] = t
		if t.InNodeID > 0 {
			inNodeIDs[t.InNodeID] = struct{}{}
		}
		if t.OutNodeID != nil && *t.OutNodeID > 0 {
			outNodeIDs[*t.OutNodeID] = struct{}{}
		}
		if t.Type == 1 && (t.OutNodeID == nil || *t.OutNodeID == 0) && t.OutExitID == nil {
			if t.InNodeID > 0 {
				outNodeIDs[t.InNodeID] = struct{}{}
			}
		}
		if t.OutExitID != nil && *t.OutExitID > 0 {
			outExitIDs[*t.OutExitID] = struct{}{}
		}
	}

	nodeIDs := make([]int64, 0, len(inNodeIDs))
	for id := range inNodeIDs {
		nodeIDs = append(nodeIDs, id)
	}
	var nodes []model.Node
	if len(nodeIDs) > 0 {
		dbpkg.DB.Where("id IN ?", nodeIDs).Find(&nodes)
	}
	nodeMap := map[int64]model.Node{}
	for _, n := range nodes {
		nodeMap[n.ID] = n
	}

	outNodeIDList := make([]int64, 0, len(outNodeIDs))
	for id := range outNodeIDs {
		outNodeIDList = append(outNodeIDList, id)
	}
	ssMap := map[int64]model.ExitSetting{}
	anyTLSMap := map[int64]model.AnyTLSSetting{}
	if len(outNodeIDList) > 0 {
		var ss []model.ExitSetting
		dbpkg.DB.Where("node_id IN ?", outNodeIDList).Find(&ss)
		for _, s := range ss {
			ssMap[s.NodeID] = s
		}
		var ats []model.AnyTLSSetting
		dbpkg.DB.Where("node_id IN ?", outNodeIDList).Find(&ats)
		for _, a := range ats {
			anyTLSMap[a.NodeID] = a
		}
	}

	extMap := map[int64]model.ExitNodeExternal{}
	if len(outExitIDs) > 0 {
		ids := make([]int64, 0, len(outExitIDs))
		for id := range outExitIDs {
			ids = append(ids, id)
		}
		var exts []model.ExitNodeExternal
		dbpkg.DB.Where("id IN ?", ids).Find(&exts)
		for _, e := range exts {
			extMap[e.ID] = e
		}
	}

	items := make([]subProxy, 0, len(forwards))
	skipped := make([]subSkip, 0)
	usedNames := map[string]int{}
	for _, f := range forwards {
		baseName := strings.TrimSpace(f.Name)
		if baseName == "" {
			baseName = fmt.Sprintf("forward-%d", f.ID)
		}
		baseGroup := strings.TrimSpace(f.Group)
		if baseGroup == "" {
			baseGroup = "默认"
		}
		t, ok := tunnelMap[f.TunnelID]
		if !ok {
			skipped = append(skipped, subSkip{
				ID:     f.ID,
				Name:   baseName,
				Group:  baseGroup,
				Reason: "隧道不存在或已删除",
			})
			continue
		}
		entryNode := nodeMap[t.InNodeID]
		entryHost := subscriptionEntryHost(t, entryNode)
		if entryHost == "" {
			skipped = append(skipped, subSkip{
				ID:     f.ID,
				Name:   baseName,
				Group:  baseGroup,
				Reason: "入口IP为空",
			})
			continue
		}
		if f.InPort <= 0 {
			skipped = append(skipped, subSkip{
				ID:     f.ID,
				Name:   baseName,
				Group:  baseGroup,
				Reason: "入口端口无效",
			})
			continue
		}
		var ext *model.ExitNodeExternal
		if t.OutExitID != nil {
			if e, ok := extMap[*t.OutExitID]; ok {
				ext = &e
			}
		}
		proto, cipher, password, params := resolveExitProtocol(t, ext, ssMap, anyTLSMap)
		if normalizeProtocol(proto) == "anytls" && user.ID > 0 {
			if paramString(params, "password") == "" {
				derived := anytlsUserPassword(password, user.ID)
				if derived != "" {
					password = derived
					if params == nil {
						params = map[string]interface{}{}
					}
					params["password"] = derived
				}
			} else {
				password = paramString(params, "password")
			}
		}
		if strings.TrimSpace(proto) == "" {
			skipped = append(skipped, subSkip{
				ID:     f.ID,
				Name:   baseName,
				Group:  baseGroup,
				Reason: "出口协议为空",
			})
			continue
		}
		if !isSupportedProtocol(proto) {
			reason := fmt.Sprintf("不支持协议:%s", proto)
			if strings.EqualFold(strings.TrimSpace(proto), "tls") {
				reason = "协议为 tls，未映射，请在出口节点里维护真实协议"
			}
			skipped = append(skipped, subSkip{
				ID:     f.ID,
				Name:   baseName,
				Group:  baseGroup,
				Reason: reason,
			})
			continue
		}
		if proto == "ss" && (cipher == "" || password == "") {
			skipped = append(skipped, subSkip{
				ID:     f.ID,
				Name:   baseName,
				Group:  baseGroup,
				Reason: "SS缺少加密或密码",
			})
			continue
		}
		if proto == "anytls" && password == "" {
			skipped = append(skipped, subSkip{
				ID:     f.ID,
				Name:   baseName,
				Group:  baseGroup,
				Reason: "AnyTLS缺少密码",
			})
			continue
		}
		if !requiredParamsReady(proto, subProxy{Type: proto, Cipher: cipher, Password: password, Params: params}, params) {
			skipped = append(skipped, subSkip{
				ID:     f.ID,
				Name:   baseName,
				Group:  baseGroup,
				Reason: "协议参数不完整",
			})
			continue
		}
		group := baseGroup
		name := uniqueName(baseName, f.ID, usedNames)
		items = append(items, subProxy{
			ID:       f.ID,
			Name:     name,
			Group:    group,
			Type:     proto,
			Server:   entryHost,
			Port:     f.InPort,
			Cipher:   cipher,
			Password: password,
			Params:   params,
		})
	}
	return user, items, skipped, true
}

func extractToken(c *gin.Context) string {
	token := strings.TrimSpace(c.Query("token"))
	if token == "" {
		token = strings.TrimSpace(c.GetHeader("Authorization"))
		if strings.HasPrefix(strings.ToLower(token), "bearer ") {
			token = strings.TrimSpace(token[7:])
		}
	}
	if token == "" {
		token = strings.TrimSpace(c.GetHeader("token"))
	}
	return token
}

func subscriptionEntryHost(t model.Tunnel, n model.Node) string {
	if t.InIP != "" && t.InIP != "0.0.0.0" && t.InIP != "::" {
		return t.InIP
	}
	if n.ServerIP != "" {
		return n.ServerIP
	}
	if n.IP != "" {
		return n.IP
	}
	return t.InIP
}

func parseExitConfig(raw *string) map[string]interface{} {
	if raw == nil {
		return map[string]interface{}{}
	}
	cfg := strings.TrimSpace(*raw)
	if cfg == "" || cfg == "null" {
		return map[string]interface{}{}
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(cfg), &out); err != nil {
		return map[string]interface{}{}
	}
	return out
}

func normalizeProtocol(proto string) string {
	p := strings.ToLower(strings.TrimSpace(proto))
	p = strings.ReplaceAll(p, " ", "")
	p = strings.ReplaceAll(p, "_", "")
	p = strings.ReplaceAll(p, "-", "")
	switch p {
	case "shadowsocks":
		return "ss"
	case "shadowtls":
		return "shadowtls"
	case "socks":
		return "socks5"
	case "tuicv5":
		return "tuic"
	case "hy2", "hysteria2":
		return "hysteria2"
	default:
		return p
	}
}

func resolveExitProtocol(t model.Tunnel, ext *model.ExitNodeExternal, ssMap map[int64]model.ExitSetting, anyTLSMap map[int64]model.AnyTLSSetting) (string, string, string, map[string]interface{}) {
	if ext != nil {
		params := parseExitConfig(ext.Config)
		proto := normalizeProtocol(ptrString(ext.Protocol))
		if proto == "" {
			if v, ok := params["protocol"].(string); ok {
				proto = normalizeProtocol(v)
			}
		}
		if proto == "" {
			if v, ok := params["type"].(string); ok {
				proto = normalizeProtocol(v)
			}
		}
		if proto == "" {
			proto = normalizeProtocol(ptrString(t.Protocol))
		}
		return proto, "", "", params
	}
	outID := outNodeIDOr0(t)
	if outID == 0 && t.InNodeID > 0 {
		outID = t.InNodeID
	}
	preferred := normalizeProtocol(ptrString(t.Protocol))
	if preferred == "" {
		if _, ok := ssMap[outID]; ok {
			preferred = "ss"
		} else if _, ok := anyTLSMap[outID]; ok {
			preferred = "anytls"
		}
	}
	switch preferred {
	case "ss":
		if s, ok := ssMap[outID]; ok {
			return "ss", strings.TrimSpace(s.Method), s.Password, map[string]interface{}{}
		}
	case "anytls":
		if a, ok := anyTLSMap[outID]; ok {
			return "anytls", "", a.Password, map[string]interface{}{}
		}
	default:
		return preferred, "", "", map[string]interface{}{}
	}
	return "", "", "", map[string]interface{}{}
}

func isSupportedProtocol(proto string) bool {
	switch normalizeProtocol(proto) {
	case "ss", "anytls", "socks5", "http", "https", "vmess", "vless", "trojan", "hysteria", "hysteria2", "hy2", "tuic", "snell", "wireguard", "ssh", "shadowtls", "ssr", "naive", "juicity", "mieru", "unknown":
		return true
	default:
		return false
	}
}

func uniqueName(base string, id int64, used map[string]int) string {
	if base == "" {
		base = fmt.Sprintf("forward-%d", id)
	}
	if _, ok := used[base]; !ok {
		used[base] = 1
		return base
	}
	used[base]++
	return fmt.Sprintf("%s-%d", base, used[base])
}

func buildSkipStrings(skipped []subSkip) []string {
	out := make([]string, 0, len(skipped))
	for _, s := range skipped {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			name = fmt.Sprintf("forward-%d", s.ID)
		}
		group := strings.TrimSpace(s.Group)
		if group == "" {
			group = "默认"
		}
		reason := strings.TrimSpace(s.Reason)
		if reason == "" {
			reason = "未知原因"
		}
		out = append(out, fmt.Sprintf("skip[%d] %s (group=%s): %s", s.ID, name, group, reason))
	}
	return out
}

func buildSkipLines(skipped []subSkip, prefix string) []string {
	if len(skipped) == 0 {
		return nil
	}
	lines := make([]string, 0, len(skipped)+1)
	if prefix == "" {
		prefix = "#"
	}
	lines = append(lines, fmt.Sprintf("%s ---- skipped ----", prefix))
	for _, s := range buildSkipStrings(skipped) {
		lines = append(lines, fmt.Sprintf("%s %s", prefix, s))
	}
	return lines
}

func appendSkipComments(out string, skipped []subSkip, prefix string) string {
	if len(skipped) == 0 {
		return out
	}
	lines := buildSkipLines(skipped, prefix)
	if len(lines) == 0 {
		return out
	}
	if strings.TrimSpace(out) == "" {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines, "\n") + "\n" + strings.TrimRight(out, "\n")
}

func buildClashConfig(items []subProxy, skipped []subSkip) string {
	tmpl, ok := tpl.Load("clash.yaml")
	if !ok || strings.TrimSpace(tmpl) == "" {
		return buildClashConfigLegacy(items, skipped)
	}
	proxies, groupMap := renderClashProxies(items)
	out := applyClashTemplate(tmpl, proxies, groupMap)
	if strings.TrimSpace(out) == "" {
		return buildClashConfigLegacy(items, skipped)
	}
	return appendSkipComments(out, skipped, "#")
}

func writeClashAnyTLS(buf *bytes.Buffer, it subProxy, params map[string]interface{}) {
	pass := it.Password
	if pass == "" {
		pass = paramString(params, "password")
	}
	buf.WriteString("    password: " + yamlQuote(pass) + "\n")
	buf.WriteString("    udp: " + strconv.FormatBool(paramBoolDefault(params, "udp", true)) + "\n")
	buf.WriteString("    client-fingerprint: " + yamlQuote(paramStringDefault(params, "client-fingerprint", "chrome")) + "\n")
	buf.WriteString("    idle-session-check-interval: " + strconv.Itoa(paramIntDefault(params, "idle-session-check-interval", 30)) + "\n")
	buf.WriteString("    idle-session-timeout: " + strconv.Itoa(paramIntDefault(params, "idle-session-timeout", 30)) + "\n")
	buf.WriteString("    min-idle-session: " + strconv.Itoa(paramIntDefault(params, "min-idle-session", 0)) + "\n")
	buf.WriteString("    sni: " + yamlQuote(paramStringDefault(params, "sni", "www.apple.com")) + "\n")
	buf.WriteString("    skip-cert-verify: " + strconv.FormatBool(paramBoolDefault(params, "skip-cert-verify", true)) + "\n")
}

func buildClashConfigLegacy(items []subProxy, skipped []subSkip) string {
	if len(items) == 0 {
		return appendSkipComments("proxies: []\nproxy-groups: []\nrules:\n  - MATCH,DIRECT\n", skipped, "#")
	}
	// group proxies (allow multiple groups)
	groupMap := map[string][]string{}
	for _, it := range items {
		for _, g := range splitGroupNames(it.Group) {
			groupMap[g] = append(groupMap[g], it.Name)
		}
	}
	groupNames := make([]string, 0, len(groupMap))
	for g := range groupMap {
		groupNames = append(groupNames, g)
	}
	sort.Strings(groupNames)

	var buf bytes.Buffer
	buf.WriteString("port: 7890\n")
	buf.WriteString("socks-port: 7891\n")
	buf.WriteString("allow-lan: true\n")
	buf.WriteString("mode: rule\n")
	buf.WriteString("log-level: info\n")
	buf.WriteString("proxies:\n")
	for _, it := range items {
		typ := normalizeProtocol(it.Type)
		params := it.Params
		if params == nil {
			params = map[string]interface{}{}
		}
		if typ == "unknown" {
			if v := paramString(params, "surgeType"); v != "" {
				typ = normalizeProtocol(v)
			} else if v := paramString(params, "type"); v != "" {
				typ = normalizeProtocol(v)
			}
		}
		if typ == "unknown" {
			if v := paramString(params, "clashType"); v != "" {
				typ = normalizeProtocol(v)
			} else if v := paramString(params, "type"); v != "" {
				typ = normalizeProtocol(v)
			}
		}
		if !requiredParamsReady(typ, it, params) {
			continue
		}
		buf.WriteString("  - name: " + yamlName(it.Name) + "\n")
		buf.WriteString("    type: " + typ + "\n")
		buf.WriteString("    server: " + yamlQuote(it.Server) + "\n")
		buf.WriteString("    port: " + strconv.Itoa(it.Port) + "\n")
		switch typ {
		case "ss":
			cipher := it.Cipher
			if cipher == "" {
				cipher = paramString(params, "cipher")
			}
			pass := it.Password
			if pass == "" {
				pass = paramString(params, "password")
			}
			buf.WriteString("    cipher: " + yamlQuote(cipher) + "\n")
			buf.WriteString("    password: " + yamlQuote(pass) + "\n")
			buf.WriteString("    udp: true\n")
		case "anytls":
			writeClashAnyTLS(&buf, it, params)
		}
		skip := map[string]struct{}{
			"name": {}, "type": {}, "server": {}, "port": {},
		}
		if typ == "ss" {
			skip["cipher"] = struct{}{}
			skip["password"] = struct{}{}
		}
		if typ == "anytls" {
			skip["password"] = struct{}{}
			skip["udp"] = struct{}{}
			skip["client-fingerprint"] = struct{}{}
			skip["idle-session-check-interval"] = struct{}{}
			skip["idle-session-timeout"] = struct{}{}
			skip["min-idle-session"] = struct{}{}
			skip["sni"] = struct{}{}
			skip["skip-cert-verify"] = struct{}{}
		}
		appendClashParams(&buf, params, skip)
	}
	buf.WriteString("proxy-groups:\n")
	// global group referencing each group
	buf.WriteString("  - name: \"GLOBAL\"\n")
	buf.WriteString("    type: select\n")
	buf.WriteString("    proxies:\n")
	for _, g := range groupNames {
		buf.WriteString("      - " + yamlName(g) + "\n")
	}
	buf.WriteString("      - DIRECT\n")
	for _, g := range groupNames {
		buf.WriteString("  - name: " + yamlName(g) + "\n")
		buf.WriteString("    type: select\n")
		buf.WriteString("    proxies:\n")
		for _, n := range groupMap[g] {
			buf.WriteString("      - " + yamlName(n) + "\n")
		}
		buf.WriteString("      - DIRECT\n")
	}
	buf.WriteString("rules:\n")
	buf.WriteString("  - MATCH,GLOBAL\n")
	return appendSkipComments(buf.String(), skipped, "#")
}

func buildClashMetaConfig(items []subProxy, skipped []subSkip) string {
	// Clash Meta uses the same base schema for proxies/proxy-groups
	return buildClashConfig(items, skipped)
}

func buildShadowrocket(items []subProxy, skipped []subSkip) string {
	lines := make([]string, 0, len(items))
	for _, it := range items {
		typ := normalizeProtocol(it.Type)
		params := it.Params
		if params == nil {
			params = map[string]interface{}{}
		}
		switch typ {
		case "ss":
			if it.Cipher == "" || it.Password == "" {
				it.Cipher = paramString(params, "cipher")
				it.Password = paramString(params, "password")
			}
			if it.Cipher == "" || it.Password == "" {
				continue
			}
			raw := fmt.Sprintf("%s:%s@%s:%d", it.Cipher, it.Password, it.Server, it.Port)
			enc := base64.StdEncoding.EncodeToString([]byte(raw))
			lines = append(lines, "ss://"+enc+"#"+url.QueryEscape(it.Name))
		case "anytls":
			pass := it.Password
			if pass == "" {
				pass = paramString(params, "password")
			}
			if pass == "" {
				continue
			}
			lines = append(lines, "anytls://"+url.QueryEscape(pass)+"@"+it.Server+":"+strconv.Itoa(it.Port)+"#"+url.QueryEscape(it.Name))
		case "socks5":
			lines = append(lines, "socks5://"+it.Server+":"+strconv.Itoa(it.Port)+"#"+url.QueryEscape(it.Name))
		case "http", "https":
			lines = append(lines, it.Type+"://"+it.Server+":"+strconv.Itoa(it.Port)+"#"+url.QueryEscape(it.Name))
		default:
			if raw := paramString(params, "uri"); raw != "" {
				lines = append(lines, raw)
			}
		}
	}
	out := strings.Join(lines, "\n")
	return appendSkipComments(out, skipped, "#")
}

func buildSurgeConfig(items []subProxy, skipped []subSkip, ver string, sourceURL string) string {
	tmpl, ok := tpl.Load("surge.surgeconfig")
	if !ok || strings.TrimSpace(tmpl) == "" {
		return buildSurgeConfigLegacy(items, skipped, ver, sourceURL)
	}
	if sourceURL == "" {
		sourceURL = "https://example.com"
	}
	if !strings.Contains(tmpl, "#!MANAGED-CONFIG") {
		tmpl = "#!MANAGED-CONFIG " + sourceURL + " interval=86400\n\n" + strings.TrimLeft(tmpl, "\n")
	}
	proxies, groupMap := renderSurgeProxies(items)
	out := strings.ReplaceAll(tmpl, "{{PROXIES}}", strings.TrimRight(proxies, "\n"))
	groupByKey, canonical := normalizeGroupMap(groupMap)
	typeMap, defaultType := parseGroupTypeDirectives(tmpl)
	out, templateGroups := applySurgeGroupProxies(out, groupByKey, canonical)
	extra := buildSurgeExtraGroups(groupByKey, canonical, templateGroups, typeMap, defaultType)
	out = strings.ReplaceAll(out, "{{EXTRA_GROUPS}}", strings.TrimRight(extra, "\n"))
	if strings.TrimSpace(out) == "" {
		return buildSurgeConfigLegacy(items, skipped, ver, sourceURL)
	}
	return appendSkipComments(out, skipped, ";")
}

func buildSurgeConfigLegacy(items []subProxy, skipped []subSkip, ver string, sourceURL string) string {
	var buf bytes.Buffer
	if sourceURL == "" {
		sourceURL = "https://example.com"
	}
	buf.WriteString("#!MANAGED-CONFIG ")
	buf.WriteString(sourceURL)
	buf.WriteString(" interval=86400\n\n")
	buf.WriteString("[Proxy]\n")
	groupMap := map[string][]string{}
	for _, it := range items {
		for _, g := range splitGroupNames(it.Group) {
			groupMap[g] = append(groupMap[g], it.Name)
		}
		typ := normalizeProtocol(it.Type)
		params := it.Params
		if params == nil {
			params = map[string]interface{}{}
		}
		if raw := paramString(params, "surge"); raw != "" {
			buf.WriteString(formatSurgeLine(raw, it))
			continue
		}
		switch typ {
		case "ss":
			if it.Cipher == "" || it.Password == "" {
				it.Cipher = paramString(params, "cipher")
				it.Password = paramString(params, "password")
			}
			if it.Cipher == "" || it.Password == "" {
				continue
			}
			buf.WriteString(fmt.Sprintf("%s = ss, %s, %d, encrypt-method=%s, password=%s\n", it.Name, it.Server, it.Port, it.Cipher, it.Password))
		case "anytls":
			pass := it.Password
			if pass == "" {
				pass = paramString(params, "password")
			}
			if pass == "" {
				continue
			}
			buf.WriteString(fmt.Sprintf("%s = anytls, %s, %d, password=%s, tfo=true, skip-cert-verify=true\n", it.Name, it.Server, it.Port, pass))
		case "socks5":
			buf.WriteString(fmt.Sprintf("%s = socks5, %s, %d\n", it.Name, it.Server, it.Port))
		case "http", "https":
			buf.WriteString(fmt.Sprintf("%s = %s, %s, %d\n", it.Name, it.Server, it.Port))
		default:
			line := buildSurgeGenericLine(typ, it, params)
			if line != "" {
				buf.WriteString(line)
			}
		}
	}
	groupNames := make([]string, 0, len(groupMap))
	for g := range groupMap {
		groupNames = append(groupNames, g)
	}
	sort.Strings(groupNames)
	buf.WriteString("\n[Proxy Group]\n")
	if len(groupNames) > 0 {
		buf.WriteString("GLOBAL = select")
		for _, g := range groupNames {
			buf.WriteString(", " + g)
		}
		buf.WriteString(", DIRECT\n")
		for _, g := range groupNames {
			buf.WriteString(g + " = select")
			for _, n := range groupMap[g] {
				buf.WriteString(", " + n)
			}
			buf.WriteString(", DIRECT\n")
		}
	}
	buf.WriteString("\n[Rule]\n")
	if ver == "6" {
		buf.WriteString("FINAL, GLOBAL\n")
	} else {
		buf.WriteString("FINAL, GLOBAL\n")
	}
	return appendSkipComments(buf.String(), skipped, ";")
}

func renderClashProxies(items []subProxy) (string, map[string][]string) {
	groupMap := map[string][]string{}
	var buf bytes.Buffer
	for _, it := range items {
		typ := normalizeProtocol(it.Type)
		params := it.Params
		if params == nil {
			params = map[string]interface{}{}
		}
		if typ == "unknown" {
			if v := paramString(params, "surgeType"); v != "" {
				typ = normalizeProtocol(v)
			} else if v := paramString(params, "type"); v != "" {
				typ = normalizeProtocol(v)
			}
		}
		if typ == "unknown" {
			if v := paramString(params, "clashType"); v != "" {
				typ = normalizeProtocol(v)
			} else if v := paramString(params, "type"); v != "" {
				typ = normalizeProtocol(v)
			}
		}
		if !requiredParamsReady(typ, it, params) {
			continue
		}
		buf.WriteString("  - name: " + yamlName(it.Name) + "\n")
		buf.WriteString("    type: " + typ + "\n")
		buf.WriteString("    server: " + yamlQuote(it.Server) + "\n")
		buf.WriteString("    port: " + strconv.Itoa(it.Port) + "\n")
		switch typ {
		case "ss":
			cipher := it.Cipher
			if cipher == "" {
				cipher = paramString(params, "cipher")
			}
			pass := it.Password
			if pass == "" {
				pass = paramString(params, "password")
			}
			buf.WriteString("    cipher: " + yamlQuote(cipher) + "\n")
			buf.WriteString("    password: " + yamlQuote(pass) + "\n")
			buf.WriteString("    udp: true\n")
		case "anytls":
			writeClashAnyTLS(&buf, it, params)
		}
		skip := map[string]struct{}{
			"name": {}, "type": {}, "server": {}, "port": {},
		}
		if typ == "ss" {
			skip["cipher"] = struct{}{}
			skip["password"] = struct{}{}
		}
		if typ == "anytls" {
			skip["password"] = struct{}{}
			skip["udp"] = struct{}{}
			skip["client-fingerprint"] = struct{}{}
			skip["idle-session-check-interval"] = struct{}{}
			skip["idle-session-timeout"] = struct{}{}
			skip["min-idle-session"] = struct{}{}
			skip["sni"] = struct{}{}
			skip["skip-cert-verify"] = struct{}{}
		}
		appendClashParams(&buf, params, skip)
		for _, g := range splitGroupNames(it.Group) {
			groupMap[g] = append(groupMap[g], it.Name)
		}
	}
	if buf.Len() == 0 {
		return "  []", groupMap
	}
	return strings.TrimRight(buf.String(), "\n"), groupMap
}

func applyClashTemplate(tmpl string, proxies string, groupMap map[string][]string) string {
	out := strings.ReplaceAll(tmpl, "{{PROXIES}}", proxies)
	groupByKey, canonical := normalizeGroupMap(groupMap)
	typeMap, defaultType := parseGroupTypeDirectives(tmpl)
	re := regexp.MustCompile(`\{\{GROUP:([^}]+)\}\}`)
	templateGroups := map[string]struct{}{}
	allProxyNames := collectAllProxyNames(groupByKey, canonical)
	nodeSelectKey := normalizeGroupKey("节点选择")
	out = re.ReplaceAllStringFunc(out, func(match string) string {
		sub := re.FindStringSubmatch(match)
		if len(sub) < 2 {
			return ""
		}
		raw := strings.TrimSpace(sub[1])
		if raw == "" {
			return ""
		}
		group := strings.TrimSpace(strings.SplitN(raw, "|", 2)[0])
		key := normalizeGroupKey(group)
		if key != "" {
			templateGroups[key] = struct{}{}
		}
		if key == nodeSelectKey {
			return formatClashGroupList(allProxyNames, "      ")
		}
		return formatClashGroupList(groupByKey[key], "      ")
	})
	extra := buildExtraClashGroups(groupByKey, canonical, templateGroups, typeMap, defaultType)
	out = strings.ReplaceAll(out, "{{EXTRA_GROUPS}}", strings.TrimRight(extra, "\n"))
	return out
}

func collectAllProxyNames(groupByKey map[string][]string, canonical map[string]string) []string {
	if len(groupByKey) == 0 {
		return nil
	}
	keys := make([]string, 0, len(groupByKey))
	for key := range groupByKey {
		if key == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return canonical[keys[i]] < canonical[keys[j]]
	})
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, key := range keys {
		for _, name := range groupByKey[key] {
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}

func formatClashGroupList(names []string, indent string) string {
	if len(names) == 0 {
		return ""
	}
	var buf bytes.Buffer
	for _, name := range names {
		buf.WriteString(indent + "- " + yamlName(name) + "\n")
	}
	return strings.TrimRight(buf.String(), "\n")
}

func buildExtraClashGroups(groupMap map[string][]string, canonical map[string]string, templateGroups map[string]struct{}, typeMap map[string]string, defaultType string) string {
	if len(groupMap) == 0 {
		return ""
	}
	groupKeys := make([]string, 0, len(groupMap))
	for key := range groupMap {
		if key == "" {
			continue
		}
		if _, ok := templateGroups[key]; ok {
			continue
		}
		groupKeys = append(groupKeys, key)
	}
	sort.Slice(groupKeys, func(i, j int) bool {
		return canonical[groupKeys[i]] < canonical[groupKeys[j]]
	})
	var buf bytes.Buffer
	for _, key := range groupKeys {
		names := groupMap[key]
		if len(names) == 0 {
			continue
		}
		name := canonical[key]
		if name == "" {
			name = key
		}
		typ := clashGroupTypeFromSpec(typeMap[key], defaultType)
		buf.WriteString("  - name: " + yamlName(name) + "\n")
		buf.WriteString("    type: " + typ + "\n")
		buf.WriteString("    proxies:\n")
		buf.WriteString("      - DIRECT\n")
		for _, n := range names {
			buf.WriteString("      - " + yamlName(n) + "\n")
		}
		buf.WriteString("\n")
	}
	return strings.TrimRight(buf.String(), "\n")
}

func renderSurgeProxies(items []subProxy) (string, map[string][]string) {
	groupMap := map[string][]string{}
	var buf bytes.Buffer
	for _, it := range items {
		typ := normalizeProtocol(it.Type)
		params := it.Params
		if params == nil {
			params = map[string]interface{}{}
		}
		line := ""
		if raw := paramString(params, "surge"); raw != "" {
			line = formatSurgeLine(raw, it)
		} else {
			switch typ {
			case "ss":
				if it.Cipher == "" || it.Password == "" {
					it.Cipher = paramString(params, "cipher")
					it.Password = paramString(params, "password")
				}
				if it.Cipher == "" || it.Password == "" {
					continue
				}
				line = fmt.Sprintf("%s = ss, %s, %d, encrypt-method=%s, password=%s\n", it.Name, it.Server, it.Port, it.Cipher, it.Password)
			case "anytls":
				pass := it.Password
				if pass == "" {
					pass = paramString(params, "password")
				}
				if pass == "" {
					continue
				}
				line = fmt.Sprintf("%s = anytls, %s, %d, password=%s, tfo=true, skip-cert-verify=true\n", it.Name, it.Server, it.Port, pass)
			case "socks5":
				line = fmt.Sprintf("%s = socks5, %s, %d\n", it.Name, it.Server, it.Port)
			case "http", "https":
				line = fmt.Sprintf("%s = %s, %s, %d\n", it.Name, it.Type, it.Server, it.Port)
			default:
				line = buildSurgeGenericLine(typ, it, params)
			}
		}
		if line == "" {
			continue
		}
		if !strings.HasSuffix(line, "\n") {
			line += "\n"
		}
		buf.WriteString(line)
		for _, g := range splitGroupNames(it.Group) {
			groupMap[g] = append(groupMap[g], it.Name)
		}
	}
	return strings.TrimRight(buf.String(), "\n"), groupMap
}

func applySurgeGroupProxies(content string, groupMap map[string][]string, canonical map[string]string) (string, map[string]struct{}) {
	lines := strings.Split(content, "\n")
	inGroup := false
	templateGroups := map[string]struct{}{}
	proxyIncludeGroups := buildSurgeProxyIncludeGroups(canonical)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]"))
			inGroup = strings.EqualFold(section, "Proxy Group")
			continue
		}
		if !inGroup || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		group := strings.TrimSpace(parts[0])
		if group == "" {
			continue
		}
		key := normalizeGroupKey(group)
		if key == "" {
			continue
		}
		templateGroups[key] = struct{}{}
		names := groupMap[key]
		if len(names) == 0 {
			continue
		}
		existing := map[string]struct{}{}
		for _, token := range strings.Split(parts[1], ",") {
			existing[strings.TrimSpace(token)] = struct{}{}
		}
		appendList := make([]string, 0, len(names))
		for _, name := range names {
			if _, ok := existing[name]; ok {
				continue
			}
			appendList = append(appendList, name)
		}
		if len(appendList) == 0 {
			if strings.EqualFold(group, "Proxy") && len(proxyIncludeGroups) > 0 {
				lines[i] = appendIncludeOtherGroup(line, proxyIncludeGroups)
			}
			continue
		}
		nextLine := strings.TrimRight(line, " ") + ", " + strings.Join(appendList, ", ")
		if strings.EqualFold(group, "Proxy") && len(proxyIncludeGroups) > 0 {
			nextLine = appendIncludeOtherGroup(nextLine, proxyIncludeGroups)
		}
		lines[i] = nextLine
	}
	return strings.Join(lines, "\n"), templateGroups
}

func buildSurgeProxyIncludeGroups(canonical map[string]string) []string {
	if len(canonical) == 0 {
		return nil
	}
	names := make([]string, 0, len(canonical))
	for key, name := range canonical {
		if key == "" || name == "" {
			continue
		}
		if strings.EqualFold(key, "proxy") || strings.EqualFold(strings.TrimSpace(name), "proxy") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func appendIncludeOtherGroup(line string, groups []string) string {
	if len(groups) == 0 {
		return line
	}
	reQuoted := regexp.MustCompile(`(?i)include-other-group\\s*=\\s*\"([^\"]*)\"`)
	if match := reQuoted.FindStringSubmatch(line); len(match) > 1 {
		merged := mergeGroupLists(splitGroupList(match[1]), groups)
		if len(merged) == 0 {
			return line
		}
		return reQuoted.ReplaceAllString(line, fmt.Sprintf(`include-other-group="%s"`, strings.Join(merged, ", ")))
	}
	rePlain := regexp.MustCompile(`(?i)include-other-group\\s*=\\s*([^,]+)`)
	if match := rePlain.FindStringSubmatch(line); len(match) > 1 {
		merged := mergeGroupLists(splitGroupList(match[1]), groups)
		if len(merged) == 0 {
			return line
		}
		return rePlain.ReplaceAllString(line, fmt.Sprintf(`include-other-group="%s"`, strings.Join(merged, ", ")))
	}
	merged := mergeGroupLists(nil, groups)
	if len(merged) == 0 {
		return line
	}
	return strings.TrimRight(line, " ") + `, include-other-group="` + strings.Join(merged, ", ") + `"`
}

func splitGroupList(raw string) []string {
	if raw == "" {
		return nil
	}
	raw = strings.ReplaceAll(raw, "，", ",")
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func mergeGroupLists(base []string, extra []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(base)+len(extra))
	for _, v := range base {
		key := strings.TrimSpace(v)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	for _, v := range extra {
		key := strings.TrimSpace(v)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func buildSurgeExtraGroups(groupMap map[string][]string, canonical map[string]string, templateGroups map[string]struct{}, typeMap map[string]string, defaultType string) string {
	if len(groupMap) == 0 {
		return ""
	}
	groupKeys := make([]string, 0, len(groupMap))
	for key := range groupMap {
		if key == "" {
			continue
		}
		if _, ok := templateGroups[key]; ok {
			continue
		}
		groupKeys = append(groupKeys, key)
	}
	sort.Slice(groupKeys, func(i, j int) bool {
		return canonical[groupKeys[i]] < canonical[groupKeys[j]]
	})
	var buf bytes.Buffer
	for _, key := range groupKeys {
		names := groupMap[key]
		if len(names) == 0 {
			continue
		}
		name := canonical[key]
		if name == "" {
			name = key
		}
		spec := surgeGroupSpec(typeMap[key], defaultType)
		buf.WriteString(name + " = " + spec)
		for _, name := range names {
			buf.WriteString(", " + name)
		}
		buf.WriteString(", DIRECT\n")
	}
	return strings.TrimRight(buf.String(), "\n")
}

func normalizeGroupKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func splitGroupNames(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{"默认"}
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', '，', ';', '；', '/', '|':
			return true
		default:
			return false
		}
	})
	seen := map[string]struct{}{}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		v := strings.TrimSpace(p)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return []string{"默认"}
	}
	return out
}

func normalizeGroupMap(groupMap map[string][]string) (map[string][]string, map[string]string) {
	byKey := map[string][]string{}
	canonical := map[string]string{}
	if len(groupMap) == 0 {
		return byKey, canonical
	}
	keys := make([]string, 0, len(groupMap))
	for k := range groupMap {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return normalizeGroupKey(keys[i]) < normalizeGroupKey(keys[j])
	})
	seen := map[string]map[string]struct{}{}
	for _, name := range keys {
		key := normalizeGroupKey(name)
		if key == "" {
			continue
		}
		if _, ok := canonical[key]; !ok {
			canonical[key] = name
		}
		if _, ok := seen[key]; !ok {
			seen[key] = map[string]struct{}{}
		}
		for _, proxy := range groupMap[name] {
			if proxy == "" {
				continue
			}
			if _, ok := seen[key][proxy]; ok {
				continue
			}
			seen[key][proxy] = struct{}{}
			byKey[key] = append(byKey[key], proxy)
		}
	}
	return byKey, canonical
}

func parseGroupTypeDirectives(content string) (map[string]string, string) {
	result := map[string]string{}
	defaultType := ""
	lines := strings.Split(content, "\n")
	re := regexp.MustCompile(`(?i)^\s*[#;]\s*group-type\s*:\s*(.+)$`)
	for _, line := range lines {
		m := re.FindStringSubmatch(line)
		if len(m) < 2 {
			continue
		}
		raw := strings.TrimSpace(m[1])
		if raw == "" {
			continue
		}
		entries := splitDirectiveEntries(raw)
		for _, entry := range entries {
			part := strings.TrimSpace(entry)
			if part == "" {
				continue
			}
			key, val, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			name := normalizeGroupKey(key)
			typ := strings.TrimSpace(val)
			if name == "" || typ == "" {
				continue
			}
			if name == "*" || name == "default" {
				defaultType = typ
				continue
			}
			result[name] = typ
		}
	}
	return result, defaultType
}

func splitDirectiveEntries(raw string) []string {
	if strings.Contains(raw, ";") {
		return strings.Split(raw, ";")
	}
	return strings.Split(raw, ",")
}

func clashGroupTypeFromSpec(spec string, fallback string) string {
	value := strings.TrimSpace(spec)
	if value == "" {
		value = strings.TrimSpace(fallback)
	}
	if value == "" {
		return "select"
	}
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	if len(fields) == 0 {
		return "select"
	}
	return fields[0]
}

func surgeGroupSpec(spec string, fallback string) string {
	value := strings.TrimSpace(spec)
	if value == "" {
		value = strings.TrimSpace(fallback)
	}
	if value == "" {
		return "select"
	}
	return value
}

func requestFullURL(c *gin.Context) string {
	scheme := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto"))
	if scheme == "" {
		if c.Request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	if host := c.Request.Host; host != "" {
		return scheme + "://" + host + c.Request.RequestURI
	}
	return ""
}

func yamlQuote(s string) string {
	return strconv.Quote(s)
}

func yamlName(s string) string {
	if yamlBareSafe(s) {
		return s
	}
	return strconv.Quote(s)
}

func yamlBareSafe(s string) bool {
	if s == "" {
		return false
	}
	if strings.TrimSpace(s) != s {
		return false
	}
	if strings.ContainsAny(s, "\t\r\n:#{}[],&*?|<>=!%@$`\"'") {
		return false
	}
	switch s[0] {
	case '-', '?', ':', '@', '!', '*', '&', ',', '#':
		return false
	}
	return true
}

func paramString(params map[string]interface{}, key string) string {
	if params == nil {
		return ""
	}
	if v, ok := params[key]; ok {
		switch t := v.(type) {
		case string:
			return strings.TrimSpace(t)
		case []string:
			return strings.Join(t, ",")
		case []interface{}:
			parts := make([]string, 0, len(t))
			for _, item := range t {
				parts = append(parts, fmt.Sprintf("%v", item))
			}
			return strings.Join(parts, ",")
		case bool:
			if t {
				return "true"
			}
			return "false"
		case float64:
			if t == float64(int64(t)) {
				return strconv.FormatInt(int64(t), 10)
			}
			return strconv.FormatFloat(t, 'f', -1, 64)
		case int:
			return strconv.Itoa(t)
		case int64:
			return strconv.FormatInt(t, 10)
		case fmt.Stringer:
			return strings.TrimSpace(t.String())
		}
	}
	return ""
}

func requiredParamsReady(proto string, it subProxy, params map[string]interface{}) bool {
	switch normalizeProtocol(proto) {
	case "ss":
		if it.Cipher == "" {
			it.Cipher = paramString(params, "cipher")
		}
		if it.Password == "" {
			it.Password = paramString(params, "password")
		}
		return it.Cipher != "" && it.Password != ""
	case "anytls":
		if it.Password == "" {
			it.Password = paramString(params, "password")
		}
		return it.Password != ""
	case "vmess", "vless", "tuic":
		return paramString(params, "uuid") != ""
	case "trojan", "hysteria", "hysteria2", "hy2", "snell":
		return paramString(params, "password") != "" || paramString(params, "psk") != ""
	default:
		return true
	}
}

func appendClashParams(buf *bytes.Buffer, params map[string]interface{}, skip map[string]struct{}) {
	if params == nil || len(params) == 0 {
		return
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		if _, ok := skip[k]; ok {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		writeYamlKey(buf, k, params[k], 4)
	}
}

func writeYamlKey(buf *bytes.Buffer, key string, value interface{}, indent int) {
	pad := strings.Repeat(" ", indent)
	switch v := value.(type) {
	case nil:
		return
	case string:
		buf.WriteString(pad + key + ": " + yamlQuote(v) + "\n")
	case bool:
		if v {
			buf.WriteString(pad + key + ": true\n")
		} else {
			buf.WriteString(pad + key + ": false\n")
		}
	case int:
		buf.WriteString(pad + key + ": " + strconv.Itoa(v) + "\n")
	case int64:
		buf.WriteString(pad + key + ": " + strconv.FormatInt(v, 10) + "\n")
	case float64:
		if v == float64(int64(v)) {
			buf.WriteString(pad + key + ": " + strconv.FormatInt(int64(v), 10) + "\n")
		} else {
			buf.WriteString(pad + key + ": " + strconv.FormatFloat(v, 'f', -1, 64) + "\n")
		}
	case []interface{}:
		buf.WriteString(pad + key + ":\n")
		for _, item := range v {
			writeYamlItem(buf, item, indent+2)
		}
	case []string:
		buf.WriteString(pad + key + ":\n")
		for _, item := range v {
			writeYamlItem(buf, item, indent+2)
		}
	case map[string]interface{}:
		buf.WriteString(pad + key + ":\n")
		writeYamlMap(buf, v, indent+2)
	default:
		buf.WriteString(pad + key + ": " + yamlQuote(fmt.Sprintf("%v", v)) + "\n")
	}
}

func writeYamlMap(buf *bytes.Buffer, m map[string]interface{}, indent int) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		writeYamlKey(buf, k, m[k], indent)
	}
}

func writeYamlItem(buf *bytes.Buffer, value interface{}, indent int) {
	pad := strings.Repeat(" ", indent)
	switch v := value.(type) {
	case map[string]interface{}:
		buf.WriteString(pad + "-\n")
		writeYamlMap(buf, v, indent+2)
	case string:
		buf.WriteString(pad + "- " + yamlQuote(v) + "\n")
	case bool:
		if v {
			buf.WriteString(pad + "- true\n")
		} else {
			buf.WriteString(pad + "- false\n")
		}
	case int:
		buf.WriteString(pad + "- " + strconv.Itoa(v) + "\n")
	case int64:
		buf.WriteString(pad + "- " + strconv.FormatInt(v, 10) + "\n")
	case float64:
		if v == float64(int64(v)) {
			buf.WriteString(pad + "- " + strconv.FormatInt(int64(v), 10) + "\n")
		} else {
			buf.WriteString(pad + "- " + strconv.FormatFloat(v, 'f', -1, 64) + "\n")
		}
	default:
		buf.WriteString(pad + "- " + yamlQuote(fmt.Sprintf("%v", v)) + "\n")
	}
}

func formatSurgeLine(raw string, it subProxy) string {
	line := strings.ReplaceAll(raw, "{name}", it.Name)
	line = strings.ReplaceAll(line, "{server}", it.Server)
	line = strings.ReplaceAll(line, "{port}", strconv.Itoa(it.Port))
	if strings.HasSuffix(line, "\n") {
		return line
	}
	return line + "\n"
}

func buildSurgeGenericLine(typ string, it subProxy, params map[string]interface{}) string {
	if typ == "" {
		return ""
	}
	parts := []string{it.Name + " = " + typ, it.Server, strconv.Itoa(it.Port)}
	appendParam := func(k, v string) {
		if v == "" {
			return
		}
		parts = append(parts, k+"="+v)
	}
	if u := paramString(params, "uuid"); u != "" {
		appendParam("uuid", u)
	}
	if u := paramString(params, "username"); u != "" {
		appendParam("username", u)
	}
	if u := paramString(params, "password"); u != "" {
		appendParam("password", u)
	}
	if u := paramString(params, "psk"); u != "" {
		appendParam("psk", u)
	}
	if u := paramString(params, "cipher"); u != "" {
		appendParam("encrypt-method", u)
	}
	if u := paramString(params, "sni"); u != "" {
		appendParam("sni", u)
	}
	if u := paramString(params, "alpn"); u != "" {
		appendParam("alpn", u)
	}
	if u := paramString(params, "flow"); u != "" {
		appendParam("flow", u)
	}
	if u := paramString(params, "tls"); u != "" {
		appendParam("tls", u)
	}
	if u := paramString(params, "skip-cert-verify"); u != "" {
		appendParam("skip-cert-verify", u)
	}
	if ws, ok := params["ws-opts"].(map[string]interface{}); ok {
		appendParam("ws", "true")
		if path, ok := ws["path"]; ok {
			appendParam("ws-path", paramString(map[string]interface{}{"v": path}, "v"))
		}
		if headers, ok := ws["headers"].(map[string]interface{}); ok {
			if host, ok := headers["Host"]; ok {
				appendParam("ws-headers", "Host:"+paramString(map[string]interface{}{"v": host}, "v"))
			}
		}
	}
	if grpc, ok := params["grpc-opts"].(map[string]interface{}); ok {
		appendParam("grpc", "true")
		if svc, ok := grpc["grpc-service-name"]; ok {
			appendParam("grpc-service-name", paramString(map[string]interface{}{"v": svc}, "v"))
		}
	}
	return strings.Join(parts, ", ") + "\n"
}

func buildSingboxConfig(items []subProxy, skipped []subSkip) string {
	if len(items) == 0 {
		if len(skipped) == 0 {
			return "{}"
		}
	}
	groupMap := map[string][]string{}
	outbounds := make([]map[string]interface{}, 0, len(items))
	for _, it := range items {
		typ := normalizeProtocol(it.Type)
		params := it.Params
		if params == nil {
			params = map[string]interface{}{}
		}
		if _, ok := params["singbox"]; !ok {
			if !requiredParamsReady(typ, it, params) {
				continue
			}
		}
		ob := buildSingboxOutbound(typ, it, params)
		if len(ob) == 0 {
			continue
		}
		outbounds = append(outbounds, ob)
		for _, g := range splitGroupNames(it.Group) {
			groupMap[g] = append(groupMap[g], it.Name)
		}
	}
	if len(outbounds) == 0 {
		if len(skipped) == 0 {
			return "{}"
		}
		cfg := map[string]interface{}{
			"outbounds":   []interface{}{},
			"__skipped__": buildSkipStrings(skipped),
		}
		raw, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return "{}"
		}
		return string(raw)
	}
	groupNames := make([]string, 0, len(groupMap))
	for g := range groupMap {
		groupNames = append(groupNames, g)
	}
	sort.Strings(groupNames)

	// selectors
	for _, g := range groupNames {
		outbounds = append(outbounds, map[string]interface{}{
			"type":      "selector",
			"tag":       g,
			"outbounds": append(groupMap[g], "DIRECT"),
		})
	}
	outbounds = append(outbounds, map[string]interface{}{
		"type":      "selector",
		"tag":       "GLOBAL",
		"outbounds": append(groupNames, "DIRECT"),
	})
	outbounds = append(outbounds,
		map[string]interface{}{"type": "direct", "tag": "DIRECT"},
		map[string]interface{}{"type": "block", "tag": "BLOCK"},
	)

	if len(outbounds) == 0 {
		if len(skipped) == 0 {
			return "{}"
		}
		cfg := map[string]interface{}{
			"outbounds":   []interface{}{},
			"__skipped__": buildSkipStrings(skipped),
		}
		raw, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return "{}"
		}
		return string(raw)
	}

	cfg := map[string]interface{}{
		"outbounds": outbounds,
		"route": map[string]interface{}{
			"final": "GLOBAL",
		},
	}
	if len(skipped) > 0 {
		cfg["__skipped__"] = buildSkipStrings(skipped)
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func buildV2raySubscription(items []subProxy, skipped []subSkip) string {
	lines := make([]string, 0, len(items))
	for _, it := range items {
		typ := normalizeProtocol(it.Type)
		params := it.Params
		if params == nil {
			params = map[string]interface{}{}
		}
		if raw := paramString(params, "v2ray"); raw != "" {
			lines = append(lines, raw)
			continue
		}
		uri := buildV2rayURI(typ, it, params)
		if uri == "" {
			continue
		}
		lines = append(lines, uri)
	}
	skipLines := buildSkipLines(skipped, "#")
	if len(lines) == 0 {
		if len(skipLines) == 0 {
			return ""
		}
		lines = append(lines, skipLines...)
	} else if len(skipLines) > 0 {
		lines = append(skipLines, lines...)
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n")))
}

func buildQXConfig(items []subProxy, skipped []subSkip) string {
	lines := make([]string, 0, len(items))
	for _, it := range items {
		typ := normalizeProtocol(it.Type)
		params := it.Params
		if params == nil {
			params = map[string]interface{}{}
		}
		line := buildQXLine(typ, it, params)
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		if len(skipped) == 0 {
			return ""
		}
		return strings.Join(buildSkipLines(skipped, ";"), "\n")
	}
	out := strings.Join(lines, "\n")
	return appendSkipComments(out, skipped, ";")
}

func buildSingboxOutbound(typ string, it subProxy, params map[string]interface{}) map[string]interface{} {
	if raw := params["singbox"]; raw != nil {
		switch v := raw.(type) {
		case string:
			var out map[string]interface{}
			if err := json.Unmarshal([]byte(v), &out); err == nil {
				if _, ok := out["tag"]; !ok {
					out["tag"] = it.Name
				}
				return out
			}
		case map[string]interface{}:
			if _, ok := v["tag"]; !ok {
				v["tag"] = it.Name
			}
			return v
		}
	}
	base := map[string]interface{}{
		"type":   typ,
		"tag":    it.Name,
		"server": it.Server,
	}
	if it.Port > 0 {
		base["server_port"] = it.Port
	}
	switch normalizeProtocol(typ) {
	case "ss":
		base["type"] = "shadowsocks"
		cipher := it.Cipher
		if cipher == "" {
			cipher = paramString(params, "cipher")
		}
		pass := it.Password
		if pass == "" {
			pass = paramString(params, "password")
		}
		if cipher == "" || pass == "" {
			return map[string]interface{}{}
		}
		base["method"] = cipher
		base["password"] = pass
	case "anytls":
		pass := it.Password
		if pass == "" {
			pass = paramString(params, "password")
		}
		if pass == "" {
			return map[string]interface{}{}
		}
		base["type"] = "anytls"
		base["password"] = pass
		sni := paramString(params, "sni")
		if sni == "" {
			sni = "www.apple.com"
		}
		insecure := paramBool(params, "skip-cert-verify") || paramBool(params, "insecure")
		base["tls"] = map[string]interface{}{
			"enabled":     true,
			"server_name": sni,
			"insecure":    insecure,
		}
	case "vmess":
		base["type"] = "vmess"
		base["uuid"] = paramString(params, "uuid")
		base["alter_id"] = paramInt(params, "alterId")
		sec := paramString(params, "cipher")
		if sec == "" {
			sec = "auto"
		}
		base["security"] = sec
	case "vless":
		base["type"] = "vless"
		base["uuid"] = paramString(params, "uuid")
		if flow := paramString(params, "flow"); flow != "" {
			base["flow"] = flow
		}
	case "trojan":
		base["type"] = "trojan"
		base["password"] = paramString(params, "password")
	case "ssh":
		base["type"] = "ssh"
		base["username"] = paramString(params, "username")
		base["password"] = paramString(params, "password")
	case "hysteria":
		base["type"] = "hysteria"
		if v := paramString(params, "auth"); v != "" {
			base["auth_str"] = v
		}
		if v := paramInt(params, "up"); v > 0 {
			base["up_mbps"] = v
		}
		if v := paramInt(params, "down"); v > 0 {
			base["down_mbps"] = v
		}
	case "hysteria2":
		base["type"] = "hysteria2"
		base["password"] = paramString(params, "password")
		if v := paramInt(params, "up"); v > 0 {
			base["up_mbps"] = v
		}
		if v := paramInt(params, "down"); v > 0 {
			base["down_mbps"] = v
		}
		if v := paramString(params, "obfs"); v != "" {
			base["obfs"] = map[string]interface{}{
				"type": v,
			}
			if p := paramString(params, "obfs-password"); p != "" {
				base["obfs"] = map[string]interface{}{
					"type":     v,
					"password": p,
				}
			}
		}
	case "tuic":
		base["type"] = "tuic"
		base["uuid"] = paramString(params, "uuid")
		base["password"] = paramString(params, "password")
		if v := paramString(params, "congestion-controller"); v != "" {
			base["congestion_control"] = v
		}
		if v := paramString(params, "udp-relay-mode"); v != "" {
			base["udp_relay_mode"] = v
		}
		if paramBool(params, "disable-sni") {
			base["disable_sni"] = true
		}
		if paramBool(params, "reduce-rtt") {
			base["reduce_rtt"] = true
		}
	case "snell":
		base["type"] = "snell"
		base["psk"] = paramString(params, "psk")
		if v := paramString(params, "version"); v != "" {
			base["version"] = v
		}
		if v := paramString(params, "obfs"); v != "" {
			base["obfs"] = v
		}
		if v := paramString(params, "obfs-host"); v != "" {
			base["obfs_host"] = v
		}
	case "wireguard":
		base["type"] = "wireguard"
		base["private_key"] = paramString(params, "private-key")
		base["peer_public_key"] = paramString(params, "public-key")
		if v := paramString(params, "preshared-key"); v != "" {
			base["pre_shared_key"] = v
		}
		if v := paramString(params, "ip"); v != "" {
			base["local_address"] = []string{v}
		}
		if v := paramString(params, "dns"); v != "" {
			base["dns"] = []string{v}
		}
		if v := paramInt(params, "mtu"); v > 0 {
			base["mtu"] = v
		}
	case "socks5":
		base["type"] = "socks"
		base["username"] = paramString(params, "username")
		base["password"] = paramString(params, "password")
	case "http", "https":
		base["type"] = "http"
		base["username"] = paramString(params, "username")
		base["password"] = paramString(params, "password")
		if typ == "https" {
			base["tls"] = map[string]interface{}{"enabled": true}
		}
	default:
		return map[string]interface{}{}
	}

	needTLS := paramBool(params, "tls") || paramString(params, "sni") != ""
	if normalizeProtocol(typ) == "anytls" || normalizeProtocol(typ) == "hysteria2" || normalizeProtocol(typ) == "hysteria" || normalizeProtocol(typ) == "tuic" || normalizeProtocol(typ) == "trojan" {
		needTLS = true
	}
	if needTLS {
		tlsCfg := map[string]interface{}{
			"enabled": true,
		}
		if sni := paramString(params, "sni"); sni != "" {
			tlsCfg["server_name"] = sni
		} else if host := it.Server; host != "" {
			if net.ParseIP(host) == nil {
				tlsCfg["server_name"] = host
			}
		}
		if normalizeProtocol(typ) == "anytls" {
			if _, ok := tlsCfg["server_name"]; !ok {
				tlsCfg["server_name"] = "www.apple.com"
			}
			if _, ok := tlsCfg["insecure"]; !ok {
				tlsCfg["insecure"] = true
			}
		}
		if alpn := paramString(params, "alpn"); alpn != "" {
			tlsCfg["alpn"] = strings.Split(alpn, ",")
		}
		if paramBool(params, "skip-cert-verify") {
			tlsCfg["insecure"] = true
		}
		if real, ok := params["reality-opts"].(map[string]interface{}); ok {
			tlsCfg["enabled"] = true
			tlsCfg["reality"] = true
			if v := paramString(real, "public-key"); v != "" {
				tlsCfg["public_key"] = v
			}
			if v := paramString(real, "short-id"); v != "" {
				tlsCfg["short_id"] = v
			}
			if v := paramString(real, "fingerprint"); v != "" {
				tlsCfg["fingerprint"] = v
			}
		}
		base["tls"] = tlsCfg
	}

	if netw := paramString(params, "network"); netw != "" {
		switch netw {
		case "ws":
			ws := map[string]interface{}{"type": "ws"}
			if m, ok := params["ws-opts"].(map[string]interface{}); ok {
				if v := paramString(m, "path"); v != "" {
					ws["path"] = v
				}
				if headers, ok := m["headers"].(map[string]interface{}); ok {
					if host := paramString(headers, "Host"); host != "" {
						ws["headers"] = map[string]interface{}{"Host": host}
					}
				}
			}
			base["transport"] = ws
		case "grpc":
			grpc := map[string]interface{}{"type": "grpc"}
			if m, ok := params["grpc-opts"].(map[string]interface{}); ok {
				if v := paramString(m, "grpc-service-name"); v != "" {
					grpc["service_name"] = v
				}
			}
			base["transport"] = grpc
		case "h2":
			h2 := map[string]interface{}{"type": "http"}
			if m, ok := params["h2-opts"].(map[string]interface{}); ok {
				if v := paramString(m, "path"); v != "" {
					h2["path"] = v
				}
				if v := paramString(m, "host"); v != "" {
					h2["host"] = []string{v}
				}
			}
			base["transport"] = h2
		}
	}
	return base
}

func buildV2rayURI(typ string, it subProxy, params map[string]interface{}) string {
	// allow custom uri
	if raw := paramString(params, "uri"); raw != "" {
		return raw
	}
	switch normalizeProtocol(typ) {
	case "ss":
		cipher := it.Cipher
		if cipher == "" {
			cipher = paramString(params, "cipher")
		}
		pass := it.Password
		if pass == "" {
			pass = paramString(params, "password")
		}
		if cipher == "" || pass == "" {
			return ""
		}
		raw := fmt.Sprintf("%s:%s@%s:%d", cipher, pass, it.Server, it.Port)
		enc := base64.StdEncoding.EncodeToString([]byte(raw))
		return "ss://" + enc + "#" + url.QueryEscape(it.Name)
	case "vmess":
		vmess := map[string]string{
			"v":    "2",
			"ps":   it.Name,
			"add":  it.Server,
			"port": strconv.Itoa(it.Port),
			"id":   paramString(params, "uuid"),
			"aid":  paramString(params, "alterId"),
			"net":  paramString(params, "network"),
			"type": "none",
			"host": "",
			"path": "",
			"tls":  "",
		}
		if vmess["net"] == "" {
			vmess["net"] = "tcp"
		}
		if ws, ok := params["ws-opts"].(map[string]interface{}); ok {
			vmess["path"] = paramString(ws, "path")
			if headers, ok := ws["headers"].(map[string]interface{}); ok {
				vmess["host"] = paramString(headers, "Host")
			}
		}
		if paramBool(params, "tls") || paramString(params, "sni") != "" {
			vmess["tls"] = "tls"
		}
		raw, _ := json.Marshal(vmess)
		return "vmess://" + base64.StdEncoding.EncodeToString(raw)
	case "vless":
		uuid := paramString(params, "uuid")
		if uuid == "" {
			return ""
		}
		q := url.Values{}
		q.Set("encryption", "none")
		if sni := paramString(params, "sni"); sni != "" {
			q.Set("sni", sni)
		}
		if flow := paramString(params, "flow"); flow != "" {
			q.Set("flow", flow)
		}
		if paramBool(params, "tls") || paramString(params, "sni") != "" {
			q.Set("security", "tls")
		}
		if netw := paramString(params, "network"); netw != "" {
			q.Set("type", netw)
		}
		if ws, ok := params["ws-opts"].(map[string]interface{}); ok {
			if v := paramString(ws, "path"); v != "" {
				q.Set("path", v)
			}
			if headers, ok := ws["headers"].(map[string]interface{}); ok {
				if v := paramString(headers, "Host"); v != "" {
					q.Set("host", v)
				}
			}
		}
		if real, ok := params["reality-opts"].(map[string]interface{}); ok {
			q.Set("security", "reality")
			if v := paramString(real, "public-key"); v != "" {
				q.Set("pbk", v)
			}
			if v := paramString(real, "short-id"); v != "" {
				q.Set("sid", v)
			}
			if v := paramString(real, "fingerprint"); v != "" {
				q.Set("fp", v)
			}
		}
		return fmt.Sprintf("vless://%s@%s:%d?%s#%s", uuid, it.Server, it.Port, q.Encode(), url.QueryEscape(it.Name))
	case "trojan":
		pass := it.Password
		if pass == "" {
			pass = paramString(params, "password")
		}
		if pass == "" {
			return ""
		}
		q := url.Values{}
		if sni := paramString(params, "sni"); sni != "" {
			q.Set("peer", sni)
		}
		if netw := paramString(params, "network"); netw != "" {
			q.Set("type", netw)
		}
		if ws, ok := params["ws-opts"].(map[string]interface{}); ok {
			if v := paramString(ws, "path"); v != "" {
				q.Set("path", v)
			}
			if headers, ok := ws["headers"].(map[string]interface{}); ok {
				if v := paramString(headers, "Host"); v != "" {
					q.Set("host", v)
				}
			}
		}
		return fmt.Sprintf("trojan://%s@%s:%d?%s#%s", url.QueryEscape(pass), it.Server, it.Port, q.Encode(), url.QueryEscape(it.Name))
	case "ssr":
		return buildSSRURI(it, params)
	case "hysteria2":
		pass := it.Password
		if pass == "" {
			pass = paramString(params, "password")
		}
		if pass == "" {
			return ""
		}
		q := url.Values{}
		if sni := paramString(params, "sni"); sni != "" {
			q.Set("sni", sni)
		}
		if v := paramInt(params, "up"); v > 0 {
			q.Set("upmbps", strconv.Itoa(v))
		}
		if v := paramInt(params, "down"); v > 0 {
			q.Set("downmbps", strconv.Itoa(v))
		}
		if v := paramString(params, "obfs"); v != "" {
			q.Set("obfs", v)
		}
		if v := paramString(params, "obfs-password"); v != "" {
			q.Set("obfs-password", v)
		}
		return fmt.Sprintf("hysteria2://%s@%s:%d?%s#%s", url.QueryEscape(pass), it.Server, it.Port, q.Encode(), url.QueryEscape(it.Name))
	case "tuic":
		uuid := paramString(params, "uuid")
		pass := paramString(params, "password")
		if uuid == "" || pass == "" {
			return ""
		}
		q := url.Values{}
		if v := paramString(params, "sni"); v != "" {
			q.Set("sni", v)
		}
		if v := paramString(params, "congestion-controller"); v != "" {
			q.Set("congestion_control", v)
		}
		if v := paramString(params, "udp-relay-mode"); v != "" {
			q.Set("udp_relay_mode", v)
		}
		return fmt.Sprintf("tuic://%s:%s@%s:%d?%s#%s", uuid, url.QueryEscape(pass), it.Server, it.Port, q.Encode(), url.QueryEscape(it.Name))
	case "anytls":
		pass := it.Password
		if pass == "" {
			pass = paramString(params, "password")
		}
		if pass == "" {
			return ""
		}
		return fmt.Sprintf("anytls://%s@%s:%d#%s", url.QueryEscape(pass), it.Server, it.Port, url.QueryEscape(it.Name))
	default:
		return ""
	}
}

func buildQXLine(typ string, it subProxy, params map[string]interface{}) string {
	if raw := paramString(params, "qx"); raw != "" {
		return formatSurgeLine(raw, it)
	}
	switch normalizeProtocol(typ) {
	case "ss":
		cipher := it.Cipher
		if cipher == "" {
			cipher = paramString(params, "cipher")
		}
		pass := it.Password
		if pass == "" {
			pass = paramString(params, "password")
		}
		if cipher == "" || pass == "" {
			return ""
		}
		return fmt.Sprintf("shadowsocks=%s:%d,method=%s,password=%s,tag=%s", it.Server, it.Port, cipher, pass, it.Name)
	case "ssr":
		cipher := it.Cipher
		if cipher == "" {
			cipher = paramString(params, "cipher")
		}
		pass := it.Password
		if pass == "" {
			pass = paramString(params, "password")
		}
		if cipher == "" || pass == "" {
			return ""
		}
		proto := paramString(params, "protocol")
		if proto == "" {
			proto = "origin"
		}
		obfs := paramString(params, "obfs")
		if obfs == "" {
			obfs = "plain"
		}
		parts := []string{
			fmt.Sprintf("shadowsocksr=%s:%d", it.Server, it.Port),
			"method=" + cipher,
			"password=" + pass,
			"protocol=" + proto,
			"obfs=" + obfs,
			"tag=" + it.Name,
		}
		if v := paramString(params, "protocol-param"); v != "" {
			parts = append(parts, "protocol_param="+v)
		}
		if v := paramString(params, "obfs-param"); v != "" {
			parts = append(parts, "obfs_param="+v)
		}
		if v := paramString(params, "obfs-host"); v != "" {
			parts = append(parts, "obfs-host="+v)
		}
		return strings.Join(parts, ",")
	case "vmess":
		uuid := paramString(params, "uuid")
		if uuid == "" {
			return ""
		}
		parts := []string{
			fmt.Sprintf("vmess=%s:%d", it.Server, it.Port),
			"method=auto",
			"username=" + uuid,
			"tag=" + it.Name,
		}
		if aid := paramString(params, "alterId"); aid != "" {
			parts = append(parts, "alterId="+aid)
		}
		if paramBool(params, "tls") {
			parts = append(parts, "tls=true")
		}
		if sni := paramString(params, "sni"); sni != "" {
			parts = append(parts, "sni="+sni)
		}
		if netw := paramString(params, "network"); netw == "ws" {
			parts = append(parts, "obfs=ws")
			if ws, ok := params["ws-opts"].(map[string]interface{}); ok {
				if v := paramString(ws, "path"); v != "" {
					parts = append(parts, "obfs-uri="+v)
				}
				if headers, ok := ws["headers"].(map[string]interface{}); ok {
					if v := paramString(headers, "Host"); v != "" {
						parts = append(parts, "obfs-host="+v)
					}
				}
			}
		}
		return strings.Join(parts, ",")
	case "vless":
		uuid := paramString(params, "uuid")
		if uuid == "" {
			return ""
		}
		parts := []string{
			fmt.Sprintf("vless=%s:%d", it.Server, it.Port),
			"username=" + uuid,
			"tag=" + it.Name,
		}
		if sni := paramString(params, "sni"); sni != "" {
			parts = append(parts, "sni="+sni)
		}
		if paramBool(params, "tls") {
			parts = append(parts, "tls=true")
		}
		if netw := paramString(params, "network"); netw == "ws" {
			parts = append(parts, "obfs=ws")
		}
		if flow := paramString(params, "flow"); flow != "" {
			parts = append(parts, "flow="+flow)
		}
		return strings.Join(parts, ",")
	case "trojan":
		pass := it.Password
		if pass == "" {
			pass = paramString(params, "password")
		}
		if pass == "" {
			return ""
		}
		parts := []string{
			fmt.Sprintf("trojan=%s:%d", it.Server, it.Port),
			"password=" + pass,
			"tag=" + it.Name,
		}
		if sni := paramString(params, "sni"); sni != "" {
			parts = append(parts, "sni="+sni)
		}
		return strings.Join(parts, ",")
	case "anytls":
		pass := it.Password
		if pass == "" {
			pass = paramString(params, "password")
		}
		if pass == "" {
			return ""
		}
		return fmt.Sprintf("anytls=%s:%d,password=%s,tag=%s", it.Server, it.Port, pass, it.Name)
	case "http", "https":
		return fmt.Sprintf("%s=%s:%d,tag=%s", typ, it.Server, it.Port, it.Name)
	case "socks5":
		return fmt.Sprintf("socks5=%s:%d,tag=%s", it.Server, it.Port, it.Name)
	default:
		return ""
	}
}

func buildSSRURI(it subProxy, params map[string]interface{}) string {
	cipher := it.Cipher
	if cipher == "" {
		cipher = paramString(params, "cipher")
	}
	pass := it.Password
	if pass == "" {
		pass = paramString(params, "password")
	}
	if cipher == "" || pass == "" {
		return ""
	}
	proto := paramString(params, "protocol")
	if proto == "" {
		proto = "origin"
	}
	obfs := paramString(params, "obfs")
	if obfs == "" {
		obfs = "plain"
	}
	base := fmt.Sprintf("%s:%d:%s:%s:%s:%s", it.Server, it.Port, proto, cipher, obfs, base64.StdEncoding.EncodeToString([]byte(pass)))
	q := url.Values{}
	if v := paramString(params, "protocol-param"); v != "" {
		q.Set("protoparam", base64.StdEncoding.EncodeToString([]byte(v)))
	}
	if v := paramString(params, "obfs-param"); v != "" {
		q.Set("obfsparam", base64.StdEncoding.EncodeToString([]byte(v)))
	}
	q.Set("remarks", base64.StdEncoding.EncodeToString([]byte(it.Name)))
	raw := base + "/?" + q.Encode()
	return "ssr://" + base64.StdEncoding.EncodeToString([]byte(raw))
}

func paramBool(params map[string]interface{}, key string) bool {
	if params == nil {
		return false
	}
	if v, ok := params[key]; ok {
		switch t := v.(type) {
		case bool:
			return t
		case string:
			return strings.EqualFold(strings.TrimSpace(t), "true")
		case float64:
			return t != 0
		case int:
			return t != 0
		case int64:
			return t != 0
		}
	}
	return false
}

func paramInt(params map[string]interface{}, key string) int {
	if params == nil {
		return 0
	}
	if v, ok := params[key]; ok {
		switch t := v.(type) {
		case int:
			return t
		case int64:
			return int(t)
		case float64:
			return int(t)
		case string:
			n, _ := strconv.Atoi(strings.TrimSpace(t))
			return n
		}
	}
	return 0
}

func paramStringDefault(params map[string]interface{}, key string, def string) string {
	if params == nil {
		return def
	}
	if v := paramString(params, key); v != "" {
		return v
	}
	return def
}

func paramBoolDefault(params map[string]interface{}, key string, def bool) bool {
	if params == nil {
		return def
	}
	if _, ok := params[key]; ok {
		return paramBool(params, key)
	}
	return def
}

func paramIntDefault(params map[string]interface{}, key string, def int) int {
	if params == nil {
		return def
	}
	if _, ok := params[key]; ok {
		return paramInt(params, key)
	}
	return def
}
