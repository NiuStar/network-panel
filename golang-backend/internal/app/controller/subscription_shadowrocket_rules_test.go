package controller

import (
	"strings"
	"testing"
)

func TestBuildShadowrocketRuleConfigRejectsUnpinnedAnyTLS(t *testing.T) {
	for _, params := range []map[string]interface{}{
		{"sni": "node.example.com", "skip-cert-verify": false},
		{"sni": "node.example.com", "skip-cert-verify": true, "cert-sha256": strings.Repeat("a", 64)},
	} {
		_, err := buildShadowrocketRuleConfig([]subProxy{{Name: "node", Type: "anytls", Server: "192.0.2.1", Port: 443, Password: "test", Params: params}})
		if err == nil {
			t.Fatalf("accepted insecure AnyTLS config: %#v", params)
		}
	}
}

func TestBuildShadowrocketRuleConfigEmbedsNodesAndRules(t *testing.T) {
	items := []subProxy{
		{Name: "🇺🇸 US-A", Type: "ss", Server: "us.example.com", Port: 443, Cipher: "aes-256-gcm", Password: "example-pass"},
		{Name: "🇭🇰 HK-B", Type: "anytls", Server: "hk.example.com", Port: 8443, Password: "u1:example-pass", Params: map[string]interface{}{"sni": "example.com", "skip-cert-verify": false, "cert-sha256": strings.Repeat("a", 64)}},
	}
	got, err := buildShadowrocketRuleConfig(items)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[General]", "[Proxy]\n🇺🇸 US-A = ss, us.example.com, 443, method=aes-256-gcm, password=example-pass",
		"🇭🇰 HK-B = anytls, hk.example.com, 8443, password=example-pass", "sni=example.com",
		"[Proxy Group]", "🤖 AI 服务 = select,🇺🇸 US-A,", "📹 油管视频 = select,🇺🇸 US-A,", "RULE-SET,https://raw.githubusercontent.com/LingJingMaster/Shadowrocket-Rules/", "GEOIP,CN,🔒 国内服务", "FINAL,🐟 漏网之鱼", "[Host]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(got, "update-url = https://raw.githubusercontent.com/LingJingMaster/Shadowrocket-Rules/") {
		t.Fatal("upstream update-url would replace personalized profile with a node-free template")
	}
	if strings.Contains(got, "u1:example-pass") {
		t.Fatal("AnyTLS user prefix leaked")
	}
	if strings.Index(got, "[Proxy]\n") > strings.Index(got, "[Proxy Group]") {
		t.Fatal("proxy section after group")
	}
}

func TestBuildShadowrocketRuleConfigRejectsEmptyOrUnsupportedNodes(t *testing.T) {
	for _, items := range [][]subProxy{nil, {{Name: "opaque", Type: "unknown", Server: "example.com", Port: 443}}} {
		if _, err := buildShadowrocketRuleConfig(items); err == nil {
			t.Errorf("accepted unusable nodes: %#v", items)
		}
	}
}

func TestBuildShadowrocketRuleConfigRejectsConfigInjection(t *testing.T) {
	items := []subProxy{{Name: "ok\n[Rule]\nFINAL,DIRECT", Type: "ss", Server: "example.com", Port: 443, Cipher: "aes-256-gcm", Password: "secret"}}
	if _, err := buildShadowrocketRuleConfig(items); err == nil {
		t.Fatal("accepted newline in profile field")
	}
}

func TestBuildShadowrocketRuleConfigRejectsPartialNodeList(t *testing.T) {
	items := []subProxy{
		{Name: "good", Type: "ss", Server: "example.com", Port: 443, Cipher: "aes-256-gcm", Password: "secret"},
		{Name: "missing", Type: "vmess", Server: "example.net", Port: 443},
	}
	if _, err := buildShadowrocketRuleConfig(items); err == nil {
		t.Fatal("silently dropped a node from a seemingly complete profile")
	}
}

