package controller

import (
    "net/http"
    "time"
    "strconv"
    "strings"

    "network-panel/golang-backend/internal/app/dto"
    "network-panel/golang-backend/internal/app/model"
    "network-panel/golang-backend/internal/app/response"
    "network-panel/golang-backend/internal/app/util"
    dbpkg "network-panel/golang-backend/internal/db"

    "github.com/gin-gonic/gin"
)

// POST /api/v1/user/login
func UserLogin(c *gin.Context) {
	var req dto.LoginDto
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}

	// Captcha check is stubbed: always passes if configured disabled or empty
	// Validate user
	var user model.User
	if err := dbpkg.DB.Where("user = ?", req.Username).First(&user).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("账号或密码错误"))
		return
	}
	if user.Pwd != util.MD5(req.Password) {
		c.JSON(http.StatusOK, response.ErrMsg("账号或密码错误"))
		return
	}
	if user.Status != nil && *user.Status == 0 {
		c.JSON(http.StatusOK, response.ErrMsg("账户停用"))
		return
	}
	token := util.GenerateToken(user.ID, user.User, user.RoleID)
	requireChange := (user.User == "admin_user" || req.Password == "admin_user")
	c.JSON(http.StatusOK, response.Ok(gin.H{
		"token":                 token,
		"name":                  user.User,
		"role_id":               user.RoleID,
		"requirePasswordChange": requireChange,
	}))
}

// POST /api/v1/user/register {username, password}
// Public registration; guarded by vite_config: registration_enabled=true
func UserRegister(c *gin.Context) {
    var p struct { Username string `json:"username"`; Password string `json:"password"` }
    if err := c.ShouldBindJSON(&p); err != nil || p.Username == "" || len(p.Password) < 6 {
        c.JSON(http.StatusOK, response.ErrMsg("参数错误")); return
    }
    // check flag
    var cfg model.ViteConfig
    if err := dbpkg.DB.Where("name = ?", "registration_enabled").First(&cfg).Error; err != nil || cfg.Value != "true" {
        c.JSON(http.StatusOK, response.ErrMsg("暂未开放注册")); return
    }
    // uniqueness
    var cnt int64
    dbpkg.DB.Model(&model.User{}).Where("user = ?", p.Username).Count(&cnt)
    if cnt > 0 { c.JSON(http.StatusOK, response.ErrMsg("用户名已存在")); return }
    now := time.Now().UnixMilli(); status := 1
    // default quotas from config:
    // - registration_default_flow_gb: user flow quota (GB)
    // - registration_default_forward: per-user forward count quota
    // Note: tunnel count quota is enforced at create-time via controller/tunnel.go using registration_default_num.
    defFlowGb := int64(0)
    defForward := 20 // forwards
    var c1, c3 model.ViteConfig
    dbpkg.DB.Where("name=?", "registration_default_flow_gb").First(&c1)
    if v, err := strconv.ParseInt(strings.TrimSpace(c1.Value), 10, 64); err==nil && v>=0 { defFlowGb = v }
    dbpkg.DB.Where("name=?", "registration_default_forward").First(&c3)
    if v, err := strconv.Atoi(strings.TrimSpace(c3.Value)); err==nil && v>0 { defForward = v }
    u := model.User{ BaseEntity: model.BaseEntity{CreatedTime: now, UpdatedTime: now, Status: &status},
        User: p.Username, Pwd: util.MD5(p.Password), RoleID: 1, Flow: defFlowGb, InFlow: 0, OutFlow: 0, Num: defForward, FlowResetTime: 0 }
    if err := dbpkg.DB.Create(&u).Error; err != nil { c.JSON(http.StatusOK, response.ErrMsg("注册失败")); return }
    // auto login: return token
    token := util.GenerateToken(u.ID, u.User, u.RoleID)
    c.JSON(http.StatusOK, response.Ok(gin.H{"token": token, "name": u.User, "role_id": u.RoleID}))
}

// POST /api/v1/user/create
func UserCreate(c *gin.Context) {
	var req dto.UserDto
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	// uniqueness
	var cnt int64
	dbpkg.DB.Model(&model.User{}).Where("user = ?", req.User).Count(&cnt)
	if cnt > 0 {
		c.JSON(http.StatusOK, response.ErrMsg("用户名已存在"))
		return
	}
	now := time.Now().UnixMilli()
	status := 1
    u := model.User{
        BaseEntity: model.BaseEntity{CreatedTime: now, UpdatedTime: now, Status: &status},
        User:       req.User,
        Pwd:        util.MD5(req.Pwd),
        RoleID:     2, // admin-created limited user (forwards-only)
        ExpTime:    &req.ExpTime,
        Flow:       req.Flow,
        InFlow:     0, OutFlow: 0,
        Num:           req.Num,
        FlowResetTime: req.FlowResetTime,
    }
	if err := dbpkg.DB.Create(&u).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("用户创建失败"))
		return
	}
	c.JSON(http.StatusOK, response.OkMsg("用户创建成功"))
}

// (helper provided in response package)

