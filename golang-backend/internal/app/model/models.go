package model

import "gorm.io/gorm"

type BaseEntity struct {
    ID          int64  `gorm:"primaryKey;column:id" json:"id"`
    CreatedTime int64  `gorm:"column:created_time" json:"createdTime"`
    UpdatedTime int64  `gorm:"column:updated_time" json:"updatedTime"`
    Status      *int   `gorm:"column:status" json:"status,omitempty"`
}

type User struct {
    BaseEntity
    User          string `gorm:"column:user" json:"user"`
    Pwd           string `gorm:"column:pwd" json:"pwd"`
    RoleID        int    `gorm:"column:role_id" json:"role_id"`
    ExpTime       *int64 `gorm:"column:exp_time" json:"exp_time,omitempty"`
    Flow          int64  `gorm:"column:flow" json:"flow"`
    InFlow        int64  `gorm:"column:in_flow" json:"in_flow"`
    OutFlow       int64  `gorm:"column:out_flow" json:"out_flow"`
    Num           int    `gorm:"column:num" json:"num"`
    FlowResetTime int64  `gorm:"column:flow_reset_time" json:"flow_reset_time"`
}

func (User) TableName() string { return "user" }

type Node struct {
    BaseEntity
    Name     string `gorm:"column:name" json:"name"`
    Secret   string `gorm:"column:secret" json:"secret"`
    IP       string `gorm:"column:ip" json:"ip"`
    ServerIP string `gorm:"column:server_ip" json:"serverIp"`
    Version  string `gorm:"column:version" json:"version"`
    PortSta  int    `gorm:"column:port_sta" json:"portSta"`
    PortEnd  int    `gorm:"column:port_end" json:"portEnd"`
    // Billing fields
    PriceCents  *int64 `gorm:"column:price_cents" json:"priceCents,omitempty"`
    CycleDays   *int   `gorm:"column:cycle_days" json:"cycleDays,omitempty"`
    StartDateMs *int64 `gorm:"column:start_date_ms" json:"startDateMs,omitempty"`
}
func (Node) TableName() string { return "node" }

type Tunnel struct {
    BaseEntity
    Name          string   `gorm:"column:name" json:"name"`
    InNodeID      int64    `gorm:"column:in_node_id" json:"inNodeId"`
    InIP          string   `gorm:"column:in_ip" json:"inIp"`
    OutNodeID     *int64   `gorm:"column:out_node_id" json:"outNodeId,omitempty"`
    OutIP         *string  `gorm:"column:out_ip" json:"outIp,omitempty"`
    Type          int      `gorm:"column:type" json:"type"`
    Flow          int      `gorm:"column:flow" json:"flow"`
    Protocol      *string  `gorm:"column:protocol" json:"protocol,omitempty"`
    TrafficRatio  *float64 `gorm:"column:traffic_ratio" json:"trafficRatio,omitempty"`
    TCPListenAddr *string  `gorm:"column:tcp_listen_addr" json:"tcpListenAddr,omitempty"`
    UDPListenAddr *string  `gorm:"column:udp_listen_addr" json:"udpListenAddr,omitempty"`
    InterfaceName *string  `gorm:"column:interface_name" json:"interfaceName,omitempty"`
}
func (Tunnel) TableName() string { return "tunnel" }

type Forward struct {
    BaseEntity
    UserID        int64   `gorm:"column:user_id" json:"userId"`
    UserName      string  `gorm:"column:user_name" json:"userName"`
    Name          string  `gorm:"column:name" json:"name"`
    TunnelID      int64   `gorm:"column:tunnel_id" json:"tunnelId"`
    InPort        int     `gorm:"column:in_port" json:"inPort"`
    OutPort       *int    `gorm:"column:out_port" json:"outPort,omitempty"`
    RemoteAddr    string  `gorm:"column:remote_addr" json:"remoteAddr"`
    InterfaceName *string `gorm:"column:interface_name" json:"interfaceName,omitempty"`
    Strategy      *string `gorm:"column:strategy" json:"strategy,omitempty"`
    InFlow        int64   `gorm:"column:in_flow" json:"inFlow"`
    OutFlow       int64   `gorm:"column:out_flow" json:"outFlow"`
    Inx           *int    `gorm:"column:inx" json:"inx,omitempty"`
}
func (Forward) TableName() string { return "forward" }

