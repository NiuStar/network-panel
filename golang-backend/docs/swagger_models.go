package docs

import (
	"network-panel/golang-backend/internal/app/dto"
	"network-panel/golang-backend/internal/app/model"
)

// RespBase 通用返回包装
type RespBase struct {
	Code int    `json:"code" example:"0"`
	Msg  string `json:"msg" example:"操作成功"`
	Ts   int64  `json:"ts" example:"1700000000000"`
}

// LoginResponse 登录返回
type LoginResponse struct {
	RespBase
	Data LoginResponseData `json:"data"`
}

type LoginResponseData struct {
	Token                 string `json:"token" example:"Bearer xxx"`
	Name                  string `json:"name" example:"admin"`
	RoleID                int    `json:"role_id" example:"0"`
	RequirePasswordChange bool   `json:"requirePasswordChange" example:"false"`
}

// RegisterResponse 注册返回
type RegisterResponse struct {
	RespBase
	Data LoginResponseData `json:"data"`
}

// UserInfoPackage 用户套餐与配额
type UserInfoPackage struct {
	Flow          int64  `json:"flow" example:"1024"`
	InFlow        int64  `json:"inFlow" example:"0"`
	OutFlow       int64  `json:"outFlow" example:"0"`
	Num           int    `json:"num" example:"20"`
	ExpTime       *int64 `json:"expTime" example:"1700000000000"`
	FlowResetTime int64  `json:"flowResetTime" example:"0"`
	UsedBilled    int64  `json:"usedBilled" example:"0"`
}

type PackageTunnelPermission struct {
	model.UserTunnel
	TunnelName     string  `json:"tunnelName" example:"default-tunnel"`
	SpeedLimitName *string `json:"speedLimitName,omitempty" example:"10Mbps"`
	TunnelFlow     *int    `json:"tunnelFlow,omitempty" example:"2"`
}

type PackageForward struct {
	model.Forward
	TunnelName string `json:"tunnelName" example:"default-tunnel"`
	InIp       string `json:"inIp" example:"1.1.1.1"`
}

type PackageFlowPoint struct {
	Time string `json:"time" example:"01-02 15:00"`
	Flow int64  `json:"flow" example:"1024"`
}

type UserPackageResponse struct {
	RespBase
	Data struct {
		UserInfo          UserInfoPackage           `json:"userInfo"`
		TunnelPermissions []PackageTunnelPermission `json:"tunnelPermissions"`
		Forwards          []PackageForward          `json:"forwards"`
		StatisticsFlows   []PackageFlowPoint        `json:"statisticsFlows"`
	} `json:"data"`
}

// UserListItem 用户列表项
type UserListItem struct {
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

type UserListResponse struct {
	RespBase
	Data []UserListItem `json:"data"`
}

type ConfigListResponse struct {
	RespBase
	Data map[string]string `json:"data"`
}

type ConfigGetResponse struct {
	RespBase
	Data string `json:"data" example:"some-value"`
}

// ChangePasswordRequest 请求体
type ChangePasswordRequest = dto.ChangePasswordDto

// LoginRequest 请求体
type LoginRequest = dto.LoginDto

type VersionResponse struct {
	RespBase
	Data VersionInfo `json:"data"`
}

type VersionInfo struct {
	Server   string `json:"server" example:"1.0.1"`
	Agent    string `json:"agent" example:"go-agent-1.0.1"`
	Agent2   string `json:"agent2" example:"go-agent2-1.0.1"`
	Center   string `json:"center" example:"https://center.example.com"`
	CenterOn bool   `json:"centerOn" example:"true"`
}

type VersionLatestResponse struct {
	RespBase
	Data struct {
		Tag    string      `json:"tag" example:"v1.0.1"`
		Name   string      `json:"name" example:"Release name"`
		Assets interface{} `json:"assets"`
	} `json:"data"`
}

type VersionUpgradeResponse struct {
	RespBase
	Data struct {
		Tag     interface{} `json:"tag"`
		Created interface{} `json:"created"`
		Logs    []string    `json:"logs"`
		Restart interface{} `json:"restart,omitempty"`
		Errors  []string    `json:"errors,omitempty"`
	} `json:"data"`
}