// POST /api/v1/user/list
func UserList(c *gin.Context) {
    var users []model.User
    dbpkg.DB.Where("role_id <> ?", 0).Find(&users)
    // compute usedBilled per user: sum over forwards with tunnel.flow rule (single uses max(in,out), double uses in+out)
    type agg struct{ UserID int64; Used int64 }
    var aggs []agg
    dbpkg.DB.Table("forward f").
        Select("f.user_id as user_id, SUM(CASE WHEN t.flow = 1 THEN (CASE WHEN f.in_flow > f.out_flow THEN f.in_flow ELSE f.out_flow END) ELSE (f.in_flow + f.out_flow) END) as used").
        Joins("left join tunnel t on t.id = f.tunnel_id").
        Group("f.user_id").Scan(&aggs)
    usedMap := map[int64]int64{}
    for _, a := range aggs { usedMap[a.UserID] = a.Used }

    // forward count per user
    type aggF struct{ UserID int64; C int64 }
    var af []aggF
    dbpkg.DB.Table("forward").Select("user_id, COUNT(1) as c").Group("user_id").Scan(&af)
    fMap := map[int64]int64{}; for _, a := range af { fMap[a.UserID] = a.C }
    // tunnel count per user (distinct tunnels in forwards)
    var at []aggF
    dbpkg.DB.Table("forward f").Select("f.user_id as user_id, COUNT(DISTINCT f.tunnel_id) as c").Group("f.user_id").Scan(&at)
    tMap := map[int64]int64{}; for _, a := range at { tMap[a.UserID] = a.C }
    // node count per user (distinct in/out nodes across user's forwards' tunnels)
    var an []aggF
    dbpkg.DB.Raw(`SELECT x.user_id, COUNT(DISTINCT x.nid) as c
        FROM (
            SELECT f.user_id, t.in_node_id as nid FROM forward f LEFT JOIN tunnel t ON t.id=f.tunnel_id
            UNION ALL
            SELECT f.user_id, t.out_node_id as nid FROM forward f LEFT JOIN tunnel t ON t.id=f.tunnel_id WHERE t.out_node_id IS NOT NULL
        ) x GROUP BY x.user_id`).Scan(&an)
    nMap := map[int64]int64{}; for _, a := range an { nMap[a.UserID] = a.C }

    // normalize to camelCase for frontend consistency
    out := make([]map[string]any, 0, len(users))
    for i := range users {
        u := users[i]
        m := map[string]any{
            "id":             u.ID,
            "createdTime":    u.CreatedTime,
            "updatedTime":    u.UpdatedTime,
            "status":         u.Status,
            "user":           u.User,
            "roleId":         u.RoleID,
            "expTime":        u.ExpTime,
            "flow":           u.Flow,
            "inFlow":         u.InFlow,
            "outFlow":        u.OutFlow,
            "num":            u.Num,
            "flowResetTime":  u.FlowResetTime,
            "usedBilled":     usedMap[u.ID],
            "forwardCount":   fMap[u.ID],
            "tunnelCount":    tMap[u.ID],
            "nodeCount":      nMap[u.ID],
        }
        out = append(out, m)
    }
    c.JSON(http.StatusOK, response.Ok(out))
}

// POST /api/v1/user/update
func UserUpdate(c *gin.Context) {
	var req dto.UserUpdateDto
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var u model.User
	if err := dbpkg.DB.First(&u, req.ID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("用户不存在"))
		return
	}
	if req.User != "" {
		var cnt int64
		dbpkg.DB.Model(&model.User{}).Where("user = ? AND id <> ?", req.User, req.ID).Count(&cnt)
		if cnt > 0 {
			c.JSON(http.StatusOK, response.ErrMsg("用户名已被其他用户使用"))
			return
		}
		u.User = req.User
	}
	if req.Pwd != nil {
		u.Pwd = util.MD5(*req.Pwd)
	}
	if req.Flow != nil {
		u.Flow = *req.Flow
	}
	if req.Num != nil {
		u.Num = *req.Num
	}
	if req.ExpTime != nil {
		u.ExpTime = req.ExpTime
	}
	if req.FlowResetTime != nil {
		u.FlowResetTime = *req.FlowResetTime
	}
	if req.Status != nil {
		u.Status = req.Status
	}
	u.UpdatedTime = time.Now().UnixMilli()
	if err := dbpkg.DB.Save(&u).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("用户更新失败"))
		return
	}
	c.JSON(http.StatusOK, response.OkMsg("用户更新成功"))
}

// POST /api/v1/user/delete {"id":...}
func UserDelete(c *gin.Context) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var u model.User
	if err := dbpkg.DB.First(&u, p.ID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("用户不存在"))
		return
	}
	if u.RoleID == 0 {
		c.JSON(http.StatusOK, response.ErrMsg("不能删除管理员用户"))
		return
	}
	// cascade deletions: forward, user_tunnel, statistics_flow (best-effort)
	dbpkg.DB.Where("user_id = ?", p.ID).Delete(&model.Forward{})
	dbpkg.DB.Where("user_id = ?", p.ID).Delete(&model.UserTunnel{})
	dbpkg.DB.Where("user_id = ?", p.ID).Delete(&model.StatisticsFlow{})
	if err := dbpkg.DB.Delete(&u).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("用户删除失败"))
		return
	}
	c.JSON(http.StatusOK, response.OkMsg("用户及关联数据删除成功"))
}

