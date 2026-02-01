package controller

import (
    "fmt"
    "net/url"
    "strings"

    dbpkg "network-panel/golang-backend/internal/db"
    "network-panel/golang-backend/internal/app/model"
)

// makeObserverForNode builds an observer string for gost service to report flow to panel.
// Template can be customized via vite_config name "forward_observer_template".
// Placeholders: {SERVER} -> base server URL (http://host), {SECRET} -> node secret.
// Default: webapi://{SERVER}/flow/upload?secret={SECRET}
func makeObserverForNode(nodeID int64) string {
    secret := nodeSecret(nodeID)
    if secret == "" {
        return ""
    }
    base := serverBaseURL()
    if base == "" {
        return ""
    }
    tpl := strings.TrimSpace(getCfg("forward_observer_template"))
    if tpl == "" {
        tpl = "webapi://{SERVER}/flow/upload?secret={SECRET}"
    }
    v := strings.ReplaceAll(tpl, "{SERVER}", base)
    v = strings.ReplaceAll(v, "{SECRET}", secret)
    return v
}

// buildObserverPluginSpec returns (observerName, pluginSpec) for a specific service.
// Each service gets its own observer with unique name and addr carrying forward ID for attribution.
func buildObserverPluginSpec(nodeID int64, serviceName string) (string, map[string]any) {
    secret := nodeSecret(nodeID)
    base := serverBaseURL()
    scheme := serverScheme()
    if secret == "" || base == "" || strings.TrimSpace(serviceName) == "" {
        return "", nil
    }
    // Derive forwardID from serviceName: forwardId_userId_userTunnelId
    fwdID := ""
    if i := strings.Index(serviceName, "_"); i > 0 {
        fwdID = serviceName[:i]
    } else {
        fwdID = serviceName
    }
    obsName := "obs_" + fwdID
    // Build plugin addr using http scheme
    addr := scheme + "://" + base + "/api/v1/flow/upload?secret=" + secret + "&id=" + fwdID
    // allow override via template forward_observer_plugin_template, e.g. http://{SERVER}/path?secret={SECRET}&id={ID}
    if tpl := strings.TrimSpace(getCfg("forward_observer_plugin_template")); tpl != "" {
        v := strings.ReplaceAll(tpl, "{SERVER}", base)
        v = strings.ReplaceAll(v, "{SECRET}", secret)
        v = strings.ReplaceAll(v, "{ID}", fwdID)
        addr = v
    }
    plugin := map[string]any{
        "type":  "http",
        "addr":  addr,
    }
    // optional override for plugin type
    if pt := strings.TrimSpace(getCfg("forward_observer_plugin_type")); pt != "" {
        plugin["type"] = pt
    }
    spec := map[string]any{
        "name":   obsName,
        "plugin": plugin,
    }
    return obsName, spec
}

// buildExitObserverPluginSpec builds observer for exit services to report flow to /flow/exit
func buildExitObserverPluginSpec(nodeID int64, baseUserID int64, port int) (string, map[string]any) {
    secret := nodeSecret(nodeID)
    base := serverBaseURL()
    scheme := serverScheme()
    if secret == "" || base == "" || baseUserID <= 0 || port <= 0 {
        return "", nil
    }
    obsName := fmt.Sprintf("obs_exit_%d_%d", nodeID, port)
    addr := fmt.Sprintf(
        "%s://%s/api/v1/flow/exit?secret=%s&userId=%d&nodeId=%d&port=%d",
        scheme,
        base,
        url.QueryEscape(secret),
        baseUserID,
        nodeID,
        port,
    )
    plugin := map[string]any{
        "type": "http",
        "addr": addr,
    }
    spec := map[string]any{
        "name":   obsName,
        "plugin": plugin,
    }
    return obsName, spec
}

func serverScheme() string {
    raw := strings.TrimSpace(getCfg("ip"))
    if strings.HasPrefix(raw, "https://") {
        return "https"
    }
    return "http"
}

func serverBaseURL() string {
    // Return host[:port] without scheme, no trailing slash
    host := strings.TrimSpace(getCfg("ip"))
    if host == "" {
        return ""
    }
    if strings.HasPrefix(host, "http://") {
        host = strings.TrimPrefix(host, "http://")
    } else if strings.HasPrefix(host, "https://") {
        host = strings.TrimPrefix(host, "https://")
    }
    if strings.HasSuffix(host, "/") {
        host = strings.TrimSuffix(host, "/")
    }
    return host
}

func nodeSecret(nodeID int64) string {
    var n model.Node
    if err := dbpkg.DB.Select("secret").First(&n, nodeID).Error; err == nil {
        return n.Secret
    }
    return ""
}