type UserTunnel struct {
    ID            int64  `gorm:"primaryKey;column:id" json:"id"`
    UserID        int64  `gorm:"column:user_id" json:"userId"`
    TunnelID      int64  `gorm:"column:tunnel_id" json:"tunnelId"`
    Flow          int64  `gorm:"column:flow" json:"flow"`
    InFlow        int64  `gorm:"column:in_flow" json:"inFlow"`
    OutFlow       int64  `gorm:"column:out_flow" json:"outFlow"`
    FlowResetTime *int64 `gorm:"column:flow_reset_time" json:"flowResetTime,omitempty"`
    ExpTime       *int64 `gorm:"column:exp_time" json:"expTime,omitempty"`
    SpeedID       *int64 `gorm:"column:speed_id" json:"speedId,omitempty"`
    Num           int    `gorm:"column:num" json:"num"`
    Status        int    `gorm:"column:status" json:"status"`
}
func (UserTunnel) TableName() string { return "user_tunnel" }

type SpeedLimit struct {
    ID          int64  `gorm:"primaryKey;column:id" json:"id"`
    CreatedTime int64  `gorm:"column:created_time" json:"createdTime"`
    UpdatedTime int64  `gorm:"column:updated_time" json:"updatedTime"`
    Status      int    `gorm:"column:status" json:"status"`
    Name        string `gorm:"column:name" json:"name"`
    Speed       int    `gorm:"column:speed" json:"speed"`
    TunnelID    int64  `gorm:"column:tunnel_id" json:"tunnelId"`
    TunnelName  string `gorm:"column:tunnel_name" json:"tunnelName"`
}
func (SpeedLimit) TableName() string { return "speed_limit" }

type ViteConfig struct {
    ID    int64  `gorm:"primaryKey;column:id"`
    Name  string `gorm:"column:name"`
    Value string `gorm:"column:value"`
    Time  int64  `gorm:"column:time"`
}
func (ViteConfig) TableName() string { return "vite_config" }

type StatisticsFlow struct {
    ID          int64  `gorm:"primaryKey;column:id" json:"id"`
    UserID      int64  `gorm:"column:user_id" json:"userId"`
    Flow        int64  `gorm:"column:flow" json:"flow"`
    TotalFlow   int64  `gorm:"column:total_flow" json:"totalFlow"`
    Time        string `gorm:"column:time" json:"time"`
    CreatedTime int64  `gorm:"column:created_time" json:"createdTime"`
}
func (StatisticsFlow) TableName() string { return "statistics_flow" }

// Ensure models compile with gorm
var _ *gorm.DB

// NodeOpLog stores generic operation results from node (RunScript/WriteFile/RestartService/StopService)
type NodeOpLog struct {
    ID        int64  `gorm:"primaryKey;column:id" json:"id"`
    TimeMs    int64  `gorm:"column:time_ms" json:"timeMs"`
    NodeID    int64  `gorm:"column:node_id" json:"nodeId"`
    Cmd       string `gorm:"column:cmd" json:"cmd"`
    RequestID string `gorm:"column:request_id" json:"requestId"`
    Success   int    `gorm:"column:success" json:"success"` // 1 ok, 0 fail
    Message   string `gorm:"column:message" json:"message"`
    Stdout    *string `gorm:"column:stdout" json:"stdout,omitempty"`
    Stderr    *string `gorm:"column:stderr" json:"stderr,omitempty"`
}
func (NodeOpLog) TableName() string { return "node_op_log" }

// ExitSetting persists the last configured SS exit settings per node
type ExitSetting struct {
    BaseEntity
    NodeID   int64   `gorm:"column:node_id;uniqueIndex" json:"nodeId"`
    Port     int     `gorm:"column:port" json:"port"`
    Password string  `gorm:"column:password" json:"password"`
    Method   string  `gorm:"column:method" json:"method"`
    Observer *string `gorm:"column:observer" json:"observer,omitempty"`
    Limiter  *string `gorm:"column:limiter" json:"limiter,omitempty"`
    RLimiter *string `gorm:"column:rlimiter" json:"rlimiter,omitempty"`
    // Metadata is a JSON string storing arbitrary key-values for handler.metadata
    Metadata *string `gorm:"column:metadata" json:"metadata,omitempty"`
}