// POST /api/v1/user/package
func UserPackage(c *gin.Context) {
	// Return aggregated package info as frontend expects
	uidInf, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusOK, response.ErrMsg("用户未登录或token无效"))
		return
	}
	uid := uidInf.(int64)

	var user model.User
	if err := dbpkg.DB.First(&user, uid).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("用户不存在"))
		return
	}

    // build userInfo payload (camelCase)
    // compute billed used (sum over forwards by tunnel.flow rule)
    type agg struct{ Used int64 }
    var a agg
    dbpkg.DB.Table("forward f").
        Select("SUM(CASE WHEN t.flow = 1 THEN (CASE WHEN f.in_flow > f.out_flow THEN f.in_flow ELSE f.out_flow END) ELSE (f.in_flow + f.out_flow) END) as used").
        Joins("left join tunnel t on t.id = f.tunnel_id").
        Where("f.user_id = ?", uid).Scan(&a)

    userInfo := gin.H{
        "flow":          user.Flow,
        "inFlow":        user.InFlow,
        "outFlow":       user.OutFlow,
        "num":           user.Num,
        "expTime":       user.ExpTime,
        "flowResetTime": user.FlowResetTime,
        "usedBilled":    a.Used,
    }

	// tunnel permissions with names and tunnelFlow
	var tunnelPermissions []struct {
		model.UserTunnel
		TunnelName     string  `json:"tunnelName"`
		SpeedLimitName *string `json:"speedLimitName,omitempty"`
		TunnelFlow     *int    `json:"tunnelFlow,omitempty"`
	}
	dbpkg.DB.Table("user_tunnel ut").
		Select("ut.*, t.name as tunnel_name, sl.name as speed_limit_name, t.flow as tunnel_flow").
		Joins("left join tunnel t on t.id = ut.tunnel_id").
		Joins("left join speed_limit sl on sl.id = ut.speed_id").
		Where("ut.user_id = ?", uid).
		Scan(&tunnelPermissions)

	// forwards with tunnel name and in ip
	var forwards []struct {
		model.Forward
		TunnelName string `json:"tunnelName"`
		InIp       string `json:"inIp"`
	}
	dbpkg.DB.Table("forward f").
		Select("f.*, t.name as tunnel_name, t.in_ip as in_ip").
		Joins("left join tunnel t on t.id = f.tunnel_id").
		Where("f.user_id = ?", uid).
		Scan(&forwards)

	// recent statistics flows (optional; return whatever exists)
	var statisticsFlows []model.StatisticsFlow
	dbpkg.DB.Where("user_id = ?", uid).Order("created_time desc").Limit(200).Find(&statisticsFlows)

	c.JSON(http.StatusOK, response.Ok(gin.H{
		"userInfo":          userInfo,
		"tunnelPermissions": tunnelPermissions,
		"forwards":          forwards,
		"statisticsFlows":   statisticsFlows,
	}))
}

// POST /api/v1/user/updatePassword
func UserUpdatePassword(c *gin.Context) {
	uidInf, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusOK, response.ErrMsg("用户未登录或token无效"))
		return
	}
	uid := uidInf.(int64)
	var req dto.ChangePasswordDto
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	if req.NewPassword != req.ConfirmPassword {
		c.JSON(http.StatusOK, response.ErrMsg("新密码和确认密码不匹配"))
		return
	}
	var u model.User
	if err := dbpkg.DB.First(&u, uid).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("用户不存在"))
		return
	}
	if u.Pwd != util.MD5(req.CurrentPassword) {
		c.JSON(http.StatusOK, response.ErrMsg("当前密码错误"))
		return
	}
	// username unique
	var cnt int64
	dbpkg.DB.Model(&model.User{}).Where("user = ? AND id <> ?", req.NewUsername, uid).Count(&cnt)
	if cnt > 0 {
		c.JSON(http.StatusOK, response.ErrMsg("用户名已被其他用户使用"))
		return
	}
	u.User = req.NewUsername
	u.Pwd = util.MD5(req.NewPassword)
	u.UpdatedTime = time.Now().UnixMilli()
	if err := dbpkg.DB.Save(&u).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("用户更新失败"))
		return
	}
	c.JSON(http.StatusOK, response.OkMsg("账号密码修改成功"))
}

// POST /api/v1/user/reset
func UserReset(c *gin.Context) {
	var req dto.ResetFlowDto
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	if req.Type == 1 {
		// reset user flow
		dbpkg.DB.Model(&model.User{}).Where("id = ?", req.ID).Updates(map[string]any{"in_flow": 0, "out_flow": 0})
	} else {
		dbpkg.DB.Model(&model.UserTunnel{}).Where("id = ?", req.ID).Updates(map[string]any{"in_flow": 0, "out_flow": 0})
	}
	c.JSON(http.StatusOK, response.OkNoData())
}
