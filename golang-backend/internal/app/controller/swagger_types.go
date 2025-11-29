package controller

import (
	"network-panel/golang-backend/internal/app/dto"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
)

// BaseSwaggerResp 通用返回
type BaseSwaggerResp struct {
	Code int    `json:"code" example:"0"`
	Msg  string `json:"msg" example:"操作成功"`
	Ts   int64  `json:"ts" example:"1700000000000"`
}

type SwaggerLoginResp struct {
	BaseSwaggerResp
	Data struct {
		Token                 string `json:"token" example:"Bearer xxx"`
		Name                  string `json:"name" example:"admin"`
		RoleID                int    `json:"role_id" example:"0"`
		RequirePasswordChange bool   `json:"requirePasswordChange" example:"false"`
	} `json:"data"`
}

type SwaggerRegisterResp = SwaggerLoginResp

type SwaggerUserListItem struct {
	ID            int64  `json:"id" example:"1"`
	CreatedTime   int64  `json:"createdTime" example:"1700000000000"`
	UpdatedTime   int64  `json:"updatedTime" example:"1700000000000"`
	Status        *int   `json:"status" example:"1"`
	User          string `json:"user" example:"demo"`
	RoleID        int    `json:"roleId" example:"1"`
	ExpTime       *int64 `json:"expTime" example:"1700000000000"`
	Flow          int64  `json:"flow" example:"1024"`
	InFlow        int64  `json:"inFlow" example:"0"`
	OutFlow       int64  `json:"outFlow" example:"0"`
	Num           int    `json:"num" example:"20"`
	FlowResetTime int64  `json:"flowResetTime" example:"0"`
	UsedBilled    int64  `json:"usedBilled" example:"0"`
	ForwardCount  int64  `json:"forwardCount" example:"3"`
	TunnelCount   int64  `json:"tunnelCount" example:"1"`
	NodeCount     int64  `json:"nodeCount" example:"1"`
}

type SwaggerUserListResp struct {
	BaseSwaggerResp
	Data []SwaggerUserListItem `json:"data"`
}

type SwaggerUserInfoPackage struct {
	Flow          int64  `json:"flow" example:"1024"`
	InFlow        int64  `json:"inFlow" example:"0"`
	OutFlow       int64  `json:"outFlow" example:"0"`
	Num           int    `json:"num" example:"20"`
	ExpTime       *int64 `json:"expTime" example:"1700000000000"`
	FlowResetTime int64  `json:"flowResetTime" example:"0"`
	UsedBilled    int64  `json:"usedBilled" example:"0"`
}

type SwaggerPackageTunnelPermission struct {
	model.UserTunnel
	TunnelName     string  `json:"tunnelName" example:"default-tunnel"`
	SpeedLimitName *string `json:"speedLimitName,omitempty" example:"10Mbps"`
	TunnelFlow     *int    `json:"tunnelFlow,omitempty" example:"2"`
}

type SwaggerPackageForward struct {
	model.Forward
	TunnelName string `json:"tunnelName" example:"default-tunnel"`
	InIp       string `json:"inIp" example:"1.1.1.1"`
}

type SwaggerPackageFlowPoint struct {
	Time string `json:"time" example:"01-02 15:00"`
	Flow int64  `json:"flow" example:"1024"`
}

type SwaggerUserPackageResp struct {
	BaseSwaggerResp
	Data struct {
		UserInfo          SwaggerUserInfoPackage           `json:"userInfo"`
		TunnelPermissions []SwaggerPackageTunnelPermission `json:"tunnelPermissions"`
		Forwards          []SwaggerPackageForward          `json:"forwards"`
		StatisticsFlows   []SwaggerPackageFlowPoint        `json:"statisticsFlows"`
	} `json:"data"`
}

type SwaggerConfigListResp struct {
	BaseSwaggerResp
	Data map[string]string `json:"data"`
}

type SwaggerConfigGetResp struct {
	BaseSwaggerResp
	Data string `json:"data" example:"some-value"`
}

type SwaggerConfigGetReq struct {
	Name string `json:"name" example:"app_name"`
}