func (ExitSetting) TableName() string { return "exit_setting" }

// ProbeTarget: global list of IPs to ping
type ProbeTarget struct {
    ID          int64  `gorm:"primaryKey;column:id" json:"id"`
    CreatedTime int64  `gorm:"column:created_time" json:"createdTime"`
    UpdatedTime int64  `gorm:"column:updated_time" json:"updatedTime"`
    Status      int    `gorm:"column:status" json:"status"`
    Name        string `gorm:"column:name" json:"name"`
    IP          string `gorm:"column:ip" json:"ip"`
}
func (ProbeTarget) TableName() string { return "probe_target" }

// NodeProbeResult: time series of ping results per node per target
type NodeProbeResult struct {
    ID       int64 `gorm:"primaryKey;column:id" json:"id"`
    NodeID   int64 `gorm:"column:node_id" json:"nodeId"`
    TargetID int64 `gorm:"column:target_id" json:"targetId"`
    RTTMs    int   `gorm:"column:rtt_ms" json:"rttMs"`
    OK       int   `gorm:"column:ok" json:"ok"` // 1 ok, 0 fail
    TimeMs   int64 `gorm:"column:time_ms" json:"timeMs"`
}
func (NodeProbeResult) TableName() string { return "node_probe_result" }

// NodeDisconnectLog: records node offline/online durations
type NodeDisconnectLog struct {
    ID        int64  `gorm:"primaryKey;column:id" json:"id"`
    NodeID    int64  `gorm:"column:node_id" json:"nodeId"`
    DownAtMs  int64  `gorm:"column:down_at_ms" json:"downAtMs"`
    UpAtMs    *int64 `gorm:"column:up_at_ms" json:"upAtMs,omitempty"`
    DurationS *int64 `gorm:"column:duration_s" json:"durationS,omitempty"`
}
func (NodeDisconnectLog) TableName() string { return "node_disconnect_log" }

// NodeSysInfo stores periodic system info reported by agent for timeseries
type NodeSysInfo struct {
    ID        int64   `gorm:"primaryKey;column:id" json:"id"`
    NodeID    int64   `gorm:"column:node_id" json:"nodeId"`
    TimeMs    int64   `gorm:"column:time_ms" json:"timeMs"`
    Uptime    int64   `gorm:"column:uptime" json:"uptime"`
    BytesRx   int64   `gorm:"column:bytes_rx" json:"bytesRx"`
    BytesTx   int64   `gorm:"column:bytes_tx" json:"bytesTx"`
    CPU       float64 `gorm:"column:cpu" json:"cpu"`
    Mem       float64 `gorm:"column:mem" json:"mem"`
}
func (NodeSysInfo) TableName() string { return "node_sysinfo" }

// NodeRuntime stores latest runtime metadata like interfaces list
type NodeRuntime struct {
    NodeID      int64   `gorm:"primaryKey;column:node_id" json:"nodeId"`
    Interfaces  *string `gorm:"column:interfaces" json:"interfaces,omitempty"` // JSON array string
    UpdatedTime int64   `gorm:"column:updated_time" json:"updatedTime"`
}
func (NodeRuntime) TableName() string { return "node_runtime" }

// ForwardMidPort persists expected mid-hop listening ports for a forward's multi-level path.
type ForwardMidPort struct {
    ID          int64 `gorm:"primaryKey;column:id" json:"id"`
    ForwardID   int64 `gorm:"column:forward_id;uniqueIndex:uniq_fwd_mid_idx,priority:1" json:"forwardId"`
    Idx         int   `gorm:"column:idx;uniqueIndex:uniq_fwd_mid_idx,priority:2" json:"idx"` // 0-based position in path
    NodeID      int64 `gorm:"column:node_id" json:"nodeId"`
    Port        int   `gorm:"column:port" json:"port"`
    UpdatedTime int64 `gorm:"column:updated_time" json:"updatedTime"`
}
func (ForwardMidPort) TableName() string { return "forward_mid_port" }
