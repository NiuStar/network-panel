package controller

import (
	"fmt"
	"strings"

	tpl "network-panel/golang-backend/template"
)

// The upstream profile is pinned in template/shadowrocket.conf; never inherit
// its update-url, which would replace this per-user profile with an empty one.
func buildShadowrocketRuleConfig(items []subProxy) (string, error) {
	base, ok := tpl.Load("shadowrocket.conf")
	if !ok || !strings.Contains(base, "[Proxy]\n") || !strings.Contains(base, "\n[Proxy Group]") || !strings.Contains(base, "\n[Rule]") {
		return "", fmt.Errorf("rule template is missing required sections")
	}
	lines := make([]string, 0, len(items))
	seenNames := make(map[string]bool, len(items))
	for _, it := range items {
		if it.Name == "" || it.Server == "" || it.Port <= 0 || it.Port > 65535 ||
			strings.ContainsAny(it.Name, "\r\n,=") || strings.ContainsAny(it.Server, "\r\n,") {
			return "", fmt.Errorf("invalid node name or server")
		}
		if seenNames[it.Name] {
			return "", fmt.Errorf("duplicate node name")
		}
		seenNames[it.Name] = true
		params := it.Params
		if params == nil {
			params = map[string]interface{}{}
		}
		name := it.Name
		server := it.Server
		var line string
		switch normalizeProtocol(it.Type) {
		case "ss":
			cipher, pass := it.Cipher, it.Password
			if cipher == "" {
				cipher = paramString(params, "cipher")
			}
			if pass == "" {
				pass = paramString(params, "password")
			}
			if cipher != "" && pass != "" && !strings.ContainsAny(cipher+pass, "\r\n,") {
				line = fmt.Sprintf("%s = ss, %s, %d, method=%s, password=%s", name, server, it.Port, cipher, pass)
			}
		case "anytls":
			pin, sni, ok := verifiedAnyTLSParams(params)
			if !ok || strings.ContainsAny(sni, "\r\n,") {
				return "", fmt.Errorf("AnyTLS certificate pin or SNI is missing, invalid, or verification disabled")
			}
			pass := it.Password
			if pass == "" {
				pass = paramString(params, "password")
			}
			pass = anyTLSCredentialPassword(pass, params)
			if pass != "" && !strings.ContainsAny(pass, "\r\n,") {
				line = fmt.Sprintf("%s = anytls, %s, %d, password=%s, skip-cert-verify=false, sni=%s, server-cert-fingerprint-sha256=%s", name, server, it.Port, pass, sni, pin)
			}
		case "socks5", "http", "https":
			line = fmt.Sprintf("%s = %s, %s, %d", name, normalizeProtocol(it.Type), server, it.Port)
		}
		if line == "" {
			return "", fmt.Errorf("unsupported or incomplete node")
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "", fmt.Errorf("no supported nodes")
	}
	base = strings.Replace(base,
		"update-url = https://raw.githubusercontent.com/LingJingMaster/Shadowrocket-Rules/refs/heads/main/Shadowrocket.conf",
		"# update-url disabled: personalized nodes are served by this subscription URL", 1)
	base = strings.Replace(base, "[Proxy]\n", "[Proxy]\n"+strings.Join(lines, "\n")+"\n", 1)
	return withSelectableShadowrocketGroups(base, items), nil
}

// Keep upstream rule targets and manual policy groups, but expose each account
// node directly in every group. PROXY is the home-screen selection, not a
// per-service node; do not let a service silently inherit it.
func withSelectableShadowrocketGroups(base string, items []subProxy) string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	start := strings.Index(base, "[Proxy Group]\n")
	end := start + strings.Index(base[start:], "\n[Rule]")
	section := base[start+len("[Proxy Group]\n") : end]
	var groups []string
	for _, line := range strings.Split(section, "\n") {
		parts := strings.SplitN(line, " = ", 2)
		if len(parts) != 2 {
			continue
		}
		group := parts[0]
		var choices []string
		if strings.HasPrefix(parts[1], "url-test,") {
			// Upstream automatic regions cannot be selected per node.
			// A missing region still offers all account nodes.
		} else if strings.HasPrefix(parts[1], "select,") {
			for _, option := range strings.Split(strings.TrimPrefix(parts[1], "select,"), ",") {
				if option == "PROXY" || strings.HasPrefix(option, "policy-select-name=") {
					continue
				}
				choices = append(choices, option)
			}
		} else {
			continue
		}
		defaultChoice := names[0]
		// Preserve the upstream direct/block defaults instead of forwarding
		// private, domestic or DNS traffic via an unrelated proxy.
		if len(choices) > 0 && (choices[0] == "DIRECT" || choices[0] == "REJECT") {
			defaultChoice = choices[0]
		}
		if group == "🧱 DNS 防泄露" {
			defaultChoice = "REJECT"
		}
		if group == "🇺🇸 美国节点" || group == "🤖 AI 服务" {
			for _, name := range names {
				if strings.Contains(strings.ToUpper(name), "US") || strings.Contains(name, "🇺🇸") || strings.Contains(name, "美国") {
					defaultChoice = name
					break
				}
			}
		}
		// List every node first so each rule's group exposes concrete choices.
		// Referenced groups remain as optional alternatives.
		seen := map[string]bool{}
		result := make([]string, 0, len(choices)+len(names)+1)
		for _, choice := range append(append([]string{defaultChoice}, names...), choices...) {
			if !seen[choice] {
				result = append(result, choice)
				seen[choice] = true
			}
		}
		groups = append(groups, group+" = select,"+strings.Join(result, ","))
	}
	return base[:start] + "[Proxy Group]\n" + strings.Join(groups, "\n") + "\n" + base[end+1:]
}