type SwaggerVersionInfo struct {
	Server   string `json:"server" example:"1.0.1"`
	Agent    string `json:"agent" example:"go-agent-1.0.1"`
	Agent2   string `json:"agent2" example:"go-agent2-1.0.1"`
	Center   string `json:"center" example:"https://center.example.com"`
	CenterOn bool   `json:"centerOn" example:"true"`
}

type SwaggerVersionResp struct {
	BaseSwaggerResp
	Data SwaggerVersionInfo `json:"data"`
}

type SwaggerVersionLatestResp struct {
	BaseSwaggerResp
	Data struct {
		Tag    string      `json:"tag" example:"v1.0.1"`
		Name   string      `json:"name" example:"Release name"`
		Assets interface{} `json:"assets"`
	} `json:"data"`
}

type SwaggerVersionUpgradeResp struct {
	BaseSwaggerResp
	Data struct {
		Tag     interface{} `json:"tag"`
		Created interface{} `json:"created"`
		Logs    []string    `json:"logs"`
		Restart interface{} `json:"restart,omitempty"`
		Errors  []string    `json:"errors,omitempty"`
	} `json:"data"`
}

// SwaggerChangePasswordReq 用于 @Param 显示
type SwaggerChangePasswordReq = dto.ChangePasswordDto

// SwaggerLoginReq 用于 @Param 显示
type SwaggerLoginReq = dto.LoginDto

// SwaggerRegisterReq 用于 @Param 显示
type SwaggerRegisterReq struct {
	Username string `json:"username" example:"demo"`
	Password string `json:"password" example:"123456"`
}

// SwaggerUserCreateReq 用于 @Param 显示
type SwaggerUserCreateReq = dto.UserDto

// SwaggerUserUpdateReq 用于 @Param 显示
type SwaggerUserUpdateReq = dto.UserUpdateDto

// SwaggerResetFlowReq 用于 @Param 显示
type SwaggerResetFlowReq = dto.ResetFlowDto

// SwaggerConfigUpdateMap 更新配置时的 map
type SwaggerConfigUpdateMap map[string]string

// SwaggerConfigUpdateSingle 单项配置更新
type SwaggerConfigUpdateSingle struct {
	Name  string `json:"name" example:"app_name"`
	Value string `json:"value" example:"network-panel"`
}

// SwaggerVersionUpgradeReq 版本升级请求
type SwaggerVersionUpgradeReq struct {
	ProxyPrefix string `json:"proxyPrefix" example:"https://ghproxy.com"`
}

// Common simple request structs
type SwaggerIDReq struct {
	ID int64 `json:"id" example:"1"`
}

type SwaggerForwardOrderReq struct {
	Forwards []struct {
		ID  int64 `json:"id" example:"1"`
		Inx int   `json:"inx" example:"1"`
	} `json:"forwards"`
}

type SwaggerForwardDiagnoseStepReq struct {
	ForwardID int64  `json:"forwardId" example:"1"`
	Step      string `json:"step" example:"entryExit"`
}

// Tunnel related
type SwaggerTunnelIDReq struct {
	TunnelID int64 `json:"tunnelId" example:"1"`
}

type SwaggerTunnelPathSetReq struct {
	TunnelID int64   `json:"tunnelId" example:"1"`
	Path     []int64 `json:"path" swaggertype:"array,integer" example:"2,3"`
}

type SwaggerTunnelBindReq struct {
	TunnelID int64  `json:"tunnelId" example:"1"`
	Bind     string `json:"bind" example:"0.0.0.0"`
}

type SwaggerTunnelIfaceReq struct {
	TunnelID int64  `json:"tunnelId" example:"1"`
	Iface    string `json:"iface" example:"eth0"`
}

type SwaggerTunnelUserAssignReq struct {
	UserID    int64 `json:"userId" example:"2"`
	TunnelID  int64 `json:"tunnelId" example:"1"`
	Flow      int64 `json:"flow" example:"1024"`
	Num       int   `json:"num" example:"10"`
	FlowReset int64 `json:"flowResetTime" example:"0"`
}

