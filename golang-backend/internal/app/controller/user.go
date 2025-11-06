package controller

import (
	"net/http"
	"time"

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
		RoleID:     1,
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
	for i := range users {
		users[i].Pwd = ""
	}
	c.JSON(http.StatusOK, response.Ok(users))
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
	userInfo := gin.H{
		"flow":          user.Flow,
		"inFlow":        user.InFlow,
		"outFlow":       user.OutFlow,
		"num":           user.Num,
		"expTime":       user.ExpTime,
		"flowResetTime": user.FlowResetTime,
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