func TestBuildShadowrocketRuleConfigEachServiceCanChooseEachNode(t *testing.T) {
	items := []subProxy{
		{Name: "HK-A", Type: "ss", Server: "hk.example.com", Port: 443, Cipher: "aes-256-gcm", Password: "secret"},
		{Name: "JP-B", Type: "ss", Server: "jp.example.com", Port: 443, Cipher: "aes-256-gcm", Password: "secret"},
	}
	got, err := buildShadowrocketRuleConfig(items)
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"🚀 节点选择", "🤖 AI 服务", "📹 油管视频", "🔍 谷歌服务", "📲 电报消息", "🌍 非中国", "🐟 漏网之鱼"} {
		found := false
		for _, line := range strings.Split(got, "\n") {
			if strings.HasPrefix(line, group+" = ") {
				found = true
				if !strings.HasPrefix(line, group+" = select,") || !strings.Contains(line, ",HK-A,") || !strings.Contains(line, ",JP-B,") {
					t.Errorf("group %q cannot manually select every account node: %s", group, line)
				}
			}
		}
		if !found {
			t.Errorf("group %q missing", group)
		}
	}
	if strings.Contains(got, "🇺🇸 美国节点 = url-test") {
		t.Error("regional group is auto-only")
	}
}

func TestBuildShadowrocketRuleConfigMissingRegionUsesManualNode(t *testing.T) {
	got, err := buildShadowrocketRuleConfig([]subProxy{{Name: "HK-A", Type: "ss", Server: "hk.example.com", Port: 443, Cipher: "aes-256-gcm", Password: "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "🤖 AI 服务 = select,HK-A,") || !strings.Contains(got, "🇺🇸 美国节点 = select,HK-A") {
		t.Error("missing US region leaves AI routed to an unavailable auto-test group or global PROXY")
	}
}

func TestBuildShadowrocketRuleConfigRejectsDuplicateNodeName(t *testing.T) {
	items := []subProxy{
		{Name: "same", Type: "ss", Server: "a.example.com", Port: 443, Cipher: "aes-256-gcm", Password: "secret"},
		{Name: "same", Type: "ss", Server: "b.example.com", Port: 443, Cipher: "aes-256-gcm", Password: "secret"},
	}
	if _, err := buildShadowrocketRuleConfig(items); err == nil {
		t.Fatal("ambiguous policy choice for duplicate proxy names")
	}
}

func TestBuildShadowrocketRuleConfigEveryRuleTargetIsSelectable(t *testing.T) {
	got, err := buildShadowrocketRuleConfig([]subProxy{
		{Name: "HK-A", Type: "ss", Server: "hk.example.com", Port: 443, Cipher: "aes-256-gcm", Password: "secret"},
		{Name: "US-B", Type: "ss", Server: "us.example.com", Port: 443, Cipher: "aes-256-gcm", Password: "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	groups := map[string][]string{}
	section := ""
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		if section == "[Proxy Group]" && strings.Contains(line, " = ") {
			parts := strings.SplitN(line, " = ", 2)
			groups[parts[0]] = strings.Split(parts[1], ",")
		}
		if section == "[Rule]" && (strings.HasPrefix(line, "RULE-SET,") || strings.HasPrefix(line, "DOMAIN,") || strings.HasPrefix(line, "GEOIP,") || strings.HasPrefix(line, "FINAL,")) {
			parts := strings.Split(line, ",")
			target := parts[len(parts)-1]
			if target == "no-resolve" {
				target = parts[len(parts)-2]
			}
			if target == "DIRECT" || target == "REJECT" {
				continue
			}
			choices, ok := groups[target]
			if !ok || len(choices) < 3 || choices[0] != "select" || !strings.Contains(","+strings.Join(choices, ",")+",", ",HK-A,") || !strings.Contains(","+strings.Join(choices, ",")+",", ",US-B,") {
				t.Errorf("rule target %q cannot choose each node: %s", target, line)
			}
		}
	}
}