type SwaggerTunnelUserRemoveReq struct {
	UserID   int64 `json:"userId" example:"2"`
	TunnelID int64 `json:"tunnelId" example:"1"`
}

type SwaggerTunnelDiagnoseReq struct {
	TunnelID int64  `json:"tunnelId" example:"1"`
	Step     string `json:"step,omitempty" example:"path"`
}

// SwaggerVersionUpgradeStreamResp SSE 事件：log/done/error
type SwaggerVersionUpgradeStreamResp struct {
	Event string      `json:"event" example:"log"`
	Time  string      `json:"time" example:"2024-01-01T00:00:00Z"`
	Data  interface{} `json:"data"`
}

// These aliases ensure swag can resolve response.R in annotations when needed.
type SwaggerResp = response.R

// Node related
type SwaggerNodeIDReq struct {
	ID int64 `json:"id" example:"1"`
}

type SwaggerNodeCreateReq = dto.NodeDto
type SwaggerNodeUpdateReq = dto.NodeUpdateDto

type SwaggerNodeExitReq struct {
	NodeID    int64    `json:"nodeId" example:"1"`
	Port      int      `json:"port" example:"10000"`
	Password  string   `json:"password" example:"pass"`
	Method    string   `json:"method" example:"AEAD_CHACHA20_POLY1305"`
	Observer  *string  `json:"observer" example:"console"`
	Limiter   *string  `json:"limiter" example:"5mbps"`
	RLimiter  *string  `json:"rlimiter" example:""`
	Metadata  *string  `json:"metadata" example:"{\"k\":\"v\"}"`
	Ifaces    []string `json:"ifaces,omitempty"`
	Interface string   `json:"interface,omitempty"`
}

type SwaggerNodeInstallReq struct {
	ID int64 `json:"id" example:"1"`
}

type SwaggerNodeInstallResp struct {
	Static string `json:"static" example:"curl -fsSL https://panel-static.199028.xyz/network-panel/install.sh -o install.sh && sudo bash install.sh -a 1.2.3.4:6365 -s secret"`
	Github string `json:"github" example:"curl -fsSL https://raw.githubusercontent.com/NiuStar/network-panel/refs/heads/main/install.sh -o install.sh && sudo bash install.sh -a 1.2.3.4:6365 -s secret"`
	Local  string `json:"local,omitempty" example:"curl -fsSL http://1.2.3.4:6365/install.sh -o install.sh && sudo bash install.sh -a 1.2.3.4:6365 -s secret"`
}

type SwaggerNodeOpsReq struct {
	NodeID    int64  `json:"nodeId" example:"1"`
	Limit     int    `json:"limit" example:"200"`
	RequestID string `json:"requestId" example:"req-123"`
}

type SwaggerNodeDeleteReq struct {
	ID        int64 `json:"id" example:"1"`
	Uninstall bool  `json:"uninstall" example:"true"`
}

type SwaggerNodeSimpleReq struct {
	NodeID int64 `json:"nodeId" example:"1"`
}

type SwaggerNodeRangeReq struct {
	NodeID int64  `json:"nodeId" example:"1"`
	Range  string `json:"range" example:"1h"`
}

type SwaggerNetworkStatsBatchReq struct {
	Range string `json:"range" example:"1h"`
}

type SwaggerNodeSysinfoReq struct {
	NodeID int64  `json:"nodeId" example:"1"`
	Range  string `json:"range" example:"1h"`
	Limit  int    `json:"limit" example:"200"`
}

type SwaggerNodeInterfacesReq struct {
	NodeID int64 `json:"nodeId" example:"1"`
}

type SwaggerNodeQueryServicesReq struct {
	NodeID int64  `json:"nodeId" example:"1"`
	Filter string `json:"filter" example:"gost"`
}

type SwaggerNodeNQStreamReq struct {
	Secret    string `json:"secret" example:"node-secret"`
	RequestID string `json:"requestId" example:"req-123"`
	Chunk     string `json:"chunk" example:"log line"`
	Done      bool   `json:"done" example:"false"`
	TimeMs    *int64 `json:"timeMs" example:"1700000000000"`
}
