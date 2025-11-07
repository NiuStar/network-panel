package dto

// Common
type LoginDto struct {
    Username string `json:"username" binding:"required"`
    Password string `json:"password" binding:"required"`
    CaptchaID string `json:"captchaId"`
    CaptchaData interface{} `json:"captchaData"`
}

type UserDto struct {
    User          string `json:"user" binding:"required"`
    Pwd           string `json:"pwd" binding:"required"`
    Flow          int64  `json:"flow"`
    Num           int    `json:"num"`
    ExpTime       int64  `json:"expTime"`
    FlowResetTime int64  `json:"flowResetTime"`
    Status        *int   `json:"status"`
}

type UserUpdateDto struct {
    ID            int64  `json:"id" binding:"required"`
    User          string `json:"user"`
    Pwd           *string `json:"pwd"`
    Flow          *int64  `json:"flow"`
    Num           *int    `json:"num"`
    ExpTime       *int64  `json:"expTime"`
    FlowResetTime *int64  `json:"flowResetTime"`
    Status        *int    `json:"status"`
}

type ChangePasswordDto struct {
    NewUsername     string `json:"newUsername" binding:"required"`
    CurrentPassword string `json:"currentPassword" binding:"required"`
    NewPassword     string `json:"newPassword" binding:"required"`
    ConfirmPassword string `json:"confirmPassword" binding:"required"`
}

type ResetFlowDto struct {
    Type int   `json:"type" binding:"required"` // 1 user, else tunnel
    ID   int64 `json:"id" binding:"required"`
}

// Node
type NodeDto struct {
    Name    string `json:"name" binding:"required"`
    IP      string `json:"ip" binding:"required"`
    ServerIP string `json:"serverIp"`
    PortSta int    `json:"portSta"`
    PortEnd int    `json:"portEnd"`
    PriceCents  *int64 `json:"priceCents"`
    // New: prefer cycleMonths (1/3/6/12). cycleDays kept for backward-compat (30/90/180/365)
    CycleMonths *int   `json:"cycleMonths"`
    CycleDays   *int   `json:"cycleDays"`
    StartDateMs *int64 `json:"startDateMs"`
}

type NodeUpdateDto struct {
    ID      int64  `json:"id" binding:"required"`
    Name    string `json:"name"`
    IP      string `json:"ip"`
    ServerIP string `json:"serverIp"`
    PortSta int    `json:"portSta"`
    PortEnd int    `json:"portEnd"`
    PriceCents  *int64 `json:"priceCents"`
    CycleMonths *int   `json:"cycleMonths"`
    CycleDays   *int   `json:"cycleDays"`
    StartDateMs *int64 `json:"startDateMs"`
}

// Tunnel
type TunnelDto struct {
    Name          string `json:"name" binding:"required"`
    InNodeID      int64  `json:"inNodeId" binding:"required"`
    OutNodeID     *int64 `json:"outNodeId"`
    Type          int    `json:"type" binding:"required"`
    Flow          int    `json:"flow"`
    Protocol      *string `json:"protocol"`
    TrafficRatio  *float64 `json:"trafficRatio"`
    TCPListenAddr *string `json:"tcpListenAddr"`
    UDPListenAddr *string `json:"udpListenAddr"`
    InterfaceName *string `json:"interfaceName"`
}

type TunnelUpdateDto struct {
    ID            int64   `json:"id" binding:"required"`
    Name          string  `json:"name"`
    Flow          int64   `json:"flow"`
    TCPListenAddr *string `json:"tcpListenAddr"`
    UDPListenAddr *string `json:"udpListenAddr"`
    Protocol      *string `json:"protocol"`
    InterfaceName *string `json:"interfaceName"`
    TrafficRatio  *float64 `json:"trafficRatio"`
}

// Forward
type ForwardDto struct {
    Name       string  `json:"name" binding:"required"`
    TunnelID   int64   `json:"tunnelId" binding:"required"`
    InPort     *int    `json:"inPort"`
    RemoteAddr string  `json:"remoteAddr" binding:"required"`
    Strategy   *string `json:"strategy"`
    InterfaceName *string `json:"interfaceName"`
    // optional SS tunnel parameters (for tunnel-forward)
    SsPort     *int    `json:"ssPort"`
    SsPassword *string `json:"ssPassword"`
    SsMethod   *string `json:"ssMethod"`
}

type ForwardUpdateDto struct {
    ID         int64   `json:"id" binding:"required"`
    Name       string  `json:"name"`
    TunnelID   int64   `json:"tunnelId"`
    InPort     *int    `json:"inPort"`
    RemoteAddr string  `json:"remoteAddr"`
    Strategy   *string `json:"strategy"`
    InterfaceName *string `json:"interfaceName"`
    // optional SS tunnel parameters
    SsPort     *int    `json:"ssPort"`
    SsPassword *string `json:"ssPassword"`
    SsMethod   *string `json:"ssMethod"`
}

// Speed limit
type SpeedLimitDto struct {
    Name      string `json:"name" binding:"required"`
    Speed     int    `json:"speed" binding:"required"`
    TunnelID  int64  `json:"tunnelId" binding:"required"`
    TunnelName string `json:"tunnelName" binding:"required"`
}

type SpeedLimitUpdateDto struct {
    ID        int64  `json:"id" binding:"required"`
    Name      string `json:"name"`
    Speed     int    `json:"speed"`
    TunnelID  int64  `json:"tunnelId"`
    TunnelName string `json:"tunnelName"`
}

// User tunnel
type UserTunnelDto struct {
    UserID   int64  `json:"userId" binding:"required"`
    TunnelID int64  `json:"tunnelId" binding:"required"`
    Flow     int64  `json:"flow"`
    Num      int    `json:"num"`
    FlowResetTime *int64 `json:"flowResetTime"`
    ExpTime  *int64 `json:"expTime"`
    SpeedID  *int64 `json:"speedId"`
    Status   *int   `json:"status"`
}

type UserTunnelQueryDto struct { UserID int64 `json:"userId" binding:"required"` }

type UserTunnelUpdateDto struct {
    ID       int64  `json:"id" binding:"required"`
    Flow     int64  `json:"flow"`
    Num      int    `json:"num"`
    FlowResetTime *int64 `json:"flowResetTime"`
    ExpTime  *int64 `json:"expTime"`
    SpeedID  *int64 `json:"speedId"`
    Status   *int   `json:"status"`
}

// Captcha
type CaptchaVerifyDto struct {
    ID   string      `json:"id"`
    Data interface{} `json:"data"`
}

// Flow upload from nodes
type FlowDto struct {
    N string `json:"n"` // service name: forwardId_userId_userTunnelId
    U int64  `json:"u"` // upload bytes
    D int64  `json:"d"` // download bytes
}
